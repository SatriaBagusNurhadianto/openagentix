// Package builtin holds the community implementations of the extension points in package ext.
package builtin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openagentix/openagentix/services/console/ext"
)

// Operator is the one identity of the community edition (ADR 0007, rule 5).
const Operator = "console"

// TokenAuth accepts "Authorization: Bearer <token>" where the token is the content of a file (a mounted Secret). The
// file is looked at on every request (a stat is cheap) and read again when its size or modification time changed, so
// a rotated Secret takes effect without a restart. An empty or unreadable file refuses everybody.
type TokenAuth struct {
	Path string
	// MinLen and Reject make a weak token unusable: a file holding fewer than MinLen characters, or one of the Reject
	// values (the demo token), refuses everybody, also after a rotation. Zero values: no check.
	MinLen int
	Reject []string

	mu    sync.Mutex
	mtime time.Time
	size  int64
	sum   [sha256.Size]byte
	ok    bool
}

func (t *TokenAuth) current() ([sha256.Size]byte, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fi, err := os.Stat(t.Path)
	if err != nil {
		t.ok = false
		return t.sum, false
	}
	if t.ok && fi.ModTime().Equal(t.mtime) && fi.Size() == t.size {
		return t.sum, true
	}
	b, err := os.ReadFile(t.Path)
	tok := strings.TrimSpace(string(b))
	if err != nil || tok == "" || WeakToken(tok, t.MinLen, t.Reject) != "" {
		t.ok = false
		return t.sum, false
	}
	t.sum, t.mtime, t.size, t.ok = sha256.Sum256([]byte(tok)), fi.ModTime(), fi.Size(), true
	return t.sum, true
}

// WeakToken says why a token must not be used ("" when it may): shorter than min characters, or one of the rejected values.
func WeakToken(tok string, min int, reject []string) string {
	tok = strings.TrimSpace(tok)
	if len(tok) < min {
		return "it has " + strconv.Itoa(len(tok)) + " characters; a token needs at least " + strconv.Itoa(min)
	}
	for _, r := range reject {
		if tok == r {
			return "it is the published demo token"
		}
	}
	return ""
}

// Authenticate implements ext.Authenticator.
func (t *TokenAuth) Authenticate(r *http.Request) (ext.Identity, error) {
	want, ok := t.current()
	if !ok {
		return ext.Identity{}, ext.ErrUnauthenticated
	}
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) <= len(p) || !strings.EqualFold(h[:len(p)], p) || len(h) > 8<<10 {
		return ext.Identity{}, ext.ErrUnauthenticated
	}
	got := sha256.Sum256([]byte(strings.TrimSpace(h[len(p):])))
	// both sides are hashed first, so the comparison is over equal lengths and its time does not depend on the token
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return ext.Identity{}, ext.ErrUnauthenticated
	}
	return ext.Identity{Subject: Operator, Roles: []string{"operator"}}, nil
}

// StaticAuth is for the demo mode: one fixed token held in memory.
type StaticAuth struct{ Token string }

// Authenticate implements ext.Authenticator.
func (s StaticAuth) Authenticate(r *http.Request) (ext.Identity, error) {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) <= len(p) || !strings.EqualFold(h[:len(p)], p) || s.Token == "" {
		return ext.Identity{}, ext.ErrUnauthenticated
	}
	got, want := sha256.Sum256([]byte(strings.TrimSpace(h[len(p):]))), sha256.Sum256([]byte(s.Token))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return ext.Identity{}, ext.ErrUnauthenticated
	}
	return ext.Identity{Subject: Operator, Roles: []string{"operator"}}, nil
}

// RoleAuthz has one role. The operator may do everything this API offers; with Viewer set every write is refused.
type RoleAuthz struct{ Viewer bool }

// Authorize implements ext.Authorizer.
func (a RoleAuthz) Authorize(_ context.Context, _ ext.Identity, action ext.Action, _ ext.Resource) ext.Decision {
	if a.Viewer && action.Writes() {
		return ext.Decision{Reason: "This console is read-only (viewer mode). Ask an operator to turn off viewer mode in the console settings."}
	}
	return ext.Decision{Allowed: true}
}

// LogAudit writes one JSON line per write to its logger.
type LogAudit struct{ Log *slog.Logger }

// Record implements ext.AuditSink.
func (l LogAudit) Record(ctx context.Context, e ext.AuditEvent) error {
	l.Log.LogAttrs(ctx, slog.LevelInfo, "audit",
		slog.Bool("audit", true),
		slog.String("who", e.Subject),
		slog.String("action", string(e.Action)),
		slog.String("kind", e.Resource.Kind),
		slog.String("agent", e.Resource.Name),
		slog.String("outcome", e.Outcome),
		slog.Int("status", e.Status),
		slog.String("request_id", e.RequestID),
	)
	return nil
}
