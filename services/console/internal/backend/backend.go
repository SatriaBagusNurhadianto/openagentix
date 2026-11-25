// Package backend is what the HTTP layer needs from "the cluster": read and write AgentDefinitions and read the chart's
// agents. There are two implementations, Kube (the real one) and fake.Backend (the demo mode).
package backend

import (
	"context"
	"fmt"

	"github.com/openagentix/openagentix/services/console/internal/model"
)

// Error is an error with the HTTP status and code the API answers with. Its message is one plain sentence for the person.
type Error struct {
	Status  int
	Code    string
	Message string
	// Detail is for the log only (the upstream status and a shortened reason). It is never sent to the browser.
	Detail string
}

func (e *Error) Error() string { return e.Message }

func Errf(status int, code, f string, a ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(f, a...)}
}

// Backend is the fixed set of things the API does to the cluster.
type Backend interface {
	List(ctx context.Context) (model.AgentList, error)
	Get(ctx context.Context, name string) (model.Detail, error)
	Create(ctx context.Context, name string, spec model.Spec) (model.Detail, error)
	Update(ctx context.Context, name, resourceVersion string, spec model.Spec) (model.Detail, error)
	Delete(ctx context.Context, name string) error
	// Schema is a dry run against the API server (dryRun=All): nothing is written.
	Schema(ctx context.Context, name string, spec model.Spec) (model.SchemaResult, error)
}
