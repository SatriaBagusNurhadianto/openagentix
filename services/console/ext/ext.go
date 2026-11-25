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

// SpecSummary is the small part of a definition an Authorizer may decide on. It is set only on the second authorization
// of a create or an update, after the body was decoded; the instructions and every other field stay out of it.
type SpecSummary struct {
	Model       string
	ToolProfile string
	// Team and Project are the labels openagentix.io/team and openagentix.io/project. The console does not set labels
	// itself, so on a create they are empty; on an update they are the ones stored on the object.
	Team    string
	Project string
}

// Resource names what the action is about. Kind is "agentdefinition" for everything this API offers. Name is empty for
// list and platform reads, and for the first authorization of a create (the body is not decoded yet). Spec is nil unless
// the request carries a definition (create and update, second authorization).
type Resource struct {
	Kind string
	Name string
	Spec *SpecSummary
}

// Decision is the answer of an Authorizer. Reason is shown to the caller as the 403 message when Allowed is false, so it
// must be one plain sentence and must not contain secrets.
type Decision struct {
	Allowed bool
	Reason  string
}

// Authorizer decides whether an identity may do an action to a resource.
//
// For a create and an update the server asks twice: first with the bare resource (no name for a create, no Spec), a cheap
// gate before any body is read; then, after the body is decoded, with the name and a SpecSummary. Both must allow. An
// implementation that decides on the name or the spec must therefore allow the bare question whenever the identity may
// create or update something, and refuse in the second one. GET /api/v1/session asks the bare question for create, update,
// delete and validate and reports only what is allowed, so the UI never promises more than the second question would give.
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
	Outcome   string // ok, denied, conflict, not_found, invalid or error; "pending" in a PreWrite call
	Status    int    // the HTTP status sent to the caller; 0 in a PreWrite call
	RequestID string
}

// AuditSink receives one event per write.
//
// Audit is written AFTER the write and is best effort: Record is called once the answer for the caller is decided, the
// server recovers from a panic in it, logs an error it returns, and neither changes the response. A write that succeeded
// and whose audit line then failed is still a write that happened. Record must not block for long.
//
// A sink that must never lose a line (fail closed) also implements PreWriter.
type AuditSink interface {
	Record(ctx context.Context, e AuditEvent) error
}

// PreWriter is an optional interface of an AuditSink. PreWrite is called before the console writes to the cluster (create,
// update, delete), after authentication and both authorizations, with the same event as the later Record call but
// Outcome "pending" and Status 0. Returning an error refuses the write: the cluster is not touched and the caller gets 403
// when the error is or wraps ErrRefused, 503 for any other error (the sink is not available) or a panic. The built-in sink
// does not implement it.
type PreWriter interface {
	PreWrite(ctx context.Context, e AuditEvent) error
}

// ErrRefused is what a PreWrite returns to say "this write is not allowed" rather than "the sink is broken".
var ErrRefused = errors.New("refused")
