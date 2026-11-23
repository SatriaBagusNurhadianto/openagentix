// Package mcpserver exposes the notify service to agents as an MCP server over
// the streamable HTTP transport, using the official Go SDK.
//
// The endpoint has no authentication of its own: the tool gateway in front of
// it authenticates agents, and a NetworkPolicy keeps everyone else out.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openagentix/openagentix/services/notify/internal/notify"
	"github.com/openagentix/openagentix/services/notify/internal/obs"
)

// Options configures the MCP handler.
type Options struct {
	// Stateless serves each request independently (no Mcp-Session-Id), which
	// lets any replica answer any request.
	Stateless bool
	Version   string
	// Timeout bounds every tool call. The SDK does not hand the HTTP request's
	// deadline to tool handlers, so the server-wide request timeout has to be
	// applied here too; zero means no bound.
	Timeout time.Duration
	// MaxRequestBytes bounds one HTTP request body (the SDK default is 4 MiB).
	// Zero means DefaultMaxRequestBytes.
	MaxRequestBytes int64
	// Metrics, when non-nil, counts tool calls by tool and outcome. Never arguments or results.
	Metrics *obs.Metrics
}

// DefaultMaxRequestBytes is used when Options.MaxRequestBytes is zero.
const DefaultMaxRequestBytes = 64 << 10

// SendInput is the notify_send argument object. There is deliberately no field
// for a chat id, channel, URL or credential: the destination name is the only
// routing choice an agent has.
type SendInput struct {
	Destination    string `json:"destination" jsonschema:"name of a configured destination; see notify_list_destinations"`
	Text           string `json:"text" jsonschema:"the message body, plain text"`
	Title          string `json:"title,omitempty" jsonschema:"optional one-line title shown above the body"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"optional key (letters, digits and . _ : -); repeating it for the same message returns the earlier receipt instead of sending again"`
}

// SendOutput is the structured result of notify_send.
type SendOutput struct {
	Destination  string `json:"destination"`
	Platform     string `json:"platform"`
	DeliveryID   string `json:"delivery_id"`
	SentAt       string `json:"sent_at"`
	Deduplicated bool   `json:"deduplicated"`
}

// ListInput is the (empty) notify_list_destinations argument object.
type ListInput struct{}

// DestinationEntry is one destination as agents see it.
type DestinationEntry struct {
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	Description  string `json:"description"`
	MaxTextChars int    `json:"max_text_chars"`
}

// ListOutput is the structured result of notify_list_destinations.
type ListOutput struct {
	Destinations []DestinationEntry `json:"destinations"`
}

// New returns the HTTP handler to mount at /mcp.
func New(svc *notify.Service, log *slog.Logger, opts Options) http.Handler {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.MaxRequestBytes <= 0 {
		opts.MaxRequestBytes = DefaultMaxRequestBytes
	}
	h := &handlers{svc: svc, timeout: opts.Timeout, metrics: opts.Metrics}
	srv := mcp.NewServer(&mcp.Implementation{Name: "openagentix-notify", Title: "OpenAgentix Notify", Version: opts.Version}, &mcp.ServerOptions{
		Instructions: "Deliver a result to a pre-approved chat destination. Call notify_list_destinations to see the destination names, then notify_send with one of them. You cannot choose a chat, channel or URL: only the configured names work. Messages are marked as automated.",
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "notify_send",
		Description: "Send a plain-text message to one named destination (a chat or channel an operator has approved). Use it to deliver the final result of a task started by a schedule or webhook. The message is prefixed with a line saying it comes from an automated agent. Fails with a clear message if the destination is unknown, the text is too long, or the destination is rate limited.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(true)},
	}, h.send)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "notify_list_destinations",
		Description: "List the destinations notify_send accepts: each name, its platform, a description of who receives it, and its text length limit.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, h.list)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Stateless:           opts.Stateless,
		JSONResponse:        true,
		Logger:              log,
		MaxRequestBodyBytes: opts.MaxRequestBytes,
	})
}

func boolPtr(b bool) *bool { return &b }

type handlers struct {
	svc     *notify.Service
	timeout time.Duration
	metrics *obs.Metrics
}

// sendOutcome names a send failure with a value from the closed set the metrics accept.
func sendOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	var ne *notify.Error
	if !errors.As(err, &ne) {
		return "error"
	}
	switch ne.Kind {
	case notify.Invalid:
		return "invalid"
	case notify.UnknownDestination:
		return "unknown_destination"
	case notify.TooLong:
		return "too_long"
	case notify.RateLimited:
		return "rate_limited"
	case notify.Busy:
		return "busy"
	case notify.Upstream:
		return "upstream"
	case notify.Timeout:
		return "timeout"
	case notify.Disabled:
		return "disabled"
	case notify.Uncertain:
		return "uncertain"
	}
	return "error"
}

// bound derives the context of one tool call.
func (h *handlers) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if h.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, h.timeout)
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func (h *handlers) send(ctx context.Context, _ *mcp.CallToolRequest, in SendInput) (*mcp.CallToolResult, SendOutput, error) {
	ctx, cancel := h.bound(ctx)
	defer cancel()
	began := time.Now()
	res, err := h.svc.Send(ctx, notify.Request{Destination: in.Destination, Text: in.Text, Title: in.Title, IdempotencyKey: in.IdempotencyKey})
	h.metrics.ToolCall("notify_send", sendOutcome(err), time.Since(began))
	if err != nil {
		var ne *notify.Error
		if errors.As(err, &ne) {
			return nil, SendOutput{}, errors.New(ne.Msg)
		}
		return nil, SendOutput{}, errors.New("the notify service failed; try again later")
	}
	out := SendOutput{
		Destination: res.Destination, Platform: res.Platform, DeliveryID: res.DeliveryID,
		SentAt: res.SentAt.UTC().Format(time.RFC3339), Deduplicated: res.Deduplicated,
	}
	msg := fmt.Sprintf("Delivered to %q (%s). delivery_id=%s sent_at=%s", out.Destination, out.Platform, out.DeliveryID, out.SentAt)
	if out.Deduplicated {
		msg = fmt.Sprintf("Already delivered earlier to %q (%s); not sent again. delivery_id=%s sent_at=%s", out.Destination, out.Platform, out.DeliveryID, out.SentAt)
	}
	return textResult(msg), out, nil
}

func (h *handlers) list(context.Context, *mcp.CallToolRequest, ListInput) (*mcp.CallToolResult, ListOutput, error) {
	began := time.Now()
	defer func() { h.metrics.ToolCall("notify_list_destinations", "ok", time.Since(began)) }()
	infos := h.svc.List()
	out := ListOutput{Destinations: make([]DestinationEntry, len(infos))}
	var sb strings.Builder
	if len(infos) == 0 {
		sb.WriteString("No destinations are configured, so notify_send will refuse every request.")
	}
	for i, d := range infos {
		out.Destinations[i] = DestinationEntry{Name: d.Name, Platform: d.Platform, Description: d.Description, MaxTextChars: d.MaxTextChars}
		fmt.Fprintf(&sb, "%s (%s, up to %d characters): %s\n", d.Name, d.Platform, d.MaxTextChars, d.Description)
	}
	return textResult(strings.TrimRight(sb.String(), "\n")), out, nil
}
