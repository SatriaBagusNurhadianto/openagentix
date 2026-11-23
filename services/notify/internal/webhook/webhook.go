// Package webhook posts a JSON notification to one fixed URL, optionally
// signed with HMAC-SHA256. Redirects are never followed and the response size
// is capped.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/openagentix/openagentix/services/notify/internal/delivery"
	"github.com/openagentix/openagentix/services/notify/internal/httpx"
)

// SignatureHeader carries "sha256=<hex HMAC of the request body>".
const SignatureHeader = "X-Notify-Signature"

// MaxResponseBytes caps what is read back from the receiver.
const MaxResponseBytes = 64 << 10

// Config builds a Client.
type Config struct {
	URL    string
	Secret string // optional; enables the signature header
	HTTP   *http.Client
}

// Client implements delivery.Sender for a generic webhook.
type Client struct {
	url, secret string
	http        *http.Client
}

// New returns a Client. The URL must already have passed ValidateURL.
func New(cfg Config) *Client { return &Client{url: cfg.URL, secret: cfg.Secret, http: cfg.HTTP} }

// privateLiteral reports whether host is an IP literal that is loopback,
// link-local or unspecified (IPv4-mapped IPv6 forms included). Hostnames are
// not resolved here: DNS-based cases are the egress NetworkPolicy's job.
func privateLiteral(host string) bool {
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified()
}

// ValidateURL requires an absolute https URL (http only if allowInsecure, for
// in-cluster receivers) with a host, no credentials and no fragment. Errors
// never repeat the URL, which may itself be a secret.
func ValidateURL(raw string, allowInsecure, allowPrivate bool) error {
	u, err := url.Parse(raw)
	switch {
	case raw == "":
		return errors.New("url is required")
	case err != nil:
		return errors.New("url is not valid")
	case u.Scheme != "https" && !(allowInsecure && u.Scheme == "http"):
		if u.Scheme == "http" {
			return errors.New("url must use https (set allowInsecureHTTP: true only for in-cluster receivers)")
		}
		return errors.New("url must use https")
	case u.Hostname() == "":
		return errors.New("url has no host")
	case u.User != nil:
		return errors.New("url must not contain credentials")
	case u.Fragment != "":
		return errors.New("url must not contain a fragment")
	}
	if !allowPrivate && privateLiteral(u.Hostname()) {
		return errors.New("url points at a loopback, link-local (for example cloud metadata) or unspecified address; set allowPrivateAddress: true only if that is really intended")
	}
	return nil
}

type payload struct {
	Title       string `json:"title"`
	Text        string `json:"text"`
	Destination string `json:"destination"`
	SentAt      string `json:"sent_at"`
}

// Send posts the message. text carries the automated-message header as its
// first line so a receiver that just displays it keeps the provenance.
func (c *Client) Send(ctx context.Context, m delivery.Message) error {
	body, _ := json.Marshal(payload{
		Title: m.Title, Text: m.Header + "\n\n" + m.Text, Destination: m.Destination,
		SentAt: m.SentAt.UTC().Format(time.RFC3339),
	})
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return &delivery.Error{Kind: delivery.Upstream, Detail: "invalid webhook endpoint", Definite: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "openagentix-notify")
	if c.secret != "" {
		mac := hmac.New(sha256.New, []byte(c.secret))
		mac.Write(body)
		req.Header.Set(SignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := httpx.Do(ctx, c.http, req, MaxResponseBytes)
	if err != nil {
		return err
	}
	switch {
	case resp.Status >= 200 && resp.Status < 300:
		return nil
	case resp.Status == http.StatusTooManyRequests:
		return &delivery.Error{Kind: delivery.RateLimited, Detail: "the webhook receiver rate limited the request", RetryAfter: httpx.RetryAfter(resp.Header.Get("Retry-After")), Definite: true}
	default:
		return &delivery.Error{Kind: delivery.Upstream, Detail: fmt.Sprintf("the webhook receiver refused the message (HTTP %d)", resp.Status), Definite: resp.Status >= 400 && resp.Status < 500}
	}
}
