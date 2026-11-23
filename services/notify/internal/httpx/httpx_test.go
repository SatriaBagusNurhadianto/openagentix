package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
