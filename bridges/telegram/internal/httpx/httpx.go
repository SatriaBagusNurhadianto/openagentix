// Package httpx holds small HTTP helpers shared by the clients.
package httpx

import (
	"errors"
	"io"
)

// MaxBody is the largest response body the clients accept.
const MaxBody = 4 << 20

// ErrTooLarge is returned when a response body exceeds MaxBody.
var ErrTooLarge = errors.New("response body too large")

// ReadBody reads at most MaxBody bytes; a longer body is ErrTooLarge.
func ReadBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxBody {
		return nil, ErrTooLarge
	}
	return b, nil
}
