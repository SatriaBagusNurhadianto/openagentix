package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/notify/internal/delivery"
)

func TestDo_ReturnsStatusHeaderAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(201)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL, nil)
	resp, err := Do(context.Background(), NewClient(time.Second), req, 1024)
	if err != nil || resp.Status != 201 || resp.Header.Get("X-Test") != "yes" || string(resp.Body) != "hello" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestDo_DoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("secret-body"))
	resp, err := Do(context.Background(), NewClient(time.Second), req, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 302 || hits.Load() != 0 {
		t.Fatalf("status=%d, redirect target hit %d times", resp.Status, hits.Load())
	}
}

func TestDo_OversizedBodyIsAnErrorNotTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 5000)))
	}))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := Do(context.Background(), NewClient(time.Second), req, 1000)
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.Upstream || !strings.Contains(de.Detail, "too large") {
		t.Fatalf("err = %v", err)
	}
}

func TestDo_SlowServerTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	req, _ := http.NewRequest("GET", srv.URL, nil)
	start := time.Now()
	_, err := Do(context.Background(), NewClient(50*time.Millisecond), req, 1000)
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.Timeout || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestDo_ContextDeadlineIsATimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := Do(ctx, NewClient(10*time.Second), req, 1000)
	var de *delivery.Error
	if !errors.As(err, &de) || de.Kind != delivery.Timeout {
		t.Fatalf("err = %v", err)
	}
}

func TestDo_TransportErrorHidesURLAndHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL + "/bot123:SECRET/sendMessage"
	srv.Close()
	req, _ := http.NewRequest("GET", url, nil)
	_, err := Do(context.Background(), NewClient(time.Second), req, 1000)
	if err == nil {
		t.Fatal("want error")
	}
	for _, leak := range []string{"SECRET", "127.0.0.1", "bot123", "sendMessage"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaks %q: %v", leak, err)
		}
	}
}

func TestRetryAfter_ParsesSecondsOnly(t *testing.T) {
	for in, want := range map[string]time.Duration{"3": 3 * time.Second, "0": 0, "": 0, "-4": 0, "soon": 0, "1.5": 0} {
		if got := RetryAfter(in); got != want {
			t.Errorf("RetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDo_ConnectionRefusedIsDefiniteButOtherFailuresAreNot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens: the dial is refused before anything is written
	req, _ := http.NewRequest("POST", url, strings.NewReader("x"))
	_, err := Do(context.Background(), NewClient(time.Second), req, 100)
	var de *delivery.Error
	if !errors.As(err, &de) || !de.Definite {
		t.Fatalf("refused dial: err = %v", err)
	}

	// A connection that is reset after the request was written is not provably undelivered.
	rst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _, _ := w.(http.Hijacker).Hijack()
		c.Close()
	}))
	defer rst.Close()
	req, _ = http.NewRequest("POST", rst.URL, strings.NewReader("x"))
	_, err = Do(context.Background(), NewClient(time.Second), req, 100)
	if !errors.As(err, &de) || de.Definite {
		t.Fatalf("reset after write: err = %v", err)
	}
}

func TestNewClient_IgnoresProxyEnvironmentVariables(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxied.Add(1) }))
	defer proxy.Close()
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY"} {
		t.Setenv(k, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	c := NewClient(time.Second)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T: the client falls back to http.DefaultTransport, which honours HTTP(S)_PROXY", c.Transport)
	}
	if tr.Proxy != nil {
		// A public host is never exempt from the environment proxy (loopback is).
		u, _ := tr.Proxy(&http.Request{URL: mustURL(t, "https://api.telegram.org/")})
		t.Fatalf("transport would use proxy %v", u)
	}
	if tr.DialContext == nil || tr.TLSHandshakeTimeout <= 0 || tr.ResponseHeaderTimeout <= 0 || tr.IdleConnTimeout <= 0 || tr.MaxIdleConns <= 0 || tr.MaxConnsPerHost <= 0 {
		t.Fatalf("transport lacks limits/timeouts: %+v", tr)
	}
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS verification must stay on")
	}
	// Behaviour: a direct request still works and nothing reaches the proxy.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer target.Close()
	req, _ := http.NewRequest("GET", target.URL, nil)
	if _, err := Do(context.Background(), c, req, 100); err != nil || proxied.Load() != 0 {
		t.Fatalf("err=%v proxied=%d", err, proxied.Load())
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRetryAfter_ClampsHugeAndRejectsNegativeValues(t *testing.T) {
	for _, v := range []string{"9223372036854775807", "9999999999", "99999999999999", "-5", "-9223372036854775808"} {
		d := RetryAfter(v)
		if d < 0 || d > MaxRetryAfterWait {
			t.Errorf("RetryAfter(%q) = %v, want within [0, %v]", v, d, MaxRetryAfterWait)
		}
	}
	if RetryAfter("30") != 30*time.Second || RetryAfter("0") != 0 {
		t.Fatal("ordinary values must be unchanged")
	}
	if got := Seconds(1 << 60); got != MaxRetryAfterWait {
		t.Fatalf("Seconds(huge) = %v", got)
	}
	if got := Seconds(-3); got != 0 {
		t.Fatalf("Seconds(-3) = %v", got)
	}
}
