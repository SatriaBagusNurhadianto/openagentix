// Package sanitize turns agent-supplied text into something safe to relay:
// control and bidirectional-override characters are removed, chat-platform
// markup is neutralised, and the fixed "automated message" header is built.
// Text is data: nothing here interprets it.
package sanitize

import (
	"strings"
	"unicode/utf8"
)

// Clean removes control characters (keeping \n and \t), normalises CR and CRLF
// to \n, drops bidirectional overrides and the byte order mark, and replaces
// invalid UTF-8 with U+FFFD. Zero-width joiners are kept so emoji sequences
// survive.
func Clean(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	rs := []rune(strings.ToValidUTF8(s, "�"))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\r':
			b.WriteByte('\n')
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		case r == 0x061C || r == 0x200E || r == 0x200F || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0xFEFF:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Line cleans s and collapses it to one trimmed line (for titles and labels).
func Line(s string) string {
	return strings.Join(strings.Fields(Clean(s)), " ")
}

// Slack escapes the three characters Slack treats as control syntax, which
// neutralises <!channel>, <!here>, <@U...>, <#C...> and <url|label> links.
// Order matters: & first, so the entities added below are not escaped again.
func Slack(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// Header is the fixed first line of every message. label comes from operator
// configuration, never from the agent.
func Header(label string) string {
	return "[Automated message from " + Line(label) + "]"
}

// NeutraliseHeader stops agent text from forging the header line: any
// occurrence of header in text is rewritten so it no longer matches.
func NeutraliseHeader(text, header string) string {
	if header == "" || !strings.Contains(text, header) {
		return text
	}
	return strings.ReplaceAll(text, header, "(quoted) "+header[1:len(header)-1])
}

// RuneCount is the number of characters (not bytes) in s.
func RuneCount(s string) int { return utf8.RuneCountInString(s) }
