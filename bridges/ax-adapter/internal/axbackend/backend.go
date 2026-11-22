package axbackend

import "context"

type TaskSpec struct {
	Name, Atespace, Workspace, Gateway, Image, Goal, SessionID string
	Config                                                     map[string]any
}

type Status struct {
	Done, Succeeded bool
	ExitCode        int32
	Output, Detail  string
}

type Backend interface {
	CreateTask(ctx context.Context, s TaskSpec) error
	ResumeTask(ctx context.Context, atespace, name string) error
	GetTask(ctx context.Context, atespace, name string) (Status, error)
	DeleteTask(ctx context.Context, atespace, name string) error
}
