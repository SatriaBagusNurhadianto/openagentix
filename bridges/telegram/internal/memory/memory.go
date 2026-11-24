// Package memory is a minimal gRPC-Web client for two RPCs of kagent's MemoryService, List and Delete, reached through
// the A2A gate (ADR 0006, round 3). It needs no protobuf library: the request is one string field and the answer is read
// with a small bounded decoder.
//
// Which user is meant is not part of the message. kagent takes it from the X-User-Id header, which the gate checks against
// the caller's rule and sets itself. The message names only the agent.
//
// Errors carry numbers only (an HTTP status, a gRPC status). Text from a response, which may hold memory content, is never
// put in an error.
package memory

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openagentix/openagentix/bridges/telegram/internal/httpx"
)

const (
	listPath   = "/kagent.runtime.v1.MemoryService/List"
	deletePath = "/kagent.runtime.v1.MemoryService/Delete"
	// maxItems bounds what the decoder keeps: a user with more memories than this gets the first ones.
	maxItems = 500
)

// Item is one stored memory as the user may see it.
type Item struct {
	Content   string
	ExpiresAt time.Time // zero when kagent sent none
}

// Error is a failed call. It holds numbers only.
type Error struct {
	Op   string // "list" or "delete"
	HTTP int    // HTTP status when it was not 2xx, else 0
	GRPC int    // gRPC status when it was not 0 (the trailer or header said so), else 0
	Msg  string // a fixed phrase chosen by this package, never text from the response
}

func (e *Error) Error() string {
	s := "memory " + e.Op + " failed"
	if e.HTTP != 0 {
		s += ": HTTP " + strconv.Itoa(e.HTTP)
	}
	if e.GRPC != 0 {
		s += ": gRPC status " + strconv.Itoa(e.GRPC)
	}
	if e.Msg != "" {
		s += ": " + e.Msg
	}
	return s
}

// Client calls the gate. base is its URL, agent is "namespace/name" (the agent whose memories are meant).
type Client struct {
	base, agent string
	token       string
	tokenFile   string
	http        *http.Client
}

func NewClient(base, agent string, hc *http.Client) *Client {
	return &Client{base: strings.TrimRight(base, "/"), agent: agent, http: hc}
}

// WithToken sends a fixed bearer token.
func (c *Client) WithToken(t string) *Client { c.token = t; return c }

// WithTokenFile sends the bearer token found in a file, read again on every call (a projected ServiceAccount token is
// rotated by the kubelet). A missing or empty file fails the call; nothing is sent without its token.
func (c *Client) WithTokenFile(path string) *Client { c.tokenFile = path; return c }

func (c *Client) bearer() (string, error) {
	if c.tokenFile == "" {
		return c.token, nil
	}
	b, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return "", fmt.Errorf("memory: cannot read the token file named by A2A_TOKEN_FILE: %w", errors.Unwrap(err))
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", errors.New("memory: the token file named by A2A_TOKEN_FILE is empty")
	}
	return t, nil
}

// List returns what is stored for the user id and the client's agent.
func (c *Client) List(ctx context.Context, userID string) ([]Item, error) {
	body, err := c.call(ctx, "list", listPath, userID)
	if err != nil {
		return nil, err
	}
	items, ok := decodeList(body)
	if !ok {
		return nil, &Error{Op: "list", Msg: "unreadable answer"}
	}
	return items, nil
}

// Delete removes everything stored for the user id and the client's agent.
func (c *Client) Delete(ctx context.Context, userID string) error {
	_, err := c.call(ctx, "delete", deletePath, userID)
	return err
}

// call sends one unary request and returns the data frame of the answer ("" when the answer has none). It checks the
// HTTP status and the gRPC status in the trailer frame (or in the headers of a trailers-only answer).
func (c *Client) call(ctx context.Context, op, path, userID string) ([]byte, error) {
	if userID == "" {
		return nil, &Error{Op: op, Msg: "no user id"}
	}
	token, err := c.bearer()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(frame(0, pbString(1, c.agent))))
	if err != nil {
		return nil, &Error{Op: op, Msg: "bad request"}
	}
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("Accept", "application/grpc-web+proto")
	req.Header.Set("X-Grpc-Web", "1")
	req.Header.Set("X-User-Id", userID)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// a transport error can carry the URL, never the body: keep only the kind
		if ctx.Err() != nil {
			return nil, &Error{Op: op, Msg: "timeout or cancelled"}
		}
		return nil, &Error{Op: op, Msg: "gate not reachable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &Error{Op: op, HTTP: resp.StatusCode}
	}
	if st := resp.Header.Get("Grpc-Status"); st != "" && st != "0" { // trailers-only answer
		n, _ := strconv.Atoi(st)
		return nil, &Error{Op: op, GRPC: max(n, 1)}
	}
	raw, err := httpx.ReadBody(resp.Body)
	if err != nil {
		return nil, &Error{Op: op, Msg: "answer too large or cut off"}
	}
	var data []byte
	sawStatus := resp.Header.Get("Grpc-Status") == "0"
	for len(raw) > 0 {
		if len(raw) < 5 {
			return nil, &Error{Op: op, Msg: "truncated frame"}
		}
		flag, n := raw[0], binary.BigEndian.Uint32(raw[1:5])
		if uint64(n) > uint64(len(raw)-5) {
			return nil, &Error{Op: op, Msg: "truncated frame"}
		}
		payload := raw[5 : 5+n]
		raw = raw[5+n:]
		switch {
		case flag&1 != 0:
			return nil, &Error{Op: op, Msg: "compressed frame"}
		case flag&0x80 != 0: // trailers: only grpc-status is read
			st, ok := trailerStatus(payload)
			if !ok {
				return nil, &Error{Op: op, Msg: "no status"}
			}
			if st != 0 {
				return nil, &Error{Op: op, GRPC: st}
			}
			sawStatus = true
		case flag == 0:
			if data != nil {
				return nil, &Error{Op: op, Msg: "more than one message"}
			}
			data = append([]byte{}, payload...)
		default:
			return nil, &Error{Op: op, Msg: "unknown frame"}
		}
	}
	if !sawStatus {
		return nil, &Error{Op: op, Msg: "no status"}
	}
	return data, nil
}

// trailerStatus reads grpc-status from a trailer frame ("name: value" lines).
func trailerStatus(b []byte) (int, bool) {
	for _, line := range strings.Split(string(b), "\r\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "grpc-status") {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			return n, err == nil && n >= 0
		}
	}
	return 0, false
}

func frame(flag byte, msg []byte) []byte {
	h := []byte{flag, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(h[1:], uint32(len(msg)))
	return append(h, msg...)
}

func pbString(field int, s string) []byte {
	b := binary.AppendUvarint(nil, uint64(field<<3|2))
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// ---- a bounded protobuf reader, enough for MemoryServiceListResponse ----

// decodeList reads repeated MemorySummary (field 1) and keeps the content (2) and the expiry (5, a Timestamp).
func decodeList(b []byte) ([]Item, bool) {
	var items []Item
	for len(b) > 0 {
		field, wt, rest, ok := tag(b)
		if !ok {
			return nil, false
		}
		b = rest
		if field == 1 && wt == 2 {
			msg, rest, ok := lenDelimited(b)
			if !ok {
				return nil, false
			}
			b = rest
			it, ok := decodeSummary(msg)
			if !ok {
				return nil, false
			}
			if len(items) < maxItems {
				items = append(items, it)
			}
			continue
		}
		if b, ok = skip(b, wt); !ok {
			return nil, false
		}
	}
	return items, true
}

func decodeSummary(b []byte) (Item, bool) {
	var it Item
	for len(b) > 0 {
		field, wt, rest, ok := tag(b)
		if !ok {
			return it, false
		}
		b = rest
		switch {
		case field == 2 && wt == 2:
			s, rest, ok := lenDelimited(b)
			if !ok || !utf8.Valid(s) {
				return it, false
			}
			it.Content, b = string(s), rest
		case field == 5 && wt == 2:
			ts, rest, ok := lenDelimited(b)
			if !ok {
				return it, false
			}
			b = rest
			sec, ok := timestampSeconds(ts)
			if !ok {
				return it, false
			}
			if sec > 0 {
				it.ExpiresAt = time.Unix(sec, 0).UTC()
			}
		default:
			if b, ok = skip(b, wt); !ok {
				return it, false
			}
		}
	}
	return it, true
}

func timestampSeconds(b []byte) (int64, bool) {
	var sec int64
	for len(b) > 0 {
		field, wt, rest, ok := tag(b)
		if !ok {
			return 0, false
		}
		b = rest
		if field == 1 && wt == 0 {
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return 0, false
			}
			sec, b = int64(v), b[n:]
			continue
		}
		if b, ok = skip(b, wt); !ok {
			return 0, false
		}
	}
	return sec, true
}

func tag(b []byte) (field, wt int, rest []byte, ok bool) {
	v, n := binary.Uvarint(b)
	if n <= 0 || v>>3 == 0 || v>>3 > 1<<28 {
		return 0, 0, nil, false
	}
	return int(v >> 3), int(v & 7), b[n:], true
}

func lenDelimited(b []byte) (val, rest []byte, ok bool) {
	l, n := binary.Uvarint(b)
	if n <= 0 || l > uint64(len(b)-n) {
		return nil, nil, false
	}
	return b[n : n+int(l)], b[n+int(l):], true
}

// skip passes over one value of wire type wt (varint, 64 bit, length delimited, 32 bit).
func skip(b []byte, wt int) ([]byte, bool) {
	switch wt {
	case 0:
		_, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, false
		}
		return b[n:], true
	case 1:
		if len(b) < 8 {
			return nil, false
		}
		return b[8:], true
	case 2:
		_, rest, ok := lenDelimited(b)
		return rest, ok
	case 5:
		if len(b) < 4 {
			return nil, false
		}
		return b[4:], true
	}
	return nil, false
}
