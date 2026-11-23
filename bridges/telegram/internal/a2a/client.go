// Package a2a is a minimal A2A 1.0 JSON-RPC client.
package a2a

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/openagentix/openagentix/bridges/telegram/internal/httpx"
)

type userIDKey struct{}

// WithUserID returns a context whose user id Send passes to the agent in the X-User-Id header. kagent scopes
// sessions and long-term memory by that id (insecure auth mode); without it every caller shares one default user.
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey{}, id)
}

// UserIDFromContext returns the id set by WithUserID, or "".
func UserIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(userIDKey{}).(string)
	return id
}

type Client struct {
	url   string
	token string
	http  *http.Client
}

func NewClient(url, token string, hc *http.Client) *Client {
	return &Client{url: url, token: token, http: hc}
}

type part struct {
	Text string `json:"text,omitempty"`
}

type message struct {
	MessageID string `json:"messageId"`
	ContextID string `json:"contextId,omitempty"`
	Role      string `json:"role"`
	Parts     []part `json:"parts"`
}

func (c *Client) Send(ctx context.Context, contextID, text string) (string, string, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "1", "method": "SendMessage",
		"params": map[string]any{"message": message{
			MessageID: newID(), ContextID: contextID, Role: "ROLE_USER", Parts: []part{{Text: text}},
		}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", "1.0")
	if id := UserIDFromContext(ctx); id != "" {
		req.Header.Set("X-User-Id", id)
	}
	// W3C trace propagation (traceparent only; baggage is never sent). The caller's trace wins, else this call starts one.
	tc, ok := TraceFromContext(ctx)
	if !ok {
		tc = NewTrace()
	}
	req.Header.Set("traceparent", tc.Traceparent())
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", fmt.Errorf("a2a: agent returned HTTP %d", resp.StatusCode)
	}
	var out rpcResponse
	raw, err := httpx.ReadBody(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("a2a: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", err
	}
	if out.Error != nil {
		return "", "", fmt.Errorf("a2a: agent error %d: %s", out.Error.Code, out.Error.Message)
	}
	var texts []string
	var ctxID string
	switch {
	case out.Result.Message != nil:
		m := out.Result.Message
		ctxID = m.ContextID
		texts = partTexts(texts, m.Parts)
	case out.Result.Task != nil:
		t := out.Result.Task
		ctxID = t.ContextID
		switch t.Status.State {
		case "TASK_STATE_COMPLETED", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED":
			// COMPLETED carries the answer; the other two carry the agent's question.
		default:
			// FAILED, CANCELED, REJECTED and every non-terminal or unknown state
			// (SUBMITTED, WORKING, UNSPECIFIED, ...) is an error. Never relay
			// status text: it is provider/internal detail or a half-done answer.
			// The caller logs this error and shows the user a generic reply.
			var detail []string
			if t.Status.Message != nil {
				detail = partTexts(detail, t.Status.Message.Parts)
			}
			return "", ctxID, fmt.Errorf("a2a: task ended in %q: %s", t.Status.State, strings.Join(detail, " "))
		}
		for _, a := range t.Artifacts {
			texts = partTexts(texts, a.Parts)
		}
		if t.Status.Message != nil {
			texts = partTexts(texts, t.Status.Message.Parts)
		}
	}
	if len(texts) == 0 {
		return "", ctxID, errors.New("a2a: agent reply contained no text")
	}
	return strings.Join(texts, "\n"), ctxID, nil
}

type rpcResponse struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result struct {
		Message *message `json:"message"`
		Task    *task    `json:"task"`
	} `json:"result"`
}

type task struct {
	ContextID string `json:"contextId"`
	Status    struct {
		State   string   `json:"state"`
		Message *message `json:"message"`
	} `json:"status"`
	Artifacts []struct {
		Parts []part `json:"parts"`
	} `json:"artifacts"`
}

func partTexts(dst []string, parts []part) []string {
	for _, p := range parts {
		if p.Text != "" {
			dst = append(dst, p.Text)
		}
	}
	return dst
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
