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
//
// It uses its own transport rather than http.DefaultTransport so that proxy
// environment variables (HTTP_PROXY, HTTPS_PROXY, ALL_PROXY) are never
// honoured: destinations are reached directly, and the egress NetworkPolicy
// is the only control on where traffic goes. Dial, TLS and header phases have
// their own bounds in addition to the overall timeout.
func NewClient(timeout time.Duration) *http.Client {
	dialTimeout := min(timeout, 5*time.Second)
	tr := &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    dialTimeout,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  time.Second,
		IdleConnTimeout:        60 * time.Second,
		MaxIdleConns:           10,
		MaxIdleConnsPerHost:    2,
		MaxConnsPerHost:        8,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
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
//
// Only a failed dial is marked Definite: the connection never opened, so
// nothing was written. Every other transport failure (timeout, reset, TLS
// error, cancellation) may have happened after the request left, so it stays
// uncertain.
func transportError(ctx context.Context, err error) error {
	var ne net.Error
	var oe *net.OpError
	dialFailed := errors.As(err, &oe) && oe.Op == "dial"
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return &delivery.Error{Kind: delivery.Timeout, Detail: "the destination did not answer in time", Definite: dialFailed}
	case errors.Is(err, context.Canceled):
		return &delivery.Error{Kind: delivery.Upstream, Detail: "request cancelled"}
	default:
		return &delivery.Error{Kind: delivery.Upstream, Detail: "could not reach the destination", Definite: dialFailed}
	}
}

// MaxRetryAfterWait caps any Retry-After value taken from a platform.
const MaxRetryAfterWait = 24 * time.Hour

// Seconds converts a platform-supplied number of seconds to a Duration,
// clamped to [0, MaxRetryAfterWait] so hostile values cannot overflow.
func Seconds(n int64) time.Duration {
	switch {
	case n <= 0:
		return 0
	case n >= int64(MaxRetryAfterWait/time.Second):
		return MaxRetryAfterWait
	}
	return time.Duration(n) * time.Second
}

// RetryAfter parses a Retry-After header given in whole seconds (the HTTP-date
// form is not used by Telegram or Slack and is treated as absent).
func RetryAfter(v string) time.Duration {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0
	}
	return Seconds(n)
}
