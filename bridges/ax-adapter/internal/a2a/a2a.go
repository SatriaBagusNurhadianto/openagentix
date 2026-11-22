// Package a2a serves the A2A 1.0 JSON-RPC SendMessage surface, with the same
// response shapes kagent returns (see bridges/telegram/internal/a2a/testdata):
// completed turns carry the answer in artifacts; failures are HTTP 200 with
// TASK_STATE_FAILED and the text in status.message.
package a2a

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openagentix/openagentix/bridges/ax-adapter/internal/turn"
)

type Runner interface {
	Run(ctx context.Context, contextID, text string) turn.Result
}

const (
	usageKey  = "https://kagent.dev/extensions/usage/v1"
	claudeKey = "openagentix.dev/ax/claude"

	codeParse   = -32700
	codeMethod  = -32601
	codeParams  = -32602
	codeVersion = -32009
	maxBody     = 1 << 20
)

func NewHandler(r Runner) http.Handler {
	h := &handler{r: r}
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK); _, _ = w.Write([]byte("ok")) }
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)
	mux.HandleFunc("POST /", h.rpc)
	return mux
}

type handler struct{ r Runner }

type rpcReq struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Message *struct {
			ContextID string `json:"contextId"`
			Parts     []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"message"`
	} `json:"params"`
}

func (h *handler) rpc(w http.ResponseWriter, req *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(req.Body, maxBody))
	var in rpcReq
	if err != nil || json.Unmarshal(raw, &in) != nil {
		writeJSON(w, errResp(nil, codeParse, "parse error"))
		return
	}
	if v := req.Header.Get("A2A-Version"); v != "" && v != "1.0" {
		writeJSON(w, errResp(in.ID, codeVersion, "unsupported A2A version "+v+"; this adapter speaks 1.0"))
		return
	}
	if in.Method != "SendMessage" {
		writeJSON(w, errResp(in.ID, codeMethod, "method not found: "+in.Method))
		return
	}
	var texts []string
	if in.Params.Message != nil {
		for _, p := range in.Params.Message.Parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
	}
	if len(texts) == 0 {
		writeJSON(w, errResp(in.ID, codeParams, "message must contain at least one text part"))
		return
	}
	text := strings.Join(texts, "\n")
	res := h.r.Run(req.Context(), in.Params.Message.ContextID, text)
	writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": rawID(in.ID), "result": map[string]any{"task": taskJSON(res, text)}})
}

func taskJSON(res turn.Result, userText string) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	taskID := res.TaskID
	if taskID == "" {
		taskID = randID()
	}
	task := map[string]any{
		"id": taskID, "contextId": res.ContextID,
		"history": []any{map[string]any{
			"messageId": randID(), "contextId": res.ContextID, "taskId": taskID,
			"role": "ROLE_USER", "parts": []any{map[string]any{"text": userText}},
		}},
	}
	if res.Failed {
		task["status"] = map[string]any{
			"state": "TASK_STATE_FAILED", "timestamp": now,
			"message": map[string]any{
				"messageId": randID(), "contextId": res.ContextID, "taskId": taskID,
				"role": "ROLE_AGENT", "parts": []any{map[string]any{"text": res.Text}},
			},
		}
		return task
	}
	task["status"] = map[string]any{"state": "TASK_STATE_COMPLETED", "timestamp": now}
	task["artifacts"] = []any{map[string]any{"artifactId": randID(), "parts": []any{map[string]any{"text": res.Text}}}}
	md := map[string]any{}
	if res.Usage != nil {
		md[usageKey] = res.Usage
	}
	if res.Claude != nil {
		md[claudeKey] = res.Claude
	}
	if len(md) > 0 {
		task["metadata"] = md
	}
	return task
}

func errResp(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": rawID(id), "error": map[string]any{"code": code, "message": msg}}
}

func rawID(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func randID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
