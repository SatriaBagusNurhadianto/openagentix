package axbackend

import (
	"context"
	"fmt"

	"github.com/google/ax/pkg/apis/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

// GRPC is the real Backend, talking plaintext gRPC to ax-server.
type GRPC struct {
	c    v1alpha1.AXClient
	conn *grpc.ClientConn
}

// Dial connects lazily to addr (host:port) of ax-server.
func Dial(addr string) (*GRPC, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}
	return &GRPC{c: v1alpha1.NewAXClient(conn), conn: conn}, nil
}

func (g *GRPC) Close() error { return g.conn.Close() }

func buildTask(s TaskSpec) (*v1alpha1.Task, error) {
	spec := &v1alpha1.TaskSpec{
		Workspace: &v1alpha1.WorkspaceRef{Name: s.Workspace},
		Goal:      &v1alpha1.Goal{Text: s.Goal},
		SessionId: s.SessionID,
	}
	if s.Gateway != "" {
		spec.Gateway = &v1alpha1.GatewayRef{Name: s.Gateway}
	}
	if s.Image != "" {
		spec.Profile = &v1alpha1.Profile{Image: s.Image}
	}
	if s.Config != nil {
		cfg, err := structpb.NewStruct(s.Config)
		if err != nil {
			return nil, fmt.Errorf("encoding config: %w", err)
		}
		spec.Config = cfg
	}
	return &v1alpha1.Task{
		ApiVersion: v1alpha1.APIVersion,
		Kind:       v1alpha1.KindTask,
		Metadata:   &v1alpha1.ObjectMeta{Name: s.Name, Atespace: s.Atespace},
		Spec:       spec,
	}, nil
}

func statusFrom(t *v1alpha1.Task) Status {
	st := t.GetStatus()
	out := st.GetOutcome()
	var s Status
	switch st.GetState() {
	case v1alpha1.TaskState_TASK_STATE_COMPLETED, v1alpha1.TaskState_TASK_STATE_FAILED, v1alpha1.TaskState_TASK_STATE_CANCELLED:
		s.Done = true
	}
	if out != nil && out.GetResult() != v1alpha1.TaskResult_TASK_RESULT_UNSPECIFIED {
		s.Done = true
	}
	s.ExitCode = out.GetExitCode()
	s.Output = out.GetOutput()
	s.Succeeded = out.GetResult() == v1alpha1.TaskResult_TASK_RESULT_SUCCEEDED && out.GetExitCode() == 0
	if m := out.GetStatus().GetMessage(); m != "" {
		s.Detail = m
	}
	if d := st.GetStatusDetail().GetDetail(); d != "" {
		s.Detail = d
	}
	return s
}

func (g *GRPC) CreateTask(ctx context.Context, s TaskSpec) error {
	t, err := buildTask(s)
	if err != nil {
		return err
	}
	_, err = g.c.CreateTask(ctx, &v1alpha1.CreateTaskRequest{Task: t})
	return err
}

func (g *GRPC) ResumeTask(ctx context.Context, atespace, name string) error {
	_, err := g.c.ResumeTask(ctx, &v1alpha1.ResumeTaskRequest{Atespace: atespace, Name: name})
	return err
}

func (g *GRPC) GetTask(ctx context.Context, atespace, name string) (Status, error) {
	t, err := g.c.GetTask(ctx, &v1alpha1.GetTaskRequest{Atespace: atespace, Name: name})
	if err != nil {
		return Status{}, err
	}
	return statusFrom(t), nil
}

func (g *GRPC) DeleteTask(ctx context.Context, atespace, name string) error {
	_, err := g.c.DeleteTask(ctx, &v1alpha1.DeleteTaskRequest{Atespace: atespace, Name: name})
	return err
}
