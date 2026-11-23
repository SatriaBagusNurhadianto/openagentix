// Package slack posts plain-text messages to one Slack channel with
// chat.postMessage. The channel and bot token are fixed at construction.
// Agent text is escaped so it cannot ping people or render links.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/openagentix/openagentix/services/notify/internal/delivery"
	"github.com/openagentix/openagentix/services/notify/internal/httpx"
	"github.com/openagentix/openagentix/services/notify/internal/sanitize"
)

// DefaultBaseURL is the public Slack Web API host.
const DefaultBaseURL = "https://slack.com"

const maxResponseBytes = 1 << 20

var errCodeRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// Config builds a Client.
type Config struct {
	BaseURL       string // default DefaultBaseURL; tests point it at a fake
	Token         string
	Channel       string
	HTTP          *http.Client
	MaxRetryAfter time.Duration
}

// Client implements delivery.Sender for Slack.
type Client struct {
	base, token, channel string
	http                 *http.Client
	maxRetryAfter        time.Duration
	sleep                func(ctx context.Context, d time.Duration) error // test hook
}

// New returns a Client.
func New(cfg Config) *Client {
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{base: strings.TrimRight(base, "/"), token: cfg.Token, channel: cfg.Channel, http: cfg.HTTP, maxRetryAfter: cfg.MaxRetryAfter, sleep: sleepCtx}
}

// Compose lays out header, optional title, blank line, text, with every
// character Slack treats as syntax escaped.
func Compose(m delivery.Message) string {
	var b strings.Builder
	b.WriteString(m.Header)
	b.WriteString("\n")
	if m.Title != "" {
		b.WriteString(m.Title)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.Text)
	return sanitize.Slack(b.String())
}

// Send posts the message.
func (c *Client) Send(ctx context.Context, m delivery.Message) error {
	text := Compose(m)
	for attempt := 0; ; attempt++ {
		status, code, retry, err := c.post(ctx, text)
		if err != nil {
			return err
		}
		switch {
		case status == 200 && code == "":
			return nil
		case status == http.StatusTooManyRequests || code == "ratelimited":
			if attempt == 0 && retry > 0 && retry <= c.maxRetryAfter {
				if err := c.sleep(ctx, retry); err != nil {
					return &delivery.Error{Kind: delivery.Timeout, Detail: "timed out waiting to retry after a rate limit"}
				}
				continue
			}
			return &delivery.Error{Kind: delivery.RateLimited, Detail: "slack rate limited the request", RetryAfter: retry}
		case code != "":
			return &delivery.Error{Kind: delivery.Upstream, Detail: "slack error: " + code}
		default:
			return &delivery.Error{Kind: delivery.Upstream, Detail: fmt.Sprintf("slack refused the message (HTTP %d)", status)}
		}
	}
}

// post returns the HTTP status, Slack's error code ("" on success) and any
// Retry-After.
func (c *Client) post(ctx context.Context, text string) (int, string, time.Duration, error) {
	body, _ := json.Marshal(map[string]any{
		"channel": c.channel, "text": text,
		"mrkdwn": false, "link_names": false, "unfurl_links": false, "unfurl_media": false,
	})
	req, err := http.NewRequest(http.MethodPost, c.base+"/api/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return 0, "", 0, &delivery.Error{Kind: delivery.Upstream, Detail: "invalid slack endpoint"}
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := httpx.Do(ctx, c.http, req, maxResponseBytes)
	if err != nil {
		return 0, "", 0, err
	}
	retry := httpx.RetryAfter(resp.Header.Get("Retry-After"))
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal(resp.Body, &env); jerr != nil {
		if resp.Status == http.StatusOK {
			return 0, "", 0, &delivery.Error{Kind: delivery.Upstream, Detail: "slack sent an undecodable response"}
		}
		return resp.Status, "", retry, nil
	}
	if resp.Status == http.StatusOK && !env.OK {
		code := env.Error
		if !errCodeRe.MatchString(code) {
			code = "unknown_error"
		}
		return resp.Status, code, retry, nil
	}
	return resp.Status, "", retry, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
