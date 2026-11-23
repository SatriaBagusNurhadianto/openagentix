// Package sanitize turns agent-supplied text into something safe to relay:
// control and bidirectional-override characters are removed, chat-platform
// markup is neutralised, and the fixed "automated message" header is built.
// Text is data: nothing here interprets it.
package sanitize

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Clean removes control characters (keeping \n and \t), normalises CR and CRLF
// to \n, and replaces invalid UTF-8 with U+FFFD. It also drops every invisible
// or direction-changing character an agent could use to hide or reorder
// content: bidirectional controls, the byte order mark, the line and paragraph
// separators (U+2028, U+2029), soft hyphen, word joiner, Mongolian vowel
// separator, zero-width space and non-joiner, interlinear annotation marks
// (U+FFF9 to U+FFFB) and the tag block (U+E0000 to U+E007F, which can carry a
// hidden ASCII message). The zero-width joiner is kept only between two emoji
// code points (optionally with a variation selector before it), so emoji
// sequences survive and everywhere else it is dropped. Flag sequences that
// rely on tag characters therefore degrade to a plain flag; that is accepted.
func Clean(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	rs := []rune(strings.ToValidUTF8(s, "�"))
	var prev, prevBase rune // last kept rune, and the last kept one that is not a variation selector
	keep := func(r rune) {
		b.WriteRune(r)
		prev = r
		if r != 0xFE0F {
			prevBase = r
		}
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\r':
			keep('\n')
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
		case r == '\n' || r == '\t':
			keep(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		case r == 0x200D:
			if isEmoji(prevBase) && (prev == prevBase || prev == 0xFE0F) && i+1 < len(rs) && isEmoji(rs[i+1]) {
				keep(r)
			}
		case isInvisible(r):
		default:
			keep(r)
		}
	}
	return b.String()
}

// isInvisible reports the characters Clean always removes (ZWJ is handled
// separately because it is sometimes kept).
func isInvisible(r rune) bool {
	switch {
	case r == 0x061C || r == 0x200E || r == 0x200F || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069): // bidi controls
		return true
	case r == 0xFEFF || r == 0x00AD || r == 0x2060 || r == 0x180E || r == 0x200B || r == 0x200C:
		return true
	case r == 0x2028 || r == 0x2029:
		return true
	case r >= 0xFFF9 && r <= 0xFFFB:
		return true
	case r >= 0xE0000 && r <= 0xE007F:
		return true
	}
	return false
}

// isEmoji is a coarse test for "this code point belongs to an emoji
// sequence": the pictograph planes, the dingbat and symbol blocks, and a few
// isolated emoji. It is deliberately generous; its only job is to decide
// whether a zero-width joiner sits inside an emoji sequence.
func isEmoji(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF, // mahjong, cards, enclosed, pictographs, emoticons, transport, supplemental
		r >= 0x2600 && r <= 0x27BF, // misc symbols and dingbats
		r >= 0x2300 && r <= 0x23FF, // misc technical (watch, hourglass, ...)
		r >= 0x25A0 && r <= 0x25FF, // geometric shapes
		r >= 0x2B00 && r <= 0x2BFF: // arrows and stars
		return true
	}
	switch r {
	case 0x00A9, 0x00AE, 0x203C, 0x2049, 0x2122, 0x2139, 0x3030, 0x303D, 0x3297, 0x3299:
		return true
	}
	return false
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

// NeutraliseHeader stops agent text from forging the header line. Any exact
// occurrence of header is rewritten, and so is every line that, once
// normalised, starts like a header (see fold): the same words in another case,
// with extra or exotic spaces, zero-width characters, combining marks,
// full-width letters, or common Cyrillic and Greek look-alikes, and any line
// that starts with "[automated" at all. Lines are split on every Unicode line
// break (including U+2028, U+2029 and NEL), not just "\n". A rewritten line
// loses its square brackets and gains the prefix "(quoted) ". The result is
// deterministic and idempotent.
//
// Confusable coverage is best effort: it handles the letters of the header
// label, not every homoglyph Unicode knows.
func NeutraliseHeader(text, header string) string {
	if header != "" && strings.Contains(text, header) {
		text = strings.ReplaceAll(text, header, "(quoted) "+header[1:len(header)-1])
	}
	var b strings.Builder
	b.Grow(len(text))
	start := 0
	flush := func(end int) {
		line := text[start:end]
		if forgedPrefix(line) {
			line = "(quoted) " + strings.Map(func(r rune) rune {
				if r == '[' || r == ']' || r == '［' || r == '］' {
					return -1
				}
				return r
			}, strings.TrimLeftFunc(line, func(r rune) bool { return foldRune(r) == 0 }))
		}
		b.WriteString(line)
	}
	for i, r := range text {
		if isLineBreak(r) {
			flush(i)
			b.WriteRune(r)
			start = i + utf8.RuneLen(r)
		}
	}
	flush(len(text))
	return b.String()
}

func isLineBreak(r rune) bool {
	return r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029
}

// headerPrefix is what a normalised forged line starts with.
var headerPrefix = fold("[automated")

func forgedPrefix(line string) bool { return strings.HasPrefix(fold(line), headerPrefix) }

// fold normalises s for comparison only (the result is never sent). It drops
// whitespace, control and format characters (zero-width characters, soft
// hyphen, bidi marks, variation selectors) and combining marks, maps
// full-width ASCII to ASCII, maps common Cyrillic, Greek and similar
// look-alikes of Latin letters to those letters, and folds case. This is a
// hand-rolled stand-in for NFKC plus a confusables skeleton (golang.org/x/text
// is not a dependency); it is best effort and covers the letters that occur in
// the header text.
func fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if f := foldRune(r); f != 0 {
			b.WriteRune(f)
		}
	}
	return b.String()
}

// foldRune returns the comparison form of r, or 0 if r is ignored.
func foldRune(r rune) rune {
	switch {
	case unicode.IsSpace(r), unicode.IsControl(r), unicode.In(r, unicode.Cf, unicode.Mn, unicode.Me, unicode.Zs, unicode.Zl, unicode.Zp):
		return 0
	case r == 0x180E, r == 0x115F, r == 0x1160, r == 0x3164, r == 0xFFA0:
		return 0
	case r >= 0xFF01 && r <= 0xFF5E: // full-width ASCII
		r -= 0xFEE0
	}
	if m, ok := confusables[r]; ok {
		r = m
	}
	// Canonical case: the smallest member of the rune's case-folding orbit
	// (so 'a', 'A' and U+017F all fold the same way).
	low := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < low {
			low = f
		}
	}
	return low
}

// confusables maps look-alikes of the Latin letters in "[Automated message
// from <label>]" (a d e f g m o r s t u) from Cyrillic, Greek, Armenian and a
// few Latin-extended forms. Letters are listed in both cases where they
// differ.
var confusables = map[rune]rune{
	// Cyrillic
	0x0430: 'a', 0x0410: 'A', 0x0435: 'e', 0x0415: 'E', 0x043E: 'o', 0x041E: 'O',
	0x0455: 's', 0x0405: 'S', 0x0501: 'd', 0x0500: 'D', 0x0442: 't', 0x0422: 'T',
	0x043C: 'm', 0x041C: 'M', 0x0433: 'r', 0x0446: 'u', 0x0475: 'v', 0x0261: 'g',
	0x0491: 'r', 0x04CF: 'l', 0x0456: 'i', 0x0406: 'I', 0x0440: 'p', 0x0420: 'P', 0x0441: 'c', 0x0421: 'C',
	0x0445: 'x', 0x0425: 'X', 0x0443: 'y', 0x04BB: 'h', 0x043D: 'h', 0x041D: 'H', 0x0432: 'b', 0x0412: 'B', 0x043A: 'k', 0x041A: 'K',
	// Greek
	0x03B1: 'a', 0x0391: 'A', 0x03B5: 'e', 0x0395: 'E', 0x03BF: 'o', 0x039F: 'O',
	0x03C4: 't', 0x03A4: 'T', 0x039C: 'M', 0x03C5: 'u', 0x03BD: 'v', 0x03C1: 'p', 0x03A1: 'P',
	0x03B9: 'i', 0x0399: 'I', 0x03BA: 'k', 0x039A: 'K', 0x03C7: 'x', 0x03A7: 'X', 0x0392: 'B', 0x0397: 'H',
	// Armenian and other
	0x057D: 'u', 0x0578: 'n', 0x0585: 'o', 0x0570: 'h',
	0x0131: 'i', 0x1D00: 'a', 0x1D0D: 'm', 0x1D1B: 't', 0x1D1C: 'u', 0x1D20: 'v',
}

// RuneCount is the number of characters (not bytes) in s.
func RuneCount(s string) int { return utf8.RuneCountInString(s) }
