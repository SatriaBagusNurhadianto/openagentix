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
	"encoding/hex"
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
	maxCache    = 2048
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
}

// Authenticator maps a bearer token to a caller rule.
type Authenticator struct {
	o     Options
	mu    sync.Mutex
	cache map[[32]byte]cached
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
	return &Authenticator{o: o, cache: map[[32]byte]cached{}}
}

// Authenticate returns the caller rule for a bearer token.
func (a *Authenticator) Authenticate(ctx context.Context, token string) (*rules.Caller, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	if c := a.matchKey(token); c != nil {
		return c, nil
	}
	if !looksLikeJWT(token) {
		return nil, ErrUnauthenticated
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

func (a *Authenticator) review(ctx context.Context, token string) (string, error) {
	if a.o.Reviewer == nil {
		return "", ErrUnavailable
	}
	k := sha256.Sum256([]byte(token))
	now := a.o.Now()
	a.mu.Lock()
	if e, ok := a.cache[k]; ok && now.Before(e.expires) {
		a.mu.Unlock()
		return e.user, e.err
	}
	a.mu.Unlock()
	user, err := a.o.Reviewer.Review(ctx, token, a.o.Audience)
	switch {
	case err == nil:
		a.put(k, cached{user: user, expires: now.Add(PositiveTTL)})
		return user, nil
	case errors.Is(err, ErrUnauthenticated):
		a.put(k, cached{err: ErrUnauthenticated, expires: now.Add(NegativeTTL)})
		return "", ErrUnauthenticated
	default:
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
}

func (a *Authenticator) put(k [32]byte, e cached) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) >= maxCache {
		a.cache = map[[32]byte]cached{}
	}
	a.cache[k] = e
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
