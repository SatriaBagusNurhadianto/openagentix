// Package notify is the policy layer between the MCP tools and the platform
// clients. It decides whether a message may be sent (known destination, size,
// rate, concurrency, idempotency), cleans it, and relays it. It never lets the
// caller name anything but a configured destination, and it logs outcomes and
// sizes only: never message text, titles, keys, ids, URLs or tokens.
package notify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openagentix/openagentix/services/notify/internal/delivery"
	"github.com/openagentix/openagentix/services/notify/internal/destinations"
	"github.com/openagentix/openagentix/services/notify/internal/idempotency"
	"github.com/openagentix/openagentix/services/notify/internal/ratelimit"
	"github.com/openagentix/openagentix/services/notify/internal/sanitize"
)

// MaxTitleChars bounds the optional title.
const MaxTitleChars = 100

var keyRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Kind classifies an Error so callers can react without parsing messages.
type Kind int

const (
	Invalid Kind = iota
	UnknownDestination
	TooLong
	RateLimited
	Busy
	Upstream
	Timeout
	Disabled
)

// Error is a failure whose message is safe to show to an agent.
type Error struct {
	Kind       Kind
	Msg        string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Msg }

// Request is what an agent may choose: a destination name and content.
type Request struct {
	Destination    string
	Text           string
	Title          string
	IdempotencyKey string
}

// Result describes a delivered (or deduplicated) message.
type Result struct {
	Destination  string
	Platform     string
	DeliveryID   string
	SentAt       time.Time
	Deduplicated bool
}

// DestinationInfo is the public view of a destination.
type DestinationInfo struct {
	Name         string
	Platform     string
	Description  string
	MaxTextChars int
}

// Options configure a Service.
type Options struct {
	Destinations          []destinations.Destination
	Label                 string
	MaxInFlight           int
	IdempotencyWindow     time.Duration
	IdempotencyMaxEntries int
	Log                   *slog.Logger
	Now                   func() time.Time // tests
}

type dest struct {
	destinations.Destination
	limiter *ratelimit.Limiter
}

// Service sends notifications.
type Service struct {
	dests  map[string]*dest
	names  []string
	header string
	sem    chan struct{}
	idem   *idempotency.Cache[Result]
	log    *slog.Logger
	now    func() time.Time
}

// New builds a Service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.MaxInFlight < 1 {
		o.MaxInFlight = 1
	}
	s := &Service{
		dests:  map[string]*dest{},
		header: sanitize.Header(o.Label),
		sem:    make(chan struct{}, o.MaxInFlight),
		idem:   idempotency.New[Result](o.IdempotencyWindow, max(o.IdempotencyMaxEntries, 1), o.Now),
		log:    o.Log,
		now:    o.Now,
	}
	for _, d := range o.Destinations {
		s.dests[d.Name] = &dest{Destination: d, limiter: ratelimit.New(d.Limits.RatePerMinute, d.Limits.RatePerHour, o.Now)}
		s.names = append(s.names, d.Name)
	}
	sort.Strings(s.names)
	return s
}

// Ready reports whether the service can deliver anything.
func (s *Service) Ready() error {
	if len(s.dests) == 0 {
		return errors.New("no destinations are configured: every send is refused")
	}
	return nil
}

// List describes the configured destinations, sorted by name.
func (s *Service) List() []DestinationInfo {
	out := make([]DestinationInfo, 0, len(s.names))
	for _, n := range s.names {
		d := s.dests[n]
		out = append(out, DestinationInfo{Name: d.Name, Platform: d.Platform, Description: d.Description, MaxTextChars: d.Limits.MaxTextChars})
	}
	return out
}

// Send validates, rate-limits and delivers one message.
func (s *Service) Send(ctx context.Context, req Request) (Result, error) {
	start := s.now()
	began := time.Now()
	var d *dest
	textChars, outcome, detail := 0, "", ""
	defer func() {
		attrs := []any{"outcome", outcome, "text_chars", textChars, "duration_ms", time.Since(began).Milliseconds()}
		if d != nil {
			attrs = append([]any{"destination", d.Name, "platform", d.Platform}, attrs...)
		}
		if detail != "" {
			attrs = append(attrs, "detail", detail)
		}
		lvl := slog.LevelInfo
		if outcome != "sent" && outcome != "duplicate" {
			lvl = slog.LevelWarn
		}
		s.log.Log(ctx, lvl, "notify", attrs...)
	}()
	fail := func(o string, e *Error) (Result, error) { outcome = o; return Result{}, e }

	if len(s.dests) == 0 {
		return fail("disabled", &Error{Kind: Disabled, Msg: "notifications are disabled: no destinations are configured"})
	}
	d = s.dests[req.Destination]
	if d == nil {
		return fail("unknown_destination", &Error{Kind: UnknownDestination, Msg: fmt.Sprintf("unknown destination %q: valid destinations are: %s", clip(sanitize.Line(req.Destination), 40), strings.Join(s.names, ", "))})
	}

	text := strings.TrimSpace(sanitize.NeutraliseHeader(sanitize.Clean(req.Text), s.header))
	title := sanitize.NeutraliseHeader(sanitize.Line(req.Title), s.header)
	textChars = sanitize.RuneCount(text)
	switch {
	case text == "":
		return fail("invalid", &Error{Kind: Invalid, Msg: "text must not be empty"})
	case textChars > d.Limits.MaxTextChars:
		return fail("too_long", &Error{Kind: TooLong, Msg: fmt.Sprintf("text is too long: %d characters; the limit for destination %q is %d. Shorten it and try again", textChars, d.Name, d.Limits.MaxTextChars)})
	case sanitize.RuneCount(title) > MaxTitleChars:
		return fail("too_long", &Error{Kind: TooLong, Msg: fmt.Sprintf("title is too long: the limit is %d characters", MaxTitleChars)})
	case req.IdempotencyKey != "" && !keyRe.MatchString(req.IdempotencyKey):
		return fail("invalid", &Error{Kind: Invalid, Msg: "idempotency_key must be 1 to 128 characters from A-Z a-z 0-9 . _ : -"})
	}

	key := ""
	if req.IdempotencyKey != "" {
		key = d.Name + "\x00" + req.IdempotencyKey
		sum := sha256.Sum256([]byte(title + "\x00" + text))
		switch o, prev := s.idem.Begin(key, hex.EncodeToString(sum[:])); o {
		case idempotency.Hit:
			outcome = "duplicate"
			prev.Deduplicated = true
			return prev, nil
		case idempotency.InFlight:
			return fail("idempotency_in_progress", &Error{Kind: Busy, Msg: "a request with this idempotency_key is already in progress; wait for it to finish rather than sending again"})
		case idempotency.Mismatch:
			return fail("invalid", &Error{Kind: Invalid, Msg: "this idempotency_key was already used for a different message; use a new key for new content"})
		case idempotency.Full:
			return fail("busy", &Error{Kind: Busy, Msg: "too many requests are in progress; retry shortly"})
		}
	}
	committed := false
	defer func() {
		if key != "" && !committed {
			s.idem.Abort(key)
		}
	}()

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		return fail("busy", &Error{Kind: Busy, Msg: "the notify service is busy with other sends; retry in a few seconds"})
	}
	if ok, retry := d.limiter.Allow(); !ok {
		return fail("rate_limited", &Error{Kind: RateLimited, RetryAfter: retry, Msg: fmt.Sprintf("rate limit for destination %q reached (%d per minute, %d per hour); retry in %s", d.Name, d.Limits.RatePerMinute, d.Limits.RatePerHour, human(retry))})
	}

	err := d.Sender.Send(ctx, delivery.Message{Destination: d.Name, Header: s.header, Title: title, Text: text, SentAt: start})
	if err != nil {
		var de *delivery.Error
		if !errors.As(err, &de) {
			// Not one of ours, so its text is unvetted: say nothing specific.
			de = &delivery.Error{Kind: delivery.Upstream, Detail: "unexpected delivery failure"}
		}
		detail = de.Error()
		return s.mapDelivery(d, de, &outcome)
	}
	res := Result{Destination: d.Name, Platform: d.Platform, DeliveryID: newID(), SentAt: start}
	if key != "" {
		s.idem.Commit(key, res)
		committed = true
	}
	outcome = "sent"
	return res, nil
}

func (s *Service) mapDelivery(d *dest, de *delivery.Error, outcome *string) (Result, error) {
	base := fmt.Sprintf("could not deliver to destination %q (%s): %s", d.Name, d.Platform, de.Error())
	switch de.Kind {
	case delivery.RateLimited:
		*outcome = "upstream_rate_limited"
		when := "later"
		if de.RetryAfter > 0 {
			when = "in " + human(de.RetryAfter)
		}
		return Result{}, &Error{Kind: RateLimited, RetryAfter: de.RetryAfter, Msg: fmt.Sprintf("%s rate limited this destination; retry %s", d.Platform, when)}
	case delivery.Timeout:
		*outcome = "timeout"
		return Result{}, &Error{Kind: Timeout, Msg: base + ". The message may or may not have been delivered; retry later, with an idempotency_key to avoid a duplicate"}
	default:
		*outcome = "upstream_error"
		return Result{}, &Error{Kind: Upstream, Msg: base}
	}
}

func human(d time.Duration) string {
	return fmt.Sprintf("%ds", int(math.Ceil(d.Seconds())))
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ntf_" + hex.EncodeToString(b[:])
}
