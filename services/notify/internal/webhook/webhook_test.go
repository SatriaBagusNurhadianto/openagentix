package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/notify/internal/delivery"
	"github.com/openagentix/openagentix/services/notify/internal/httpx"
)

const secret = "fake-signing-secret"

type got struct {
	header http.Header
	body   []byte
}

func capture(t *testing.T, status int, resp string) (*httptest.Server, func() []got) {
	t.Helper()
	var mu sync.Mutex
	var all []got
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		all = append(all, got{r.Header.Clone(), b})
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []got { mu.Lock(); defer mu.Unlock(); return append([]got(nil), all...) }
}

func client(url, sec string) *Client {
	return New(Config{URL: url, Secret: sec, HTTP: httpx.NewClient(2 * time.Second)})
}

var sentAt = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func msg() delivery.Message {
	return delivery.Message{Destination: "audit", Header: "[Automated message from Test]", Title: "T", Text: "body", SentAt: sentAt}
}

func TestSend_PostsJSONBody(t *testing.T) {
	srv, calls := capture(t, 200, "ok")
	if err := client(srv.URL, "").Send(context.Background(), msg()); err != nil {
		t.Fatal(err)
	}
	c := calls()
	if len(c) != 1 || c[0].header.Get("Content-Type") != "application/json" {
		t.Fatalf("calls = %+v", c)
	}
	var b map[string]any
	if err := json.Unmarshal(c[0].body, &b); err != nil {
		t.Fatal(err)
	}
	if b["title"] != "T" || b["destination"] != "audit" || b["sent_at"] != "2026-03-04T05:06:07Z" ||
		b["text"] != "[Automated message from Test]\n\nbody" || len(b) != 4 {
		t.Fatalf("body = %v", b)
	}
}

func TestSend_SignsBodyWhenSecretConfigured(t *testing.T) {
	srv, calls := capture(t, 204, "")
	if err := client(srv.URL, secret).Send(context.Background(), msg()); err != nil {
		t.Fatal(err)
	}
	c := calls()[0]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(c.body)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); c.header.Get(SignatureHeader) != want {
		t.Fatalf("signature = %q, want %q", c.header.Get(SignatureHeader), want)
	}
}

func TestSend_NoSignatureHeaderWithoutSecret(t *testing.T) {
	srv, calls := capture(t, 200, "")
	_ = client(srv.URL, "").Send(context.Background(), msg())
	if v := calls()[0].header.Get(SignatureHeader); v != "" {
		t.Fatalf("unexpected signature %q", v)
	}
}

func TestSend_RedirectIsNotFollowed(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	err := client(redir.URL, secret).Send(context.Background(), msg())
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.Upstream || hits.Load() != 0 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

func TestSend_Non2xxIsUpstreamAndDoesNotEchoBody(t *testing.T) {
	srv, _ := capture(t, 500, "internal secret detail "+secret)
	err := client(srv.URL, secret).Send(context.Background(), msg())
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.Upstream || !strings.Contains(de.Detail, "500") {
		t.Fatalf("err = %v", err)
	}
	for _, leak := range []string{"internal secret", secret, srv.URL, "127.0.0.1"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("leaks %q: %v", leak, err)
		}
	}
}

func TestSend_429IsRateLimitedWithRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	err := client(srv.URL, "").Send(context.Background(), msg())
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.RateLimited || de.RetryAfter != 12*time.Second {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_OversizedResponseIsAnError(t *testing.T) {
	srv, _ := capture(t, 200, strings.Repeat("a", MaxResponseBytes+10))
	err := client(srv.URL, "").Send(context.Background(), msg())
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_TimeoutIsReportedAsTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c := New(Config{URL: srv.URL, HTTP: httpx.NewClient(50 * time.Millisecond)})
	err := c.Send(context.Background(), msg())
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.Timeout {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_TransportErrorDoesNotLeakURL(t *testing.T) {
	srv, _ := capture(t, 200, "")
	url := srv.URL + "/hooks/TOKEN-IN-URL"
	srv.Close()
	err := client(url, "").Send(context.Background(), msg())
	if err == nil || strings.Contains(err.Error(), "TOKEN-IN-URL") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateURL(t *testing.T) {
	cases := []struct {
		url   string
		allow bool
		ok    bool
	}{
		{"https://hooks.example.com/x", false, true},
		{"http://notify.svc.cluster.local/x", false, false},
		{"http://notify.svc.cluster.local/x", true, true},
		{"ftp://example.com/x", true, false},
		{"https://user:pass@example.com/x", false, false},
		{"https:///nohost", false, false},
		{"not a url", false, false},
		{"https://example.com/x#frag", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		err := ValidateURL(c.url, c.allow)
		if (err == nil) != c.ok {
			t.Errorf("ValidateURL(%q, %v) err=%v, want ok=%v", c.url, c.allow, err, c.ok)
		}
		if err != nil && c.url != "" && strings.Contains(err.Error(), c.url) {
			t.Errorf("validation error echoes the URL: %v", err)
		}
	}
}
