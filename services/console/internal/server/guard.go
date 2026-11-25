package server

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- Host allowlist ---

// hostList is the set of Host values the console answers to. A page that rebinds its own name to 127.0.0.1 reaches the
// port-forward with the attacker's name in Host (and in Origin), so the same-origin comparison of Origin with Host proves
// nothing; only a list of names the operator chose does.
type hostList struct {
	names map[string]bool // host name, any port
	pairs map[string]bool // host:port, that port only
}

func newHostList(extra []string) *hostList {
	h := &hostList{names: map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}, pairs: map[string]bool{}}
	for _, e := range extra {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if name, port, ok := splitHost(e); ok && port != "" {
			h.pairs[net.JoinHostPort(name, port)] = true
		} else if ok {
			h.names[name] = true
		}
	}
	return h
}

// splitHost splits "name", "name:port", "[v6]" and "[v6]:port"; name is lower case, without brackets or a trailing dot.
// ok is false for anything that is not one of those shapes.
func splitHost(hp string) (name, port string, ok bool) {
	if hp == "" || strings.ContainsAny(hp, "/?#@ \t\\") {
		return "", "", false
	}
	if strings.HasPrefix(hp, "[") {
		end := strings.Index(hp, "]")
		if end < 0 {
			return "", "", false
		}
		name, rest := hp[1:end], hp[end+1:]
		if net.ParseIP(name) == nil {
			return "", "", false
		}
		if rest == "" {
			return strings.ToLower(name), "", true
		}
		if !strings.HasPrefix(rest, ":") || !validPort(rest[1:]) {
			return "", "", false
		}
		return strings.ToLower(name), rest[1:], true
	}
	name = hp
	if i := strings.LastIndex(hp, ":"); i >= 0 {
		if strings.Count(hp, ":") > 1 || !validPort(hp[i+1:]) { // a bare IPv6 address is not a valid Host
			return "", "", false
		}
		name, port = hp[:i], hp[i+1:]
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == "" {
		return "", "", false
	}
	return name, port, true
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 0 && n <= 65535 && p != ""
}

func (h *hostList) allows(hostport string) bool {
	name, port, ok := splitHost(hostport)
	if !ok {
		return false
	}
	return h.names[name] || (port != "" && h.pairs[net.JoinHostPort(name, port)])
}

// originOK reports whether an Origin header is acceptable for a request with this Host: it must parse, be on the
// allowlist, and name the same host and port the request used. "null" and anything else fail.
func (h *hostList) originOK(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if !h.allows(u.Host) {
		return false
	}
	on, op, _ := splitHost(u.Host)
	hn, hp, ok := splitHost(host)
	if !ok || on != hn {
		return false
	}
	// an origin without a port means the scheme's default; the Host of the request may carry it or not
	if op == "" {
		op = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	if hp == "" {
		hp = op
	}
	return op == hp
}

// --- failed-authentication throttle ---

const (
	maxThrottleEntries = 2048
	maxBackoff         = 15 * time.Minute
)

type failEntry struct {
	start   time.Time // start of the current counting window
	count   int
	strikes int // lock-outs so far: the back-off doubles with each
	until   time.Time
	last    time.Time
}

// throttle counts failed authentications per remote address and over all addresses. After limit failures inside one
// window an address is locked out for window, then 2 windows, 4, and so on up to 15 minutes. Memory is bounded: past
// maxThrottleEntries addresses the idle ones are dropped, and when none is idle new addresses share one entry.
type throttle struct {
	mu            sync.Mutex
	now           func() time.Time
	window        time.Duration
	limit, global int
	ents          map[string]*failEntry
	all           failEntry
	swept         time.Time
}

func newThrottle(limit, global int, window time.Duration, now func() time.Time) *throttle {
	if now == nil {
		now = time.Now
	}
	if window <= 0 {
		window = time.Minute
	}
	if limit == 0 {
		limit = 10
	}
	if global == 0 && limit > 0 {
		global = 10 * limit
	}
	return &throttle{now: now, window: window, limit: limit, global: global, ents: map[string]*failEntry{}}
}

func (t *throttle) enabled() bool { return t != nil && t.limit > 0 }

func (t *throttle) backoff(strikes int) time.Duration {
	d := t.window
	for i := 1; i < strikes && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

// wait is how long this address must still wait (0: not locked out).
func (t *throttle) wait(addr string) time.Duration {
	if !t.enabled() {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	w := max(t.all.until.Sub(now), 0)
	if e, ok := t.ents[t.key(addr)]; ok {
		w = max(w, e.until.Sub(now))
	}
	return max(w, 0)
}

func (t *throttle) key(addr string) string {
	if _, ok := t.ents[addr]; ok || len(t.ents) < maxThrottleEntries {
		return addr
	}
	if now := t.now(); now.Sub(t.swept) >= t.window/4 { // a full table is swept at most a few times per window
		t.swept = now
		t.sweep()
	}
	if len(t.ents) < maxThrottleEntries {
		return addr
	}
	return "*"
}

// sweep drops the entries that are neither locked out nor recent.
func (t *throttle) sweep() {
	now := t.now()
	for k, e := range t.ents {
		if now.After(e.until) && now.Sub(e.last) > 2*t.window {
			delete(t.ents, k)
		}
	}
}

func (t *throttle) hit(e *failEntry, limit int) {
	now := t.now()
	if now.Sub(e.last) > maxBackoff+2*t.window { // quiet for long: forget the earlier lock-outs
		e.strikes = 0
	}
	if e.start.IsZero() || now.Sub(e.start) >= t.window {
		e.start, e.count = now, 0
	}
	e.last = now
	e.count++
	if limit > 0 && e.count >= limit {
		e.strikes++
		e.until = now.Add(t.backoff(e.strikes))
		e.count = 0
		e.start = e.until
	}
}

// fail records one failed authentication.
func (t *throttle) fail(addr string) {
	if !t.enabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	k := t.key(addr)
	e := t.ents[k]
	if e == nil {
		e = &failEntry{}
		t.ents[k] = e
	}
	t.hit(e, t.limit)
	t.hit(&t.all, t.global)
}

func (t *throttle) size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.ents)
}

// remoteAddr is the peer address without the port. X-Forwarded-For is not read: nothing in front of the console is
// trusted to set it.
func remoteAddr(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// --- fixed-window rate limit per key ---

type rateLimiter struct {
	mu     sync.Mutex
	now    func() time.Time
	limit  int
	window time.Duration
	m      map[string]*rateWin
}

type rateWin struct {
	start time.Time
	n     int
}

func newRateLimiter(limit int, window time.Duration, now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{now: now, limit: limit, window: window, m: map[string]*rateWin{}}
}

// allow counts one request and returns false, with the time to wait, when the key is over its limit.
func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	if l == nil || l.limit <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.m) >= 256 {
		for k, w := range l.m {
			if now.Sub(w.start) >= l.window {
				delete(l.m, k)
			}
		}
		if _, ok := l.m[key]; !ok && len(l.m) >= 256 {
			key = "*"
		}
	}
	w := l.m[key]
	if w == nil || now.Sub(w.start) >= l.window {
		w = &rateWin{start: now}
		l.m[key] = w
	}
	if w.n >= l.limit {
		return false, w.start.Add(l.window).Sub(now)
	}
	w.n++
	return true, 0
}
