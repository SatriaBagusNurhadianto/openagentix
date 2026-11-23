// Package httpx holds the outbound HTTP helpers shared by the platform
// clients: a client that never follows redirects, bounded response bodies, and
// transport errors reduced to text that cannot leak a URL, host or token.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openagentix/openagentix/services/notify/internal/delivery"
)

// NewClient returns an HTTP client with an overall per-request timeout that
// does not follow redirects (a redirect is returned to the caller as-is).
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Response is a fully read, size-capped HTTP response.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Do sends req and reads at most maxBody bytes of the response; a longer body
// is an error, not a truncation. Every failure is a *delivery.Error.
func Do(ctx context.Context, c *http.Client, req *http.Request, maxBody int64) (*Response, error) {
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return nil, transportError(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, transportError(ctx, err)
	}
	if int64(len(body)) > maxBody {
		return nil, &delivery.Error{Kind: delivery.Upstream, Detail: fmt.Sprintf("response too large (HTTP %d)", resp.StatusCode)}
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// transportError drops the underlying error text: net/http embeds the request
// URL, and for the Telegram API the URL contains the bot token.
func transportError(ctx context.Context, err error) error {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return &delivery.Error{Kind: delivery.Timeout, Detail: "the destination did not answer in time"}
	case errors.Is(err, context.Canceled):
		return &delivery.Error{Kind: delivery.Upstream, Detail: "request cancelled"}
	default:
		return &delivery.Error{Kind: delivery.Upstream, Detail: "could not reach the destination"}
	}
}

// RetryAfter parses a Retry-After header given in whole seconds (the HTTP-date
// form is not used by Telegram or Slack and is treated as absent).
func RetryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}
