package bridge

import (
	"net/http"
	"time"
)

func (b *Bridge) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Bridge) markPoll() { b.lastPoll.Store(b.now().UnixNano()) }

// Healthy reports whether Run is polling: it started, and the last successful
// getUpdates is no older than 3x the poll timeout (at least 3s).
func (b *Bridge) Healthy() bool {
	last := b.lastPoll.Load()
	if last == 0 {
		return false
	}
	window := 3 * time.Duration(max(b.PollTimeout, 1)) * time.Second
	return b.now().Sub(time.Unix(0, last)) <= window
}

// HealthHandler serves GET /healthz: 200 while polling is healthy, else 503.
// It exposes no other information.
func (b *Bridge) HealthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if b.Healthy() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		http.Error(w, "polling stalled", http.StatusServiceUnavailable)
	})
	return mux
}
