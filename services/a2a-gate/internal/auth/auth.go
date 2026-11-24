// Package auth identifies the caller of the gate. Two kinds of bearer token are accepted:
//   - a projected ServiceAccount token, checked by a Kubernetes TokenReview for one audience (a positive answer
//     is cached for 30 seconds, the same as kagent's own cache);
//   - a static key, matched by its SHA-256 against hashes in a mounted Secret (one file per caller, named like
//     the caller).
//
// A token is never logged, never stored (only its hash is a cache key) and never forwarded.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openagentix/openagentix/services/a2a-gate/internal/rules"
)

var (
	// ErrUnauthenticated: no token, or the token is not valid (HTTP 401).
	ErrUnauthenticated = errors.New("not authenticated")
	// ErrUnknownCaller: the token is valid but its owner has no rule (HTTP 403).
	ErrUnknownCaller = errors.New("caller has no rule")
	// ErrUnavailable: the token could not be checked (HTTP 503; the gate fails closed).
	ErrUnavailable = errors.New("token check unavailable")
)

// Timings of the TokenReview cache.
const (
	PositiveTTL = 30 * time.Second
	NegativeTTL = 5 * time.Second
	maxPositive = 1024 // entries that cost a successful TokenReview: only holders of a valid token can add one
	maxNegative = 1024 // entries any caller can add: kept apart so a flood of bad tokens cannot push a good one out
)

// MaxTokenBytes is the longest bearer value the gate looks at. A projected ServiceAccount token is about 1 KiB.
const MaxTokenBytes = 8 << 10

// Defaults of the TokenReview limits (Options.MaxReviews, Options.ReviewWait).
const (
	defaultMaxReviews = 8
	defaultReviewWait = 2 * time.Second
)

// Reviewer checks a ServiceAccount token for an audience and returns the user name (system:serviceaccount:ns:name).
// A token that is not valid returns ErrUnauthenticated; any other error means the check could not be made.
type Reviewer interface {
	Review(ctx context.Context, token, audience string) (string, error)
}

// Keys supplies the SHA-256 hashes (lower-case hex) of the static keys, by caller name.
type Keys interface {
	Hashes() map[string]string
}

// Options configure an Authenticator.
type Options struct {
	Rules    *rules.Rules
	Audience string
	Keys     Keys
	Reviewer Reviewer
	Now      func() time.Time // default time.Now
	// MaxReviews caps the TokenReviews running at once (default 8). A review that cannot start within ReviewWait
	// (default 2 s) fails with ErrUnavailable: the gate does not queue without bound.
	MaxReviews int
	ReviewWait time.Duration
}

// Authenticator maps a bearer token to a caller rule.
type Authenticator struct {
	o      Options
	mu     sync.Mutex
	pos    map[[32]byte]cached // reviewed and valid (30 s)
	neg    map[[32]byte]cached // reviewed and refused (5 s)
	flight map[[32]byte]*flight
	slots  chan struct{}
}

// flight is one TokenReview in progress; every caller of the same token waits for it instead of starting another.
type flight struct {
	done chan struct{}
	user string
	err  error
}

type cached struct {
	user    string
	err     error
	expires time.Time
}

// New returns an Authenticator.
func New(o Options) *Authenticator {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxReviews <= 0 {
		o.MaxReviews = defaultMaxReviews
	}
	if o.ReviewWait <= 0 {
		o.ReviewWait = defaultReviewWait
	}
	return &Authenticator{o: o, pos: map[[32]byte]cached{}, neg: map[[32]byte]cached{}, flight: map[[32]byte]*flight{}, slots: make(chan struct{}, o.MaxReviews)}
}

// Authenticate returns the caller rule for a bearer token.
func (a *Authenticator) Authenticate(ctx context.Context, token string) (*rules.Caller, error) {
	if token == "" || len(token) > MaxTokenBytes {
		return nil, ErrUnauthenticated
	}
	if c := a.matchKey(token); c != nil {
		return c, nil
	}
	if !looksLikeJWT(token) || !a.plausible(token) {
		return nil, ErrUnauthenticated // no TokenReview is spent on a string that cannot be one of ours
	}
	user, err := a.review(ctx, token)
	if err != nil {
		return nil, err
	}
	ns, name, ok := parseServiceAccount(user)
	if !ok {
		return nil, ErrUnknownCaller
	}
	if c := a.o.Rules.ByServiceAccount(ns, name); c != nil {
		return c, nil
	}
	return nil, ErrUnknownCaller
}

// matchKey compares the token's hash with every key hash (no early exit) and returns the caller with a key rule.
func (a *Authenticator) matchKey(token string) *rules.Caller {
	if a.o.Keys == nil {
		return nil
	}
	h := sha256.Sum256([]byte(token))
	got := []byte(hex.EncodeToString(h[:]))
	var found *rules.Caller
	for name, want := range a.o.Keys.Hashes() {
		eq := subtle.ConstantTimeCompare(got, []byte(want)) == 1
		if eq {
			if c := a.o.Rules.ByName(name); c != nil && c.Key {
				found = c
			}
		}
	}
	return found
}

// plausible is a cheap check of the UNVERIFIED payload: it names our audience, has an issuer and has not expired.
// It only saves TokenReviews; the API server still decides.
func (a *Authenticator) plausible(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return false
	}
	var p struct {
		Aud json.RawMessage `json:"aud"`
		Iss string          `json:"iss"`
		Exp *float64        `json:"exp"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Iss == "" || p.Exp == nil || float64(a.o.Now().Unix()) >= *p.Exp {
		return false
	}
	var one string
	if json.Unmarshal(p.Aud, &one) == nil {
		return one == a.o.Audience
	}
	var many []string
	if json.Unmarshal(p.Aud, &many) == nil {
		for _, v := range many {
			if v == a.o.Audience {
				return true
			}
		}
	}
	return false
}

func (a *Authenticator) review(ctx context.Context, token string) (string, error) {
	if a.o.Reviewer == nil {
		return "", ErrUnavailable
	}
	k := sha256.Sum256([]byte(token))
	now := a.o.Now()
	a.mu.Lock()
	for _, m := range []map[[32]byte]cached{a.pos, a.neg} {
		if e, ok := m[k]; ok && now.Before(e.expires) {
			a.mu.Unlock()
			return e.user, e.err
		}
	}
	if f, ok := a.flight[k]; ok { // the same token is being reviewed: wait for that answer
		a.mu.Unlock()
		select {
		case <-f.done:
			return f.user, f.err
		case <-ctx.Done():
			return "", fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
		}
	}
	f := &flight{done: make(chan struct{})}
	a.flight[k] = f
	a.mu.Unlock()

	f.user, f.err = a.reviewOnce(ctx, k, token, now)
	a.mu.Lock()
	delete(a.flight, k)
	a.mu.Unlock()
	close(f.done)
	return f.user, f.err
}

func (a *Authenticator) reviewOnce(ctx context.Context, k [32]byte, token string, now time.Time) (string, error) {
	wait := time.NewTimer(a.o.ReviewWait)
	defer wait.Stop()
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-wait.C:
		return "", fmt.Errorf("%w: too many token checks at once", ErrUnavailable)
	case <-ctx.Done():
		return "", fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
	}
	user, err := a.o.Reviewer.Review(ctx, token, a.o.Audience)
	switch {
	case err == nil:
		a.put(a.pos, maxPositive, k, cached{user: user, expires: now.Add(PositiveTTL)})
		return user, nil
	case errors.Is(err, ErrUnauthenticated):
		a.put(a.neg, maxNegative, k, cached{err: ErrUnauthenticated, expires: now.Add(NegativeTTL)})
		return "", ErrUnauthenticated
	default:
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
}

// put stores an entry. A full map first drops its expired entries; if it is still full it is emptied, which only
// costs one more TokenReview per live token (the other map is not touched).
func (a *Authenticator) put(m map[[32]byte]cached, limit int, k [32]byte, e cached) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(m) >= limit {
		now := a.o.Now()
		for key, v := range m {
			if !now.Before(v.expires) {
				delete(m, key)
			}
		}
		if len(m) >= limit {
			clear(m)
		}
	}
	m[k] = e
}

func looksLikeJWT(t string) bool { return strings.Count(t, ".") == 2 }

func parseServiceAccount(user string) (ns, name string, ok bool) {
	rest, found := strings.CutPrefix(user, "system:serviceaccount:")
	if !found {
		return "", "", false
	}
	ns, name, ok = strings.Cut(rest, ":")
	return ns, name, ok && ns != "" && name != "" && !strings.Contains(name, ":")
}
