// Package ext holds the extension points of the console API (ADR 0010). They are small interfaces with no dependency
// outside the standard library, in a package that is not internal, so that a separate build of the console (for
// example one with OIDC login, roles or an audit store) can supply its own implementations and call server.New with
// them. The community build uses the implementations of internal/builtin: one bearer token, one operator role and one
// JSON log line per write.
package ext

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Identity is who a request acts as. The community build has exactly one: the console operator.
type Identity struct {
	Subject string
	// Roles are free text for an Authorizer to read. The built-in one ignores them.
	Roles []string
}

// ErrUnauthenticated is what an Authenticator returns when the request carries no valid credential. The server turns
// any error from Authenticate into 401 and never shows the error text to the caller.
var ErrUnauthenticated = errors.New("unauthenticated")

// Authenticator turns a request into an identity or an error. It must not read the body. It sees the browser's
// Authorization header, which the server itself never forwards upstream.
type Authenticator interface {
	Authenticate(r *http.Request) (Identity, error)
}

// Action is something a request asks the API to do.
type Action string

const (
	ActionRead     Action = "read"     // session, platform, list and get
	ActionValidate Action = "validate" // dry run, writes nothing
	ActionCreate   Action = "create"
	ActionUpdate   Action = "update"
	ActionDelete   Action = "delete"
)

// Writes reports whether the action changes the cluster. Only writes are audited and only writes are cut off by a
// read-only (viewer) configuration.
func (a Action) Writes() bool { return a == ActionCreate || a == ActionUpdate || a == ActionDelete }

// Resource names what the action is about. Kind is "agentdefinition" for everything this API offers. Name is empty for
// list and platform reads.
type Resource struct {
	Kind string
	Name string
}

// Decision is the answer of an Authorizer. Reason is shown to the caller as the 403 message when Allowed is false, so it
// must be one plain sentence and must not contain secrets.
type Decision struct {
	Allowed bool
	Reason  string
}

// Authorizer decides whether an identity may do an action to a resource.
type Authorizer interface {
	Authorize(ctx context.Context, id Identity, action Action, res Resource) Decision
}

// AuditEvent is one write, after it happened (or was refused). It deliberately has no field for a request body, an
// instruction text, a header or a token: a sink cannot log what it is never given.
type AuditEvent struct {
	Time      time.Time
	Subject   string
	Action    Action
	Resource  Resource
	Outcome   string // ok, denied, conflict, not_found, invalid or error
	Status    int    // the HTTP status sent to the caller
	RequestID string
}

// AuditSink receives one event per write. Record must not block for long and must not panic; the server ignores errors
// (a sink that can fail keeps its own queue).
type AuditSink interface {
	Record(ctx context.Context, e AuditEvent)
}
