package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/openagentix/openagentix/services/notify/internal/delivery"
	"github.com/openagentix/openagentix/services/notify/internal/httpx"
)

const (
	token  = "123456:FAKE-TOKEN-abcdef"
	chatID = "-1009876543210"
)

type call struct {
	path string
	body map[string]any
}

type fake struct {
	mu    sync.Mutex
	calls []call
	reply func(n int) (int, string)
	srv   *httptest.Server
}

func newFake(t *testing.T, reply func(n int) (int, string)) *fake {
	t.Helper()
	f := &fake{reply: reply}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.calls = append(f.calls, call{r.URL.Path, body})
		n := len(f.calls)
		f.mu.Unlock()
		code, resp := f.reply(n)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) snapshot() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func ok(int) (int, string) { return 200, `{"ok":true,"result":{"message_id":5}}` }

func client(f *fake) (*Client, *[]time.Duration) {
	var slept []time.Duration
	c := New(Config{BaseURL: f.srv.URL, Token: token, ChatID: chatID, HTTP: httpx.NewClient(2 * time.Second), MaxRetryAfter: 10 * time.Second})
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	return c, &slept
}

func msg(text string) delivery.Message {
	return delivery.Message{Destination: "ops", Header: "[Automated message from Test]", Text: text, SentAt: time.Now()}
}

func texts(f *fake) []string {
	var out []string
	for _, c := range f.snapshot() {
		out = append(out, c.body["text"].(string))
	}
	return out
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func TestSend_PostsPlainTextToConfiguredChat(t *testing.T) {
	f := newFake(t, ok)
	c, _ := client(f)
	m := msg("the report is ready")
	m.Title = "Nightly"
	if err := c.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	calls := f.snapshot()
	if len(calls) != 1 || calls[0].path != "/bot"+token+"/sendMessage" {
		t.Fatalf("calls = %+v", calls)
	}
	b := calls[0].body
	if b["chat_id"] != chatID {
		t.Fatalf("chat_id = %v", b["chat_id"])
	}
	if _, has := b["parse_mode"]; has {
		t.Fatal("parse_mode must not be set: text is plain")
	}
	if got := b["text"]; got != "[Automated message from Test]\nNightly\n\nthe report is ready" {
		t.Fatalf("text = %q", got)
	}
}

func TestSend_WithoutTitleHasNoBlankTitleLine(t *testing.T) {
	f := newFake(t, ok)
	c, _ := client(f)
	_ = c.Send(context.Background(), msg("body"))
	if got := texts(f)[0]; got != "[Automated message from Test]\n\nbody" {
		t.Fatalf("text = %q", got)
	}
}

func TestSend_SplitsLongTextAndEveryPartFits(t *testing.T) {
	f := newFake(t, ok)
	c, _ := client(f)
	if err := c.Send(context.Background(), msg(strings.Repeat("a", 5000)+" "+strings.Repeat("b", 5000))); err != nil {
		t.Fatal(err)
	}
	got := texts(f)
	if len(got) < 3 {
		t.Fatalf("parts = %d", len(got))
	}
	for _, g := range got {
		if utf16Len(g) > MaxMessageUnits {
			t.Fatalf("part of %d units", utf16Len(g))
		}
	}
}

func TestSplit_CountsUTF16UnitsAndNeverSplitsEmoji(t *testing.T) {
	text := strings.Repeat("😀", 3000) // 6000 UTF-16 units, 3000 runes
	got := Split(text, MaxMessageUnits)
	if len(got) != 2 {
		t.Fatalf("parts = %d, want 2", len(got))
	}
	for _, g := range got {
		if utf16Len(g) > MaxMessageUnits || !utf8.ValidString(g) || strings.ContainsRune(g, utf8.RuneError) {
			t.Fatalf("bad part: %d units", utf16Len(g))
		}
	}
	if strings.Join(got, "") != text {
		t.Fatal("parts do not reassemble")
	}
}

func TestSplit_LongCJKText(t *testing.T) {
	text := strings.Repeat("日本語é", 2500) // 10000 BMP runes
	got := Split(text, MaxMessageUnits)
	for _, g := range got {
		if utf16Len(g) > MaxMessageUnits {
			t.Fatalf("part of %d units", utf16Len(g))
		}
	}
	if strings.Join(got, "") != text || len(got) != 3 {
		t.Fatalf("parts=%d reassemble=%v", len(got), strings.Join(got, "") == text)
	}
}

func TestSplit_PrefersNewlineThenSpace(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n"
	got := Split(strings.Repeat(line, 50), MaxMessageUnits) // 5000 units
	if len(got) != 2 || !strings.HasSuffix(got[0], strings.Repeat("x", 99)) {
		t.Fatalf("first part must end at a line boundary: %d parts", len(got))
	}
	for _, g := range Split(strings.Repeat("word ", 1000), MaxMessageUnits) {
		if strings.HasPrefix(g, "ord") || strings.HasSuffix(g, "wor") || strings.HasSuffix(g, "wo") {
			t.Fatalf("split inside a word: %q", g[len(g)-6:])
		}
	}
}

func TestSplit_ShortFirstLineDoesNotLeaveANearlyEmptyPart(t *testing.T) {
	got := Split("header\n\n"+strings.Repeat("a", 5000), MaxMessageUnits)
	if len(got) != 2 || utf16Len(got[0]) < MaxMessageUnits-10 {
		t.Fatalf("parts = %d, first has %d units", len(got), utf16Len(got[0]))
	}
}

func TestSplit_SkipsWhitespaceOnlyParts(t *testing.T) {
	if got := Split(strings.Repeat("a", 4096)+"  \n \n ", MaxMessageUnits); len(got) != 1 {
		t.Fatalf("parts = %q", got)
	}
}

func TestSend_Honours429RetryAfterOnce(t *testing.T) {
	f := newFake(t, func(n int) (int, string) {
		if n == 1 {
			return 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 2","parameters":{"retry_after":2}}`
		}
		return ok(n)
	})
	c, slept := client(f)
	if err := c.Send(context.Background(), msg("hi")); err != nil {
		t.Fatal(err)
	}
	if len(f.snapshot()) != 2 || len(*slept) != 1 || (*slept)[0] != 2*time.Second {
		t.Fatalf("calls=%d slept=%v", len(f.snapshot()), *slept)
	}
}

func TestSend_SecondRateLimitIsReportedWithRetryAfter(t *testing.T) {
	f := newFake(t, func(int) (int, string) {
		return 429, `{"ok":false,"error_code":429,"description":"flood","parameters":{"retry_after":3}}`
	})
	c, _ := client(f)
	err := c.Send(context.Background(), msg("hi"))
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.RateLimited || de.RetryAfter != 3*time.Second || len(f.snapshot()) != 2 {
		t.Fatalf("err=%v calls=%d", err, len(f.snapshot()))
	}
}

func TestSend_DoesNotWaitLongerThanCap(t *testing.T) {
	f := newFake(t, func(int) (int, string) {
		return 429, `{"ok":false,"error_code":429,"description":"flood","parameters":{"retry_after":3600}}`
	})
	c, slept := client(f)
	err := c.Send(context.Background(), msg("hi"))
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.RateLimited || de.RetryAfter != time.Hour || len(f.snapshot()) != 1 || len(*slept) != 0 {
		t.Fatalf("err=%v calls=%d slept=%v", err, len(f.snapshot()), *slept)
	}
}

func TestSend_ServerErrorIsUpstreamWithoutDescription(t *testing.T) {
	f := newFake(t, func(int) (int, string) { return 502, `<html>bad gateway ` + chatID + ` ` + token + `</html>` })
	c, _ := client(f)
	err := c.Send(context.Background(), msg("hi"))
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.Upstream || !strings.Contains(de.Detail, "502") {
		t.Fatalf("err = %v", err)
	}
	assertNoSecrets(t, err.Error())
}

func TestSend_APIErrorDescriptionIsNotRelayed(t *testing.T) {
	f := newFake(t, func(int) (int, string) {
		return 400, `{"ok":false,"error_code":400,"description":"Bad Request: chat ` + chatID + ` not found"}`
	})
	c, _ := client(f)
	err := c.Send(context.Background(), msg("hi"))
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.Upstream || !strings.Contains(de.Detail, "400") {
		t.Fatalf("err = %v", err)
	}
	assertNoSecrets(t, err.Error())
}

func TestSend_RedirectIsNotFollowed(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	f := &fake{srv: redir}
	c, _ := client(f)
	err := c.Send(context.Background(), msg("hi"))
	if err == nil || hits.Load() != 0 {
		t.Fatalf("err=%v redirect hits=%d", err, hits.Load())
	}
	assertNoSecrets(t, err.Error())
}

func TestSend_TimeoutIsReportedAsTimeout(t *testing.T) {
	release := make(chan struct{})
	f := newFake(t, func(int) (int, string) { <-release; return ok(0) })
	t.Cleanup(func() { close(release) })
	c, _ := client(f)
	c.http = httpx.NewClient(50 * time.Millisecond)
	start := time.Now()
	err := c.Send(context.Background(), msg("hi"))
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.Timeout || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestSend_OversizedResponseIsAnError(t *testing.T) {
	f := newFake(t, func(int) (int, string) { return 200, `{"ok":true,"result":"` + strings.Repeat("a", 2<<20) + `"}` })
	c, _ := client(f)
	err := c.Send(context.Background(), msg("hi"))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_TransportErrorDoesNotLeakTokenOrChat(t *testing.T) {
	f := newFake(t, ok)
	c, _ := client(f)
	f.srv.Close()
	err := c.Send(context.Background(), msg("hi"))
	if err == nil {
		t.Fatal("want error")
	}
	assertNoSecrets(t, err.Error())
}

func TestSend_PartialDeliveryIsReported(t *testing.T) {
	f := newFake(t, func(n int) (int, string) {
		if n == 2 {
			return 500, "boom"
		}
		return ok(n)
	})
	c, _ := client(f)
	err := c.Send(context.Background(), msg(strings.Repeat("a", 9000)))
	var de *delivery.Error
	if !errors.As(err, &de) || de.Sent != 1 || de.Total != 3 || !strings.Contains(err.Error(), "delivered 1 of 3 parts") {
		t.Fatalf("err = %v", err)
	}
}

func assertNoSecrets(t *testing.T, s string) {
	t.Helper()
	for _, leak := range []string{token, "FAKE-TOKEN", chatID, "127.0.0.1"} {
		if strings.Contains(s, leak) {
			t.Fatalf("leaks %q: %s", leak, s)
		}
	}
}
