package call

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

// A W3C trace context (version 00): this tool only STARTS (or continues) a trace by sending the traceparent header;
// it exports no spans, so the backend's first span is the platform's and its parent id was never recorded.
var traceparentRe = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$`)

// newTraceparent returns the header value and its trace id. A valid incoming traceparent keeps its trace id (and gets a
// new span id); an empty or malformed one is ignored and a new trace starts. Flags are always 01 (sampled).
func newTraceparent(incoming string) (header, traceID string) {
	if m := traceparentRe.FindStringSubmatch(strings.TrimSpace(incoming)); m != nil &&
		m[1] != strings.Repeat("0", 32) && m[2] != strings.Repeat("0", 16) {
		traceID = m[1]
	} else {
		traceID = randHex(16)
	}
	return "00-" + traceID + "-" + randHex(8) + "-01", traceID
}

// randHex returns n random bytes as hex, never all zero (an all-zero id is invalid in W3C trace context).
func randHex(n int) string {
	b := make([]byte, n)
	for {
		_, _ = rand.Read(b)
		for _, c := range b {
			if c != 0 {
				return hex.EncodeToString(b)
			}
		}
	}
}
