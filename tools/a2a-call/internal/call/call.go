// Package call sends one prompt to an A2A 1.0 agent and reports how the task ended.
//
// It is the caller behind the chart's `schedules[]`: one JSON-RPC SendMessage per run, with a caller identity
// (X-User-Id), because kagent keys sessions and long-term memory by that id and refuses memory agents without one.
package call

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultMaxResponse is the largest response body accepted when Config.MaxResponse is 0.
const DefaultMaxResponse = 4 << 20

// maxDetail bounds the failure text copied into an error (it is platform detail, not the answer).
const maxDetail = 300

// Config describes one call. UserID is required: without it kagent maps the caller to a shared default user and
// memory-enabled agents fail ("memory requires caller identity").
type Config struct {
	URL         string
	UserID      string
	Prompt      string
	Timeout     time.Duration // whole call, connect to last byte
	MaxResponse int64         // response body limit in bytes; 0 = DefaultMaxResponse
	HTTPClient  *http.Client  // optional; redirects are never followed either way
	TokenFile   string        // optional (A2A_TOKEN_FILE): a bearer token read from this file for the request, e.g. a projected ServiceAccount token
	Traceparent string        // optional incoming W3C traceparent to continue; empty or malformed starts a new trace
}

// Result is what the agent returned. Answer is private data: callers decide whether to print it.
type Result struct {
	State     string
	TaskID    string
	ContextID string
	Answer    string
	Duration  time.Duration
	TraceID   string // the trace this run started (or continued); set even when the call fails, so a failure can be looked up
}

// NotCompletedError means the task ended (or stopped) in a state other than TASK_STATE_COMPLETED.
type NotCompletedError struct{ State, Detail string }

func (e *NotCompletedError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("task ended in %s", e.State)
	}
	return fmt.Sprintf("task ended in %s: %s", e.State, e.Detail)
}

// HTTPError is a non-2xx response (a redirect counts: it is not followed).
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("agent returned HTTP %d", e.Status) }

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, clip(e.Message))
}

var (
	ErrTimeout  = errors.New("timed out waiting for the agent")
	ErrTooLarge = errors.New("response body too large")
)

type part struct {
	Text string `json:"text,omitempty"`
}

type message struct {
	MessageID string `json:"messageId"`
	Role      string `json:"role"`
	Parts     []part `json:"parts"`
}

type rpcResponse struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result struct {
		Task *struct {
			ID        string `json:"id"`
			ContextID string `json:"contextId"`
			Status    struct {
				State   string `json:"state"`
				Message *struct {
					Parts []part `json:"parts"`
				} `json:"message"`
			} `json:"status"`
			Artifacts []struct {
				Parts []part `json:"parts"`
			} `json:"artifacts"`
		} `json:"task"`
	} `json:"result"`
}

// Run sends the prompt as a new conversation and returns the task's outcome. The error is nil only when the task
// reached TASK_STATE_COMPLETED. On NotCompletedError the Result still carries State and TaskID.
func Run(ctx context.Context, c Config) (res Result, _ error) {
	switch {
	case strings.TrimSpace(c.URL) == "":
		return res, errors.New("A2A URL is empty")
	case strings.TrimSpace(c.UserID) == "":
		return res, errors.New("user id is empty: kagent needs a caller identity for memory-enabled agents")
	case strings.TrimSpace(c.Prompt) == "":
		return res, errors.New("prompt is empty")
	case c.Timeout <= 0:
		return res, errors.New("timeout must be positive")
	}
	limit := c.MaxResponse
	if limit <= 0 {
		limit = DefaultMaxResponse
	}
	token, err := readToken(c.TokenFile)
	if err != nil {
		return res, err
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "1", "method": "SendMessage",
		"params": map[string]any{"message": message{MessageID: newID(), Role: "ROLE_USER", Parts: []part{{Text: c.Prompt}}}},
	})
	if err != nil {
		return res, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return res, fmt.Errorf("bad A2A URL: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", "1.0")
	req.Header.Set("X-User-Id", c.UserID)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	tp, traceID := newTraceparent(c.Traceparent)
	req.Header.Set("traceparent", tp) // traceparent only: no baggage, so no id or user id rides to model providers
	res.TraceID = traceID

	hc := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if c.HTTPClient != nil {
		hc = *c.HTTPClient
		hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	start := time.Now()
	defer func() { res.Duration = time.Since(start) }()

	resp, err := hc.Do(req)
	if err != nil {
		return res, wrapNet(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return res, &HTTPError{Status: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return res, wrapNet(ctx, err)
	}
	if int64(len(raw)) > limit {
		return res, ErrTooLarge
	}
	var out rpcResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return res, fmt.Errorf("malformed response: %w", err)
	}
	if out.Error != nil {
		return res, &RPCError{Code: out.Error.Code, Message: out.Error.Message}
	}
	t := out.Result.Task
	if t == nil {
		return res, errors.New("response has no task (result.task is missing)")
	}
	res.State, res.TaskID, res.ContextID = t.Status.State, t.ID, t.ContextID
	var texts []string
	for _, a := range t.Artifacts {
		for _, p := range a.Parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
	}
	res.Answer = strings.Join(texts, "\n")
	if t.Status.State != "TASK_STATE_COMPLETED" {
		var detail []string
		if t.Status.Message != nil {
			for _, p := range t.Status.Message.Parts {
				if p.Text != "" {
					detail = append(detail, p.Text)
				}
			}
		}
		res.Answer = "" // a failed run's text is not an answer
		return res, &NotCompletedError{State: t.Status.State, Detail: clip(strings.Join(detail, " "))}
	}
	return res, nil
}

// readToken returns the token in path, or "" when no file is configured. A configured file that cannot be read or
// is empty is an error: the call is never sent without the token it was set up to carry.
func readToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the token file named by A2A_TOKEN_FILE: %w", errors.Unwrap(err))
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", errors.New("the token file named by A2A_TOKEN_FILE is empty")
	}
	return t, nil
}

func wrapNet(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrTimeout
	}
	return fmt.Errorf("request failed: %w", err)
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxDetail {
		return s[:maxDetail] + "..."
	}
	return s
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
