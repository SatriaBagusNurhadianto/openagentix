package notify

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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
}

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
	evil := strings.Repeat("A", 500) + "\n\x1b[31mINJECT"
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

func TestSend_FailedSendDoesNotPoisonTheKey(t *testing.T) {
	r := newRig(t, nil)
	r.tg.err = &delivery.Error{Kind: delivery.Upstream, Detail: "HTTP 502"}
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
