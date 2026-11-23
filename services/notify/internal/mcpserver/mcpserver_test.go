package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openagentix/openagentix/services/notify/internal/config"
	"github.com/openagentix/openagentix/services/notify/internal/destinations"
	"github.com/openagentix/openagentix/services/notify/internal/httpx"
	"github.com/openagentix/openagentix/services/notify/internal/notify"
)

const (
	tgToken   = "999999:FAKE-TG-TOKEN-zzz"
	tgChat    = "-1005550001234"
	slToken   = "xoxb-FAKE-SLACK-9999"
	slChannel = "C0FAKEPRIVATE"
	hookURL   = "https://hooks.example.com/PRIVATE-HOOK-PATH"
)

const destYAML = `
destinations:
  - name: ops
    description: On-call operations chat
    platform: telegram
    chatId: "` + tgChat + `"
    tokenEnv: TG
    maxTextChars: 50
    rate: {perMinute: 2, perHour: 50}
  - name: team
    description: Engineering channel
    platform: slack
    channel: ` + slChannel + `
    tokenEnv: SL
  - name: audit
    description: Audit receiver
    platform: webhook
    url: ` + hookURL + `
`

type upstream struct {
	mu     sync.Mutex
	paths  []string
	bodies []map[string]any
	reply  func(path string) (int, string)
	hang   chan struct{}
}

type env struct {
	session *mcp.ClientSession
	up      *upstream
	logs    *bytes.Buffer
}

func setup(t *testing.T, stateless bool, timeout time.Duration, yaml string) *env {
	t.Helper()
	up := &upstream{reply: func(string) (int, string) { return 200, `{"ok":true,"result":{},"ts":"1.2"}` }}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		up.mu.Lock()
		up.paths = append(up.paths, r.URL.Path)
		up.bodies = append(up.bodies, body)
		hang, reply := up.hang, up.reply
		up.mu.Unlock()
		if hang != nil {
			select {
			case <-hang:
			case <-r.Context().Done():
			}
		}
		code, resp := reply(r.URL.Path)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(fake.Close)

	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	envs := map[string]string{"TG": tgToken, "SL": slToken}
	ds, err := destinations.Parse([]byte(yaml), destinations.Options{
		Defaults: config.Limits{MaxTextChars: 4000, RatePerMinute: 6, RatePerHour: 60},
		HTTP:     httpx.NewClient(5 * time.Second), MaxRetryAfter: time.Second,
		TelegramBaseURL: fake.URL, SlackBaseURL: fake.URL,
		Getenv: func(k string) string { return envs[k] },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := notify.New(notify.Options{Destinations: ds, Label: "Test Agent", MaxInFlight: 4, IdempotencyWindow: time.Minute, IdempotencyMaxEntries: 10, Log: log})
	mux := http.NewServeMux()
	mux.Handle("/mcp", New(svc, log, Options{Stateless: stateless, Version: "test", Timeout: timeout}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &env{session: session, up: up, logs: logs}
}

func (u *upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.paths)
}

func (e *env) call(t *testing.T, tool string, args any) (*mcp.CallToolResult, error) {
	t.Helper()
	return e.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
}

func (e *env) mustCall(t *testing.T, tool string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := e.call(t, tool, args)
	if err != nil {
		t.Fatalf("CallTool(%s): protocol error: %v", tool, err)
	}
	return res
}

func text(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	var sb strings.Builder
	for _, c := range r.Content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("non-text content %T", c)
		}
		sb.WriteString(tc.Text)
	}
	return sb.String()
}

func structured[T any](t *testing.T, r *mcp.CallToolResult) T {
	t.Helper()
	b, _ := json.Marshal(r.StructuredContent)
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("structured content: %v (%s)", err, b)
	}
	return v
}

func assertNoPrivate(t *testing.T, where, s string) {
	t.Helper()
	for _, leak := range []string{tgToken, "FAKE-TG-TOKEN", tgChat, "5550001234", slToken, "xoxb", slChannel, "PRIVATE-HOOK-PATH", "hooks.example.com", "127.0.0.1"} {
		if strings.Contains(s, leak) {
			t.Fatalf("%s leaks %q: %s", where, leak, s)
		}
	}
}

func TestListTools_SendAndListWithClosedSchemas(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	res, err := e.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	if len(got) != 2 || got["notify_send"] == nil || got["notify_list_destinations"] == nil {
		t.Fatalf("tools = %v", got)
	}
	b, _ := json.Marshal(got["notify_send"].InputSchema)
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		Additional json.RawMessage            `json:"additionalProperties"`
	}
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties) != 4 {
		t.Fatalf("notify_send must accept exactly destination, text, title, idempotency_key; got %s", b)
	}
	for _, p := range []string{"destination", "text", "title", "idempotency_key"} {
		if _, ok := schema.Properties[p]; !ok {
			t.Fatalf("missing property %s in %s", p, b)
		}
	}
	if strings.Join(schema.Required, ",") != "destination,text" && strings.Join(schema.Required, ",") != "text,destination" {
		t.Fatalf("required = %v", schema.Required)
	}
	if string(schema.Additional) != "false" {
		t.Fatalf("additionalProperties must be false, got %s", schema.Additional)
	}
	if got["notify_send"].Annotations == nil || got["notify_send"].Annotations.ReadOnlyHint {
		t.Fatalf("notify_send must not be marked read-only: %+v", got["notify_send"].Annotations)
	}
	if got["notify_list_destinations"].Annotations == nil || !got["notify_list_destinations"].Annotations.ReadOnlyHint {
		t.Fatal("notify_list_destinations should be read-only")
	}
}

func TestBothTransportModesServe(t *testing.T) {
	for _, stateless := range []bool{true, false} {
		e := setup(t, stateless, 0, destYAML)
		res := e.mustCall(t, "notify_list_destinations", map[string]any{})
		if res.IsError {
			t.Fatalf("stateless=%v: %s", stateless, text(t, res))
		}
	}
}

func TestListDestinations_NamesPlatformsDescriptionsOnly(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	res := e.mustCall(t, "notify_list_destinations", map[string]any{})
	if res.IsError {
		t.Fatal(text(t, res))
	}
	out := structured[ListOutput](t, res)
	if len(out.Destinations) != 3 || out.Destinations[0].Name != "audit" || out.Destinations[0].Platform != "webhook" ||
		out.Destinations[1].Name != "ops" || out.Destinations[1].Description != "On-call operations chat" || out.Destinations[1].MaxTextChars != 50 ||
		out.Destinations[2].Name != "team" {
		t.Fatalf("out = %+v", out)
	}
	assertNoPrivate(t, "text", text(t, res))
	b, _ := json.Marshal(res.StructuredContent)
	assertNoPrivate(t, "structured", string(b))
}

func TestSend_DeliversToTheConfiguredChatAndReturnsReceipt(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	res := e.mustCall(t, "notify_send", map[string]any{"destination": "ops", "text": "report ready", "title": "Nightly"})
	if res.IsError {
		t.Fatal(text(t, res))
	}
	out := structured[SendOutput](t, res)
	if out.Destination != "ops" || out.Platform != "telegram" || !strings.HasPrefix(out.DeliveryID, "ntf_") || out.Deduplicated {
		t.Fatalf("out = %+v", out)
	}
	if _, err := time.Parse(time.RFC3339, out.SentAt); err != nil {
		t.Fatalf("sent_at = %q", out.SentAt)
	}
	if e.up.paths[0] != "/bot"+tgToken+"/sendMessage" || e.up.bodies[0]["chat_id"] != tgChat {
		t.Fatalf("upstream got %v %v", e.up.paths, e.up.bodies)
	}
	if !strings.Contains(e.up.bodies[0]["text"].(string), "[Automated message from Test Agent]") {
		t.Fatalf("header missing: %v", e.up.bodies[0]["text"])
	}
	assertNoPrivate(t, "result", text(t, res))
}

func TestSend_SlackMessageIsNeutralised(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	res := e.mustCall(t, "notify_send", map[string]any{"destination": "team", "text": "<!channel> look"})
	if res.IsError {
		t.Fatal(text(t, res))
	}
	body := e.up.bodies[0]
	if body["channel"] != slChannel || strings.Contains(body["text"].(string), "<!channel>") {
		t.Fatalf("body = %v", body)
	}
}

func TestSend_ExtraArgumentsCannotRedirectTheMessage(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	for _, extra := range []map[string]any{
		{"chat_id": "123"}, {"chatId": "123"}, {"channel": "CATTACKER"}, {"url": "https://evil.example/x"}, {"token": "x"},
	} {
		args := map[string]any{"destination": "ops", "text": "hi"}
		for k, v := range extra {
			args[k] = v
		}
		res, err := e.call(t, "notify_send", args)
		if err == nil && !res.IsError && e.up.count() > 0 {
			if e.up.bodies[0]["chat_id"] != tgChat {
				t.Fatalf("message went to an agent-chosen chat: %v", e.up.bodies[0])
			}
		}
		for _, b := range e.up.bodies {
			if strings.Contains(toJSON(b), "CATTACKER") || strings.Contains(toJSON(b), "evil.example") {
				t.Fatalf("agent-chosen value reached upstream: %v", b)
			}
		}
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestSend_UnknownDestinationListsValidNames(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	res := e.mustCall(t, "notify_send", map[string]any{"destination": "-1001234", "text": "hi"})
	msg := text(t, res)
	if !res.IsError || !strings.Contains(msg, "audit, ops, team") {
		t.Fatalf("isError=%v msg=%s", res.IsError, msg)
	}
	if e.up.count() != 0 {
		t.Fatal("nothing may be sent")
	}
}

func TestSend_TooLongStatesTheLimit(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	res := e.mustCall(t, "notify_send", map[string]any{"destination": "ops", "text": strings.Repeat("a", 51)})
	if msg := text(t, res); !res.IsError || !strings.Contains(msg, "50") || !strings.Contains(msg, "too long") {
		t.Fatalf("isError=%v msg=%s", res.IsError, msg)
	}
}

func TestSend_RateLimitedStatesWhenToRetry(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	for i := 0; i < 2; i++ {
		if res := e.mustCall(t, "notify_send", map[string]any{"destination": "ops", "text": "hi"}); res.IsError {
			t.Fatal(text(t, res))
		}
	}
	res := e.mustCall(t, "notify_send", map[string]any{"destination": "ops", "text": "hi"})
	if msg := text(t, res); !res.IsError || !strings.Contains(msg, "rate limit") || !strings.Contains(msg, "retry in") {
		t.Fatalf("isError=%v msg=%s", res.IsError, msg)
	}
}

func TestSend_UpstreamFailureHasNoSecrets(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	e.up.reply = func(string) (int, string) {
		return 400, `{"ok":false,"error_code":400,"description":"chat ` + tgChat + ` not found; token ` + tgToken + `"}`
	}
	res := e.mustCall(t, "notify_send", map[string]any{"destination": "ops", "text": "hi"})
	msg := text(t, res)
	if !res.IsError || !strings.Contains(msg, "could not deliver") {
		t.Fatalf("isError=%v msg=%s", res.IsError, msg)
	}
	assertNoPrivate(t, "tool error", msg)
	assertNoPrivate(t, "logs", e.logs.String())
}

func TestSend_ValidationErrorsAreToolErrors(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	for _, args := range []map[string]any{
		{"destination": "ops", "text": "   "},
		{"destination": "ops", "text": "hi", "idempotency_key": "bad key"},
	} {
		res := e.mustCall(t, "notify_send", args)
		if !res.IsError {
			t.Fatalf("args %v: want a tool error", args)
		}
	}
	if res, err := e.call(t, "notify_send", map[string]any{"text": "no destination"}); err == nil && !res.IsError {
		t.Fatal("missing destination must be an error")
	}
}

func TestSend_IdempotencyKeyDedupes(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	args := map[string]any{"destination": "ops", "text": "hi", "idempotency_key": "run-1"}
	first := structured[SendOutput](t, e.mustCall(t, "notify_send", args))
	second := structured[SendOutput](t, e.mustCall(t, "notify_send", args))
	if e.up.count() != 1 || second.DeliveryID != first.DeliveryID || !second.Deduplicated {
		t.Fatalf("upstream calls=%d first=%+v second=%+v", e.up.count(), first, second)
	}
}

func TestSend_ToolCallHonoursTheServerTimeout(t *testing.T) {
	e := setup(t, true, 200*time.Millisecond, destYAML)
	e.up.hang = make(chan struct{})
	t.Cleanup(func() { close(e.up.hang) })
	start := time.Now()
	res := e.mustCall(t, "notify_send", map[string]any{"destination": "ops", "text": "hi"})
	if time.Since(start) > 3*time.Second {
		t.Fatalf("tool call ignored the timeout: took %v (outbound timeout is 5s)", time.Since(start))
	}
	if msg := text(t, res); !res.IsError || !strings.Contains(msg, "did not answer in time") {
		t.Fatalf("isError=%v msg=%s", res.IsError, msg)
	}
}

func TestNoDestinations_SendIsRefusedAndListIsEmpty(t *testing.T) {
	e := setup(t, true, 0, "destinations: []\n")
	res := e.mustCall(t, "notify_send", map[string]any{"destination": "ops", "text": "hi"})
	if msg := text(t, res); !res.IsError || !strings.Contains(msg, "no destinations") {
		t.Fatalf("isError=%v msg=%s", res.IsError, msg)
	}
	list := e.mustCall(t, "notify_list_destinations", map[string]any{})
	if out := structured[ListOutput](t, list); len(out.Destinations) != 0 || list.IsError {
		t.Fatalf("out = %+v", out)
	}
}

func TestLogsNeverContainMessageContentOrPrivateValues(t *testing.T) {
	e := setup(t, true, 0, destYAML)
	e.mustCall(t, "notify_send", map[string]any{"destination": "ops", "text": "CONFIDENTIAL-BODY", "title": "CONFIDENTIAL-TITLE", "idempotency_key": "KEY-ABC"})
	e.mustCall(t, "notify_send", map[string]any{"destination": "nope", "text": "CONFIDENTIAL-BODY"})
	e.mustCall(t, "notify_list_destinations", map[string]any{})
	logs := e.logs.String()
	for _, leak := range []string{"CONFIDENTIAL", "KEY-ABC"} {
		if strings.Contains(logs, leak) {
			t.Fatalf("logs leak %q:\n%s", leak, logs)
		}
	}
	assertNoPrivate(t, "logs", logs)
	if !strings.Contains(logs, `"outcome":"sent"`) {
		t.Fatalf("expected an outcome in logs:\n%s", logs)
	}
}
