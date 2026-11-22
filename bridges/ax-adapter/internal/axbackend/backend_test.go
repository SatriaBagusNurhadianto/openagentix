package axbackend

import (
	"testing"

	"github.com/google/ax/pkg/apis/v1alpha1"
)

func TestBuildTask(t *testing.T) {
	tk, err := buildTask(TaskSpec{
		Name: "turn-abc", Atespace: "default", Workspace: "assistant-ws", Gateway: "anthropic-gw",
		Goal: "hi", SessionID: "ctx1",
		Config: map[string]any{"contextId": "ctx1", "history": []any{map[string]any{"role": "user", "text": "x"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tk.GetApiVersion() != v1alpha1.APIVersion || tk.GetKind() != v1alpha1.KindTask {
		t.Fatalf("apiVersion/kind: %q %q", tk.GetApiVersion(), tk.GetKind())
	}
	if tk.GetMetadata().GetName() != "turn-abc" || tk.GetMetadata().GetAtespace() != "default" {
		t.Fatalf("metadata: %v", tk.GetMetadata())
	}
	s := tk.GetSpec()
	if s.GetWorkspace().GetName() != "assistant-ws" || s.GetGateway().GetName() != "anthropic-gw" ||
		s.GetGoal().GetText() != "hi" || s.GetSessionId() != "ctx1" {
		t.Fatalf("spec: %v", s)
	}
	if s.GetProfile() != nil {
		t.Fatal("profile must be omitted when no image configured")
	}
	cfg := s.GetConfig().AsMap()
	if cfg["contextId"] != "ctx1" || len(cfg["history"].([]any)) != 1 {
		t.Fatalf("config: %v", cfg)
	}
}

func TestBuildTaskOptionalFields(t *testing.T) {
	tk, err := buildTask(TaskSpec{Name: "n", Atespace: "a", Workspace: "w", Image: "img@sha256:x", Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if tk.GetSpec().GetGateway() != nil {
		t.Fatal("gateway must be omitted when empty")
	}
	if tk.GetSpec().GetProfile().GetImage() != "img@sha256:x" {
		t.Fatal("image should set profile")
	}
}

func task(state v1alpha1.TaskState, o *v1alpha1.TaskOutcome) *v1alpha1.Task {
	return &v1alpha1.Task{Status: &v1alpha1.TaskStatus{State: state, Outcome: o}}
}

func TestStatusFrom(t *testing.T) {
	cases := []struct {
		name          string
		in            *v1alpha1.Task
		done, success bool
	}{
		{"working", task(v1alpha1.TaskState_TASK_STATE_WORKING, nil), false, false},
		{"suspended", task(v1alpha1.TaskState_TASK_STATE_SUSPENDED, nil), false, false},
		{"completed ok", task(v1alpha1.TaskState_TASK_STATE_COMPLETED, &v1alpha1.TaskOutcome{Result: v1alpha1.TaskResult_TASK_RESULT_SUCCEEDED, Output: "x"}), true, true},
		{"completed nonzero", task(v1alpha1.TaskState_TASK_STATE_COMPLETED, &v1alpha1.TaskOutcome{Result: v1alpha1.TaskResult_TASK_RESULT_FAILED, ExitCode: 2, Output: "boom"}), true, false},
		{"succeeded but exit 1", task(v1alpha1.TaskState_TASK_STATE_COMPLETED, &v1alpha1.TaskOutcome{Result: v1alpha1.TaskResult_TASK_RESULT_SUCCEEDED, ExitCode: 1}), true, false},
		{"failed state", task(v1alpha1.TaskState_TASK_STATE_FAILED, nil), true, false},
		{"cancelled", task(v1alpha1.TaskState_TASK_STATE_CANCELLED, nil), true, false},
		{"outcome only", task(v1alpha1.TaskState_TASK_STATE_IDLE, &v1alpha1.TaskOutcome{Result: v1alpha1.TaskResult_TASK_RESULT_SUCCEEDED}), true, true},
	}
	for _, c := range cases {
		got := statusFrom(c.in)
		if got.Done != c.done || got.Succeeded != c.success {
			t.Errorf("%s: done=%v success=%v, want %v %v", c.name, got.Done, got.Succeeded, c.done, c.success)
		}
	}
}

func TestStatusFromDetail(t *testing.T) {
	tk := task(v1alpha1.TaskState_TASK_STATE_FAILED, nil)
	tk.Status.StatusDetail = &v1alpha1.TaskStatusDetail{Detail: "image pull failed"}
	if got := statusFrom(tk); got.Detail != "image pull failed" {
		t.Fatalf("detail: %q", got.Detail)
	}
}
