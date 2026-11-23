// Package telegram posts plain-text messages to one Telegram chat through the
// Bot API. The chat id and bot token are fixed at construction: nothing an
// agent sends can change where a message goes.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openagentix/openagentix/services/notify/internal/delivery"
	"github.com/openagentix/openagentix/services/notify/internal/httpx"
)

// MaxMessageUnits is Telegram's limit on message text, counted in UTF-16 code
// units (an emoji outside the BMP counts as two).
const MaxMessageUnits = 4096

const maxResponseBytes = 1 << 20

// DefaultBaseURL is the public Bot API endpoint.
const DefaultBaseURL = "https://api.telegram.org"

// Config builds a Client.
type Config struct {
	BaseURL       string // default DefaultBaseURL; tests point it at a fake
	Token         string
	ChatID        string
	HTTP          *http.Client
	MaxRetryAfter time.Duration // longest 429 wait honoured (once per part)
}

// Client implements delivery.Sender for Telegram.
type Client struct {
	base, token, chatID string
	http                *http.Client
	maxRetryAfter       time.Duration
	sleep               func(ctx context.Context, d time.Duration) error // test hook
}

// New returns a Client.
func New(cfg Config) *Client {
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{base: strings.TrimRight(base, "/"), token: cfg.Token, chatID: cfg.ChatID, http: cfg.HTTP, maxRetryAfter: cfg.MaxRetryAfter, sleep: sleepCtx}
}

// Compose lays out the message: header, optional title, blank line, text.
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
	return b.String()
}

// wordJoiner is invisible and has no width; between a trigger character and
// the word after it, it stops Telegram from recognising a mention, command or
// hashtag.
const wordJoiner = "\u2060"

// Neutralise stops Telegram turning agent text into pings and links. Telegram
// auto-detects @username, /command and #hashtag in plain text, so a word
// joiner (U+2060) is inserted after each such trigger character that starts a
// token: at the start of the text or after anything that is not a letter,
// digit or underscore, and followed by a letter, digit or underscore. That also
// covers "://": the second slash of a scheme starts a token, so
// "https://host" becomes "https://" + U+2060 + "host", which Telegram does
// not link. Mid-word characters are untouched, so "a/b", "input/output" and
// "100%" read as before; "/usr/bin" at the start of a word does gain one
// invisible character.
//
// Residual risk, deliberately not handled to keep prose predictable: bare
// domains ("example.com", "www.example.com"), e-mail addresses, phone numbers
// and $cashtags can still be auto-linked by Telegram clients.
func Neutralise(s string) string {
	if !strings.ContainsAny(s, "@/#") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	var prev rune = -1
	rs := []rune(s)
	for i, r := range rs {
		b.WriteRune(r)
		if (r == '@' || r == '/' || r == '#') && !isWordRune(prev) && i+1 < len(rs) && isWordRune(rs[i+1]) {
			b.WriteString(wordJoiner)
		}
		prev = r
	}
	return b.String()
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// parts is what Send will post: the message neutralised (after cleaning, before
// splitting, so the added characters count toward the UTF-16 limit; the
// operator's header is left alone) and split to fit.
func (c *Client) parts(m delivery.Message) []string {
	m.Title, m.Text = Neutralise(m.Title), Neutralise(m.Text)
	return Split(Compose(m), MaxMessageUnits)
}

// Parts reports how many requests Send will make for m.
func (c *Client) Parts(m delivery.Message) int { return len(c.parts(m)) }

// Send posts the message, split into several parts if it exceeds the limit.
func (c *Client) Send(ctx context.Context, m delivery.Message) error {
	parts := c.parts(m)
	for i, part := range parts {
		if err := c.sendPart(ctx, part); err != nil {
			if de, ok := err.(*delivery.Error); ok {
				de.Sent, de.Total = i, len(parts)
			}
			return err
		}
	}
	return nil
}

type apiResult struct {
	status     int
	errorCode  int
	retryAfter time.Duration
}

func (c *Client) sendPart(ctx context.Context, text string) error {
	for attempt := 0; ; attempt++ {
		res, err := c.post(ctx, text)
		if err != nil {
			return err
		}
		switch {
		case res.status == 200 && res.errorCode == 0:
			return nil
		case res.status == http.StatusTooManyRequests || res.errorCode == http.StatusTooManyRequests:
			if attempt == 0 && res.retryAfter > 0 && res.retryAfter <= c.maxRetryAfter {
				if err := c.sleep(ctx, res.retryAfter); err != nil {
					return &delivery.Error{Kind: delivery.Timeout, Detail: "timed out waiting to retry after a rate limit"}
				}
				continue
			}
			return &delivery.Error{Kind: delivery.RateLimited, Detail: "telegram rate limited the request", RetryAfter: res.retryAfter, Definite: true}
		default:
			code := res.status
			if res.errorCode != 0 {
				code = res.errorCode
			}
			// 4xx: Telegram refused it. 5xx may still have accepted it.
			return &delivery.Error{Kind: delivery.Upstream, Detail: fmt.Sprintf("telegram refused the message (HTTP %d)", code), Definite: code >= 400 && code < 500}
		}
	}
}

func (c *Client) post(ctx context.Context, text string) (apiResult, error) {
	body, _ := json.Marshal(map[string]any{
		"chat_id":              c.chatID,
		"text":                 text,
		"link_preview_options": map[string]any{"is_disabled": true},
	})
	req, err := http.NewRequest(http.MethodPost, c.base+"/bot"+c.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return apiResult{}, &delivery.Error{Kind: delivery.Upstream, Detail: "invalid telegram endpoint", Definite: true}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpx.Do(ctx, c.http, req, maxResponseBytes)
	if err != nil {
		return apiResult{}, err
	}
	var env struct {
		OK         bool `json:"ok"`
		ErrorCode  int  `json:"error_code"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	res := apiResult{status: resp.Status}
	if jerr := json.Unmarshal(resp.Body, &env); jerr != nil {
		if resp.Status == 200 {
			return res, &delivery.Error{Kind: delivery.Upstream, Detail: "telegram sent an undecodable response"}
		}
		return res, nil
	}
	if !env.OK {
		res.errorCode = env.ErrorCode
		if res.errorCode == 0 {
			res.errorCode = resp.Status
		}
	}
	res.retryAfter = httpx.Seconds(env.Parameters.RetryAfter)
	if res.retryAfter == 0 {
		res.retryAfter = httpx.RetryAfter(resp.Header.Get("Retry-After"))
	}
	return res, nil
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

// Split cuts text into parts of at most limit UTF-16 code units, preferring
// to break at a newline, then a space, and never inside a character. Parts
// that are only whitespace are dropped. A break is only taken in the second
// half of a part, so a short first line cannot leave a nearly empty part. (Logic follows bridges/telegram.)
func Split(text string, limit int) []string {
	var parts []string
	add := func(s string) {
		if strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	for text != "" {
		units, end := 0, 0 // end: byte offset of the longest prefix within limit
		for i, r := range text {
			w := 1
			if r >= 0x10000 {
				w = 2
			}
			if units+w > limit {
				break
			}
			units += w
			end = i + utf8.RuneLen(r)
		}
		if end == len(text) {
			add(text)
			break
		}
		cut, skip := end, 0
		if i := strings.LastIndexByte(text[:end], '\n'); i > end/2 {
			cut, skip = i, 1
		} else if i := strings.LastIndexByte(text[:end], ' '); i > end/2 {
			cut, skip = i, 1
		}
		add(text[:cut])
		text = text[cut+skip:]
	}
	return parts
}
