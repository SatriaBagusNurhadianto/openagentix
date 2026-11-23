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

// Input bounds that are checked on raw bytes, before any cleaning, so a huge
// junk argument costs nothing. A character is at most 4 bytes in UTF-8;
// the allowance is twice that so text with some invisible characters that
// cleaning will drop is still accepted.
const (
	MaxDestinationBytes = 64
	bytesPerCharAllowed = 8
	maxKeyBytes         = 128
)

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
	// Uncertain: an earlier attempt with this idempotency_key may or did
	// (partly) reach the platform, so it is not sent again.
	Uncertain
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
	// GlobalRatePerMinute / GlobalRatePerHour cap sends across all destinations
	// (one token per delivered part). Zero disables that window.
	GlobalRatePerMinute int
	GlobalRatePerHour   int
	Log                 *slog.Logger
	Now                 func() time.Time // tests
}

// record is what the idempotency cache remembers: the receipt of a delivered
// message, or (when uncertain) how far an unfinished delivery got.
type record struct {
	Result
	Sent, Total int // parts delivered / parts in the message; Total 0 if unknown
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
	idem   *idempotency.Cache[record]
	global *ratelimit.Limiter // nil when no global cap is set
	log    *slog.Logger
	now    func() time.Time

	globalMin, globalHour int
}

// partCounter is implemented by senders that may post one message as several
// requests; each part then costs one global rate token.
type partCounter interface {
	Parts(delivery.Message) int
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
		idem:   idempotency.New[record](o.IdempotencyWindow, max(o.IdempotencyMaxEntries, 1), o.Now),
		log:    o.Log,
		now:    o.Now,
	}
	if o.GlobalRatePerMinute > 0 || o.GlobalRatePerHour > 0 {
		s.global = ratelimit.New(o.GlobalRatePerMinute, o.GlobalRatePerHour, o.Now)
		s.globalMin, s.globalHour = o.GlobalRatePerMinute, o.GlobalRatePerHour
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
	if len(req.Destination) > MaxDestinationBytes {
		return fail("invalid", &Error{Kind: Invalid, Msg: fmt.Sprintf("destination is too long: the limit is %d bytes; use a name from notify_list_destinations", MaxDestinationBytes)})
	}
	d = s.dests[req.Destination]
	if d == nil {
		return fail("unknown_destination", &Error{Kind: UnknownDestination, Msg: fmt.Sprintf("unknown destination %q: valid destinations are: %s", clip(sanitize.Line(req.Destination), 40), strings.Join(s.names, ", "))})
	}
	// Cheap size checks on the raw input, then the in-flight slot, and only
	// then anything that scales with the input (cleaning, hashing).
	switch {
	case len(req.Text) > bytesPerCharAllowed*d.Limits.MaxTextChars:
		return fail("too_long", &Error{Kind: TooLong, Msg: fmt.Sprintf("text is too long; the limit for destination %q is %d characters. Shorten it and try again", d.Name, d.Limits.MaxTextChars)})
	case len(req.Title) > bytesPerCharAllowed*MaxTitleChars:
		return fail("too_long", &Error{Kind: TooLong, Msg: fmt.Sprintf("title is too long: the limit is %d characters", MaxTitleChars)})
	case len(req.IdempotencyKey) > maxKeyBytes:
		return fail("invalid", &Error{Kind: Invalid, Msg: "idempotency_key must be 1 to 128 characters from A-Z a-z 0-9 . _ : -"})
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		return fail("busy", &Error{Kind: Busy, Msg: "the notify service is busy with other sends; retry in a few seconds"})
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
			return prev.Result, nil
		case idempotency.Uncertain:
			return fail("idempotency_uncertain", &Error{Kind: Uncertain, Msg: uncertainRetryMsg(prev)})
		case idempotency.InFlight:
			return fail("idempotency_in_progress", &Error{Kind: Busy, Msg: "a request with this idempotency_key is already in progress; wait for it to finish rather than sending again"})
		case idempotency.Mismatch:
			return fail("invalid", &Error{Kind: Invalid, Msg: "this idempotency_key was already used for a different message; use a new key for new content"})
		case idempotency.Full:
			return fail("busy", &Error{Kind: Busy, Msg: "too many requests are in progress; retry shortly"})
		}
	}
	settled := false
	defer func() {
		if key != "" && !settled {
			s.idem.Abort(key)
		}
	}()

	if ok, retry := d.limiter.Allow(); !ok {
		return fail("rate_limited", &Error{Kind: RateLimited, RetryAfter: retry, Msg: fmt.Sprintf("rate limit for destination %q reached (%d per minute, %d per hour); retry in %s", d.Name, d.Limits.RatePerMinute, d.Limits.RatePerHour, human(retry))})
	}

	msg := delivery.Message{Destination: d.Name, Header: s.header, Title: title, Text: text, SentAt: start}
	if s.global != nil {
		parts := 1
		if pc, ok := d.Sender.(partCounter); ok {
			parts = max(pc.Parts(msg), 1)
		}
		if ok, retry := s.global.AllowN(parts); !ok {
			d.limiter.Refund(1) // refused as a whole: the destination's own budget is not spent
			return fail("rate_limited_global", &Error{Kind: RateLimited, RetryAfter: retry, Msg: fmt.Sprintf("global rate limit across all destinations reached (%d per minute, %d per hour; a message sent as several parts counts once per part); retry in %s", s.globalMin, s.globalHour, human(retry))})
		}
	}
	err := d.Sender.Send(ctx, msg)
	if err != nil {
		var de *delivery.Error
		if !errors.As(err, &de) {
			// Not one of ours, so its text is unvetted: say nothing specific.
			de = &delivery.Error{Kind: delivery.Upstream, Detail: "unexpected delivery failure"}
		}
		detail = de.Error()
		if key != "" && (de.Sent > 0 || !de.Definite) {
			// The platform may have the message (or some parts of it): keep the
			// key so a retry cannot send it again.
			s.idem.MarkUncertain(key, record{Sent: de.Sent, Total: de.Total})
			settled = true
		}
		return s.mapDelivery(d, de, key != "", &outcome)
	}
	res := Result{Destination: d.Name, Platform: d.Platform, DeliveryID: newID(), SentAt: start}
	if key != "" {
		s.idem.Commit(key, record{Result: res})
		settled = true
	}
	outcome = "sent"
	return res, nil
}

func (s *Service) mapDelivery(d *dest, de *delivery.Error, hasKey bool, outcome *string) (Result, error) {
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
		msg := base + ". The message may or may not have been delivered."
		if hasKey {
			msg += " Retrying with the same idempotency_key will not send it again; use a new key only if a possible duplicate is acceptable"
		} else {
			msg += " Retrying may send a duplicate; no idempotency_key was given, so nothing prevents that"
		}
		return Result{}, &Error{Kind: Timeout, Msg: msg}
	default:
		*outcome = "upstream_error"
		return Result{}, &Error{Kind: Upstream, Msg: base}
	}
}

// uncertainRetryMsg is what a retry with a held key is told.
func uncertainRetryMsg(r record) string {
	what := "delivery of the earlier attempt is uncertain"
	if r.Sent > 0 && r.Total > 0 {
		what = fmt.Sprintf("the earlier attempt was partially delivered (%d of %d parts)", r.Sent, r.Total)
	}
	return what + "; not sent again; use a new idempotency_key only if a possible duplicate is acceptable"
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
