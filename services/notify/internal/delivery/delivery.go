// Package delivery defines what platform clients (Telegram, Slack, webhook)
// have in common: the message they are handed and the errors they report.
// Errors carry only text that is safe to show an agent and to log: never
// tokens, chat ids, URLs or message content.
package delivery

import (
	"context"
	"fmt"
	"time"
)

// Message is one already-sanitised notification. Header is the fixed
// automated-message line; Text and Title are agent data.
type Message struct {
	Destination string
	Header      string
	Title       string
	Text        string
	SentAt      time.Time
}

// Sender posts a message to one configured destination.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Kind classifies a delivery failure.
type Kind int

const (
	// Upstream: the platform refused or failed the request.
	Upstream Kind = iota
	// RateLimited: the platform asked us to slow down and retrying now would not help.
	RateLimited
	// Timeout: the platform did not answer in time.
	Timeout
)

// Error is a delivery failure with a client-safe description.
type Error struct {
	Kind       Kind
	Detail     string        // safe to show, e.g. "HTTP 502" or "slack error: channel_not_found"
	RetryAfter time.Duration // for RateLimited
	Sent       int           // parts delivered before the failure (multi-part messages)
	Total      int           // parts in the message
}

func (e *Error) Error() string {
	s := e.Detail
	if e.Total > 1 {
		s += fmt.Sprintf(" (delivered %d of %d parts)", e.Sent, e.Total)
	}
	return s
}
