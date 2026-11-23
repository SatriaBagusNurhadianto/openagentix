package notify

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/notify/internal/config"
	"github.com/openagentix/openagentix/services/notify/internal/delivery"
	"github.com/openagentix/openagentix/services/notify/internal/destinations"
)

type fakeSender struct {
	mu    sync.Mutex
	msgs  []delivery.Message
	err   error
	block chan struct{} // if set, Send waits for it
	ctxs  []context.Context
	parts int // if >1, reported through Parts
}

func (f *fakeSender) Parts(delivery.Message) int { return max(f.parts, 1) }

func (f *fakeSender) Send(ctx context.Context, m delivery.Message) error {
	f.mu.Lock()
	f.msgs = append(f.msgs, m)
	f.ctxs = append(f.ctxs, ctx)
	block, err := f.block, f.err
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return &delivery.Error{Kind: delivery.Timeout, Detail: "the destination did not answer in time"}
		}
	}
	return err
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	svc  *Service
	tg   *fakeSender
	sl   *fakeSender
	clk  *clock
	logs *bytes.Buffer
}

func newRig(t *testing.T, mut func(*Options)) *rig {
	t.Helper()
	r := &rig{tg: &fakeSender{}, sl: &fakeSender{}, clk: &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, logs: &bytes.Buffer{}}
	o := Options{
		Destinations: []destinations.Destination{
			{Name: "ops", Description: "On-call chat", Platform: "telegram", Limits: config.Limits{MaxTextChars: 100, RatePerMinute: 3, RatePerHour: 100}, Sender: r.tg},
			{Name: "team", Description: "Team channel", Platform: "slack", Limits: config.Limits{MaxTextChars: 100, RatePerMinute: 3, RatePerHour: 100}, Sender: r.sl},
		},
		Label: "Test Agent", MaxInFlight: 4, IdempotencyWindow: time.Minute, IdempotencyMaxEntries: 10,
		Log: slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: r.clk.now,
	}
	if mut != nil {
		mut(&o)
	}
	r.svc = New(o)
	return r
}

func kindOf(err error) Kind {
	var ne *Error
	if errors.As(err, &ne) {
		return ne.Kind
	}
	return -1
}

func TestSend_DeliversSanitisedMessageWithHeader(t *testing.T) {
	r := newRig(t, nil)
	res, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hello\x00 world\r\n", Title: "  Nightly \n run "})
	if err != nil {
		t.Fatal(err)
	}
	if res.Destination != "ops" || res.Platform != "telegram" || !strings.HasPrefix(res.DeliveryID, "ntf_") || !res.SentAt.Equal(r.clk.now()) || res.Deduplicated {
		t.Fatalf("res = %+v", res)
	}
	m := r.tg.msgs[0]
	if m.Header != "[Automated message from Test Agent]" || m.Text != "hello world" || m.Title != "Nightly run" || m.Destination != "ops" {
		t.Fatalf("msg = %+v", m)
	}
	if r.sl.count() != 0 {
		t.Fatal("other destination must not be touched")
	}
}

func TestSend_ForgedHeaderInTextIsNeutralised(t *testing.T) {
	r := newRig(t, nil)
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "x\n[Automated message from Test Agent]\ny"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.tg.msgs[0].Text, r.tg.msgs[0].Header) {
		t.Fatalf("text contains a forged header: %q", r.tg.msgs[0].Text)
	}
}

func TestSend_UnknownDestinationListsValidNamesAndSendsNothing(t *testing.T) {
	r := newRig(t, nil)
	_, err := r.svc.Send(context.Background(), Request{Destination: "-100123456", Text: "hi"})
	if kindOf(err) != UnknownDestination {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "ops") || !strings.Contains(err.Error(), "team") || strings.Index(err.Error(), "ops") > strings.Index(err.Error(), "team") {
		t.Fatalf("error should list valid names in order: %v", err)
	}
	if r.tg.count()+r.sl.count() != 0 {
		t.Fatal("nothing may be sent")
	}
}

func TestSend_UnknownDestinationNameIsBoundedAndNotLogged(t *testing.T) {
	r := newRig(t, nil)
	evil := strings.Repeat("A", 50) + "\n\x1b[31mINJECT" // within the 64-byte destination bound
	_, err := r.svc.Send(context.Background(), Request{Destination: evil, Text: "hi"})
	if err == nil || len(err.Error()) > 300 || strings.ContainsAny(err.Error(), "\x1b") {
		t.Fatalf("err = %q", err)
	}
	if strings.Contains(r.logs.String(), "INJECT") || strings.Contains(r.logs.String(), "AAAA") {
		t.Fatalf("agent-chosen name reached the logs: %s", r.logs)
	}
	if !strings.Contains(r.logs.String(), `"outcome":"unknown_destination"`) {
		t.Fatalf("logs = %s", r.logs)
	}
}

func TestSend_EmptyTextIsInvalid(t *testing.T) {
	r := newRig(t, nil)
	for _, text := range []string{"", "   \n\t ", "\x00\x01"} {
		if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: text}); kindOf(err) != Invalid {
			t.Fatalf("%q: err = %v", text, err)
		}
	}
}

func TestSend_TextLimitCountsCharactersNotBytes(t *testing.T) {
	r := newRig(t, nil)
	if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: strings.Repeat("日", 100)}); err != nil {
		t.Fatalf("exactly at the limit must pass: %v", err)
	}
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: strings.Repeat("日", 101)})
	if kindOf(err) != TooLong || !strings.Contains(err.Error(), "100") {
		t.Fatalf("err = %v", err)
	}
	if r.tg.count() != 1 {
		t.Fatal("over-long text must not be sent")
	}
}

func TestSend_LongTitleIsRejected(t *testing.T) {
	r := newRig(t, nil)
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "x", Title: strings.Repeat("t", MaxTitleChars+1)})
	if kindOf(err) != TooLong || !strings.Contains(err.Error(), "title") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_RateLimitIsPerDestinationAndSaysWhenToRetry(t *testing.T) {
	r := newRig(t, nil)
	for i := 0; i < 3; i++ {
		if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"})
	var ne *Error
	if !errors.As(err, &ne) || ne.Kind != RateLimited || ne.RetryAfter != 20*time.Second || !strings.Contains(err.Error(), "20s") {
		t.Fatalf("err = %v", err)
	}
	if r.tg.count() != 3 {
		t.Fatalf("refused call reached the sender (%d)", r.tg.count())
	}
	if _, err := r.svc.Send(context.Background(), Request{Destination: "team", Text: "hi"}); err != nil {
		t.Fatalf("other destination must have its own budget: %v", err)
	}
	r.clk.advance(21 * time.Second)
	if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"}); err != nil {
		t.Fatalf("budget should refill: %v", err)
	}
}

func TestSend_IdempotencyKeyReturnsEarlierResultWithoutSendingAgain(t *testing.T) {
	r := newRig(t, nil)
	req := Request{Destination: "ops", Text: "hi", IdempotencyKey: "run-42"}
	first, err := r.svc.Send(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	r.clk.advance(5 * time.Second)
	second, err := r.svc.Send(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if r.tg.count() != 1 || second.DeliveryID != first.DeliveryID || !second.SentAt.Equal(first.SentAt) || !second.Deduplicated {
		t.Fatalf("count=%d first=%+v second=%+v", r.tg.count(), first, second)
	}
}

func TestSend_IdempotentReplayDoesNotUseRateBudget(t *testing.T) {
	r := newRig(t, nil)
	for i := 0; i < 10; i++ {
		if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi", IdempotencyKey: "same"}); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
}

func TestSend_IdempotencyKeyIsScopedToDestination(t *testing.T) {
	r := newRig(t, nil)
	_, _ = r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi", IdempotencyKey: "k"})
	_, _ = r.svc.Send(context.Background(), Request{Destination: "team", Text: "hi", IdempotencyKey: "k"})
	if r.tg.count() != 1 || r.sl.count() != 1 {
		t.Fatalf("tg=%d sl=%d", r.tg.count(), r.sl.count())
	}
}

func TestSend_IdempotencyKeyReusedWithDifferentContentIsRefused(t *testing.T) {
	r := newRig(t, nil)
	_, _ = r.svc.Send(context.Background(), Request{Destination: "ops", Text: "one", IdempotencyKey: "k"})
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "two", IdempotencyKey: "k"})
	if kindOf(err) != Invalid || !strings.Contains(err.Error(), "idempotency_key") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_DefinitelyRefusedSendDoesNotPoisonTheKey(t *testing.T) {
	r := newRig(t, nil)
	r.tg.err = &delivery.Error{Kind: delivery.Upstream, Detail: "telegram refused the message (HTTP 400)", Definite: true}
	req := Request{Destination: "ops", Text: "hi", IdempotencyKey: "k"}
	if _, err := r.svc.Send(context.Background(), req); err == nil {
		t.Fatal("want failure")
	}
	r.tg.mu.Lock()
	r.tg.err = nil
	r.tg.mu.Unlock()
	res, err := r.svc.Send(context.Background(), req)
	if err != nil || res.Deduplicated || r.tg.count() != 2 {
		t.Fatalf("res=%+v err=%v count=%d", res, err, r.tg.count())
	}
}

func TestSend_RateLimitedCallReleasesItsKey(t *testing.T) {
	r := newRig(t, nil)
	for i := 0; i < 3; i++ {
		_, _ = r.svc.Send(context.Background(), Request{Destination: "ops", Text: "x"})
	}
	req := Request{Destination: "ops", Text: "hi", IdempotencyKey: "k"}
	if _, err := r.svc.Send(context.Background(), req); kindOf(err) != RateLimited {
		t.Fatalf("err = %v", err)
	}
	r.clk.advance(time.Minute)
	if res, err := r.svc.Send(context.Background(), req); err != nil || res.Deduplicated {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestSend_InvalidIdempotencyKeyIsRefused(t *testing.T) {
	r := newRig(t, nil)
	for _, k := range []string{"has space", "new\nline", strings.Repeat("k", 129), "ключ"} {
		if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi", IdempotencyKey: k}); kindOf(err) != Invalid {
			t.Fatalf("key %q: err = %v", k, err)
		}
	}
}

func TestSend_ConcurrentSameKeyIsRefusedWhileFirstIsInFlight(t *testing.T) {
	r := newRig(t, nil)
	r.tg.block = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi", IdempotencyKey: "k"})
		done <- err
	}()
	waitFor(t, func() bool { return r.tg.count() == 1 })
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi", IdempotencyKey: "k"})
	if kindOf(err) != Busy || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("err = %v", err)
	}
	close(r.tg.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSend_GlobalInFlightCap(t *testing.T) {
	r := newRig(t, func(o *Options) { o.MaxInFlight = 1 })
	r.tg.block = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"})
		done <- err
	}()
	waitFor(t, func() bool { return r.tg.count() == 1 })
	_, err := r.svc.Send(context.Background(), Request{Destination: "team", Text: "hi"})
	if kindOf(err) != Busy {
		t.Fatalf("err = %v", err)
	}
	if r.sl.count() != 0 {
		t.Fatal("refused call reached the sender")
	}
	close(r.tg.block)
	<-done
	if _, err := r.svc.Send(context.Background(), Request{Destination: "team", Text: "hi"}); err != nil {
		t.Fatalf("slot must be released: %v", err)
	}
}

func TestSend_UpstreamFailureIsClientSafe(t *testing.T) {
	r := newRig(t, nil)
	r.tg.err = &delivery.Error{Kind: delivery.Upstream, Detail: "telegram refused the message (HTTP 400)"}
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"})
	if kindOf(err) != Upstream || !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "ops") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_UnexpectedSenderErrorIsGenericAndNotLeaked(t *testing.T) {
	r := newRig(t, nil)
	r.tg.err = errors.New("dial tcp 10.1.2.3:443: token=SECRET123 chat=-100999")
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"})
	if kindOf(err) != Upstream || strings.Contains(err.Error(), "SECRET123") || strings.Contains(err.Error(), "10.1.2.3") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(r.logs.String(), "SECRET123") {
		t.Fatalf("unexpected error text reached the logs: %s", r.logs)
	}
}

func TestSend_UpstreamRateLimitSaysWhenToRetry(t *testing.T) {
	r := newRig(t, nil)
	r.sl.err = &delivery.Error{Kind: delivery.RateLimited, Detail: "slack rate limited the request", RetryAfter: 45 * time.Second}
	_, err := r.svc.Send(context.Background(), Request{Destination: "team", Text: "hi"})
	var ne *Error
	if !errors.As(err, &ne) || ne.Kind != RateLimited || ne.RetryAfter != 45*time.Second || !strings.Contains(err.Error(), "45s") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_UpstreamTimeoutSaysDeliveryIsUncertain(t *testing.T) {
	r := newRig(t, nil)
	r.tg.err = &delivery.Error{Kind: delivery.Timeout, Detail: "the destination did not answer in time"}
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"})
	if kindOf(err) != Timeout || !strings.Contains(err.Error(), "may") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_PartialDeliveryIsSurfaced(t *testing.T) {
	r := newRig(t, nil)
	r.tg.err = &delivery.Error{Kind: delivery.Upstream, Detail: "telegram refused the message (HTTP 500)", Sent: 1, Total: 2}
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "delivered 1 of 2 parts") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_PassesTheCallersContextToTheSender(t *testing.T) {
	r := newRig(t, nil)
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")
	_, _ = r.svc.Send(ctx, Request{Destination: "ops", Text: "hi"})
	if r.tg.ctxs[0].Value(key{}) != "v" {
		t.Fatal("sender did not get the caller's context")
	}
}

func TestSend_NoDestinationsRefusesEverything(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Destinations = nil })
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"})
	if kindOf(err) != Disabled || !strings.Contains(err.Error(), "no destinations") {
		t.Fatalf("err = %v", err)
	}
	if err := r.svc.Ready(); err == nil || !strings.Contains(err.Error(), "no destinations") {
		t.Fatalf("Ready = %v", err)
	}
	if len(r.svc.List()) != 0 {
		t.Fatal("list must be empty")
	}
}

func TestReady_WithDestinations(t *testing.T) {
	if err := newRig(t, nil).svc.Ready(); err != nil {
		t.Fatal(err)
	}
}

func TestList_ExposesOnlyNamePlatformDescriptionAndLimit(t *testing.T) {
	r := newRig(t, nil)
	got := r.svc.List()
	if len(got) != 2 || got[0] != (DestinationInfo{Name: "ops", Platform: "telegram", Description: "On-call chat", MaxTextChars: 100}) {
		t.Fatalf("list = %+v", got)
	}
}

func TestLogs_RecordOutcomeAndSizesButNeverContent(t *testing.T) {
	r := newRig(t, nil)
	_, _ = r.svc.Send(context.Background(), Request{Destination: "ops", Text: "TOP-SECRET-BODY", Title: "TOP-SECRET-TITLE", IdempotencyKey: "KEYVALUE"})
	r.tg.err = &delivery.Error{Kind: delivery.Upstream, Detail: "telegram refused the message (HTTP 400)"}
	_, _ = r.svc.Send(context.Background(), Request{Destination: "ops", Text: "TOP-SECRET-BODY-2"})
	_, _ = r.svc.Send(context.Background(), Request{Destination: "ops", Text: strings.Repeat("z", 500)})
	logs := r.logs.String()
	for _, leak := range []string{"TOP-SECRET", "KEYVALUE", "zzzz"} {
		if strings.Contains(logs, leak) {
			t.Fatalf("logs leak %q:\n%s", leak, logs)
		}
	}
	for _, want := range []string{`"destination":"ops"`, `"platform":"telegram"`, `"outcome":"sent"`, `"outcome":"upstream_error"`, `"outcome":"too_long"`, `"text_chars":15`, `"duration_ms"`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %s:\n%s", want, logs)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

// --- I1: uncertain and partial outcomes keep the idempotency key reserved ---

func retrySameKey(t *testing.T, r *rig, first *delivery.Error) (sendsBefore int, err error) {
	t.Helper()
	r.tg.err = first
	req := Request{Destination: "ops", Text: "hi", IdempotencyKey: "k"}
	if _, err := r.svc.Send(context.Background(), req); err == nil {
		t.Fatal("first attempt must fail")
	}
	sendsBefore = r.tg.count()
	r.tg.mu.Lock()
	r.tg.err = nil // the platform is healthy again
	r.tg.mu.Unlock()
	_, err = r.svc.Send(context.Background(), req)
	return sendsBefore, err
}

func TestSend_TimeoutWithKeyIsNotSentAgainOnRetry(t *testing.T) {
	r := newRig(t, nil)
	before, err := retrySameKey(t, r, &delivery.Error{Kind: delivery.Timeout, Detail: "the destination did not answer in time"})
	if r.tg.count() != before {
		t.Fatalf("retry sent again: %d sends", r.tg.count())
	}
	if kindOf(err) != Uncertain || !strings.Contains(err.Error(), "uncertain") || !strings.Contains(err.Error(), "not sent again") || !strings.Contains(err.Error(), "new idempotency_key") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_PartialDeliveryWithKeyIsNotResentOnRetry(t *testing.T) {
	r := newRig(t, nil)
	before, err := retrySameKey(t, r, &delivery.Error{Kind: delivery.Upstream, Detail: "telegram refused the message (HTTP 400)", Definite: true, Sent: 2, Total: 4})
	if r.tg.count() != before {
		t.Fatalf("retry re-sent parts: %d sends", r.tg.count())
	}
	if kindOf(err) != Uncertain || !strings.Contains(err.Error(), "partially delivered (2 of 4 parts)") || !strings.Contains(err.Error(), "not sent again") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_AmbiguousUpstreamFailureWithKeyIsHeld(t *testing.T) {
	// A 5xx or a reset after the request was written: nothing says it was not delivered.
	r := newRig(t, nil)
	before, err := retrySameKey(t, r, &delivery.Error{Kind: delivery.Upstream, Detail: "HTTP 502"})
	if r.tg.count() != before || kindOf(err) != Uncertain {
		t.Fatalf("sends=%d err=%v", r.tg.count(), err)
	}
}

func TestSend_UpstreamRateLimitBeforeAnythingSentFreesTheKey(t *testing.T) {
	r := newRig(t, nil)
	before, err := retrySameKey(t, r, &delivery.Error{Kind: delivery.RateLimited, Detail: "telegram rate limited the request", Definite: true})
	if err != nil || r.tg.count() != before+1 {
		t.Fatalf("retry must send: sends=%d err=%v", r.tg.count(), err)
	}
}

func TestSend_UncertainKeyExpiresWithTheWindow(t *testing.T) {
	r := newRig(t, nil)
	retrySameKey(t, r, &delivery.Error{Kind: delivery.Timeout, Detail: "x"})
	r.clk.advance(2 * time.Minute) // window is one minute
	res, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi", IdempotencyKey: "k"})
	if err != nil || res.Deduplicated {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestSend_UncertainKeyWithDifferentContentIsStillMismatch(t *testing.T) {
	r := newRig(t, nil)
	retrySameKey(t, r, &delivery.Error{Kind: delivery.Timeout, Detail: "x"})
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "other", IdempotencyKey: "k"})
	if kindOf(err) != Invalid {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_TimeoutTextDoesNotPromiseDeduplication(t *testing.T) {
	r := newRig(t, nil)
	r.tg.err = &delivery.Error{Kind: delivery.Timeout, Detail: "x"}
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"})
	for _, bad := range []string{"avoid a duplicate", "to avoid"} {
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("error promises dedup: %v", err)
		}
	}
	if !strings.Contains(err.Error(), "may or may not") || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error should warn about duplicates: %v", err)
	}
}

// --- I2: cheap rejections first, in-flight slot before expensive work ---

// allocated runs f and returns the bytes it allocated (all goroutines; tests
// in this package do not run in parallel).
func allocated(f func()) uint64 {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func TestSend_OversizedInputsAreRejectedBeforeAnyCleaning(t *testing.T) {
	r := newRig(t, nil)
	huge := strings.Repeat("a\x00", 2<<20) // 4 MiB; cleaning it would allocate tens of MiB
	cases := []struct {
		name string
		req  Request
		kind Kind
	}{
		{"destination", Request{Destination: huge, Text: "hi"}, Invalid},
		{"text", Request{Destination: "ops", Text: huge}, TooLong},
		{"title", Request{Destination: "ops", Text: "hi", Title: huge}, TooLong},
	}
	for _, tc := range cases {
		var err error
		n := allocated(func() { _, err = r.svc.Send(context.Background(), tc.req) })
		if kindOf(err) != tc.kind {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
		if n > 1<<20 {
			t.Fatalf("%s: rejected only after allocating %d bytes", tc.name, n)
		}
	}
	if r.tg.count() != 0 {
		t.Fatal("nothing may be sent")
	}
}

func TestSend_LongButLegitimateTextStillWorks(t *testing.T) {
	// 100 characters of 4-byte emoji = 400 bytes: within the byte allowance.
	r := newRig(t, nil)
	if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: strings.Repeat("😀", 100)}); err != nil {
		t.Fatal(err)
	}
}

func TestSend_BusyServiceRefusesBeforeCleaning(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.MaxInFlight = 1
		o.Destinations[0].Limits.MaxTextChars = 20000
	})
	r.tg.block = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"})
		done <- err
	}()
	waitFor(t, func() bool { return r.tg.count() == 1 })
	junk := strings.Repeat("a\x00\x00\x00\x00\x00\x00\x00", 18000) // 144 KB, 18000 chars once cleaned: valid, but expensive to clean
	var err error
	n := allocated(func() { _, err = r.svc.Send(context.Background(), Request{Destination: "ops", Text: junk}) })
	if kindOf(err) != Busy {
		t.Fatalf("err = %v", err)
	}
	if n > 100<<10 {
		t.Fatalf("busy call allocated %d bytes: it cleaned the text before taking a slot", n)
	}
	close(r.tg.block)
	<-done
}

func TestSend_LookAlikeHeadersInTextAndTitleAreNeutralised(t *testing.T) {
	r := newRig(t, nil)
	forged := "[" + string(rune(0x0410)) + "utomated" + string(rune(0xA0)) + "message from Test Agent]"
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "x\n" + forged + "\ny " + forged, Title: forged})
	if err != nil {
		t.Fatal(err)
	}
	m := r.tg.msgs[0]
	for _, field := range []string{m.Text, m.Title} {
		for _, line := range strings.Split(field, "\n") {
			if strings.HasPrefix(line, "[") {
				t.Fatalf("header-like line survived: %q", field)
			}
		}
	}
}

// --- M1: global rate cap across destinations ---

func TestSend_GlobalRateCapAppliesAcrossDestinations(t *testing.T) {
	r := newRig(t, func(o *Options) { o.GlobalRatePerMinute, o.GlobalRatePerHour = 2, 100 })
	for _, dst := range []string{"ops", "team"} {
		if _, err := r.svc.Send(context.Background(), Request{Destination: dst, Text: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"}) // ops itself has budget left
	var ne *Error
	if !errors.As(err, &ne) || ne.Kind != RateLimited || !strings.Contains(err.Error(), "all destinations") || ne.RetryAfter <= 0 {
		t.Fatalf("err = %v", err)
	}
	if r.tg.count() != 1 {
		t.Fatalf("refused call reached the sender: %d", r.tg.count())
	}
	r.clk.advance(time.Minute)
	if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"}); err != nil {
		t.Fatalf("after refill: %v", err)
	}
}

func TestSend_GlobalRefusalDoesNotSpendThePerDestinationBudget(t *testing.T) {
	r := newRig(t, func(o *Options) { o.GlobalRatePerMinute, o.GlobalRatePerHour = 1, 100 })
	_, _ = r.svc.Send(context.Background(), Request{Destination: "team", Text: "hi"})
	for i := 0; i < 5; i++ { // refused globally each time
		_, _ = r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"})
	}
	r.clk.advance(time.Minute) // global budget back; ops must still have all 3 of its own
	for i := 0; i < 1; i++ {
		if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if lim := r.svc.dests["ops"].limiter; func() bool { ok, _ := lim.AllowN(2); return !ok }() {
		t.Fatal("per-destination tokens were spent by globally refused calls")
	}
}

func TestSend_MultiPartMessageConsumesOneGlobalTokenPerPart(t *testing.T) {
	r := newRig(t, func(o *Options) { o.GlobalRatePerMinute, o.GlobalRatePerHour = 5, 100 })
	r.tg.parts = 3
	if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"}); kindOf(err) != RateLimited {
		t.Fatalf("2 tokens left, 3 needed: err = %v", err)
	}
	r.tg.parts = 1
	if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"}); err != nil {
		t.Fatalf("a single part must still fit: %v", err)
	}
}

func TestSend_NoGlobalCapConfiguredMeansNoGlobalLimit(t *testing.T) {
	r := newRig(t, func(o *Options) {
		for i := range o.Destinations {
			o.Destinations[i].Limits.RatePerMinute = 1000
		}
	})
	for i := 0; i < 50; i++ {
		if _, err := r.svc.Send(context.Background(), Request{Destination: "ops", Text: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
}
