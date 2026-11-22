// Package chunk splits document text into overlapping, retrieval-sized pieces.
//
// Splitting prefers paragraph boundaries, then sentence boundaries, then word
// boundaries, and only cuts inside a word when a single word exceeds the limit.
// Sizes are counted in runes, so output is always valid UTF-8 and the result
// depends only on the input text and options.
package chunk

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Default sizes, in characters (runes).
const (
	DefaultMaxChars = 1200
	DefaultOverlap  = 150
)

// Options controls splitting. MaxChars <= 0 selects DefaultMaxChars. Overlap is
// the maximum number of characters carried from the end of one chunk to the
// start of the next; it is clamped below MaxChars.
type Options struct {
	MaxChars int
	Overlap  int
}

type unit struct {
	sep  string // separator that precedes the unit when packed after another
	text string
}

// Split returns the chunks of text in order. Blank input yields none.
func Split(text string, o Options) []string {
	max := o.MaxChars
	if max <= 0 {
		max = DefaultMaxChars
	}
	overlap := o.Overlap
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= max {
		overlap = max - 1
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var units []unit
	for _, para := range splitParagraphs(text) {
		first := true
		add := func(s string) {
			sep := " "
			if first {
				sep = "\n\n"
				first = false
			}
			units = append(units, unit{sep: sep, text: s})
		}
		if runeLen(para) <= max {
			add(para)
			continue
		}
		for _, sent := range splitSentences(para) {
			if runeLen(sent) <= max {
				add(sent)
				continue
			}
			for _, piece := range splitWords(sent, max) {
				add(piece)
			}
		}
	}
	return pack(units, max, overlap)
}

func pack(units []unit, max, overlap int) []string {
	var out []string
	cur := ""
	curLen := 0
	for _, u := range units {
		ul := runeLen(u.text)
		if cur == "" {
			cur, curLen = u.text, ul
			continue
		}
		if curLen+runeLen(u.sep)+ul <= max {
			cur += u.sep + u.text
			curLen += runeLen(u.sep) + ul
			continue
		}
		out = append(out, cur)
		next := u.text
		nextLen := ul
		if budget := min(overlap, max-1-ul); budget > 0 {
			if tail := overlapTail(cur, budget); tail != "" {
				next = tail + " " + u.text
				nextLen = runeLen(tail) + 1 + ul
			}
		}
		cur, curLen = next, nextLen
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// overlapTail returns up to budget trailing characters of s, starting on a word
// boundary. It returns "" if no whole word fits.
func overlapTail(s string, budget int) string {
	r := []rune(s)
	if budget >= len(r) {
		return ""
	}
	start := len(r) - budget
	if !unicode.IsSpace(r[start-1]) && !unicode.IsSpace(r[start]) {
		for start < len(r) && !unicode.IsSpace(r[start]) {
			start++
		}
	}
	return strings.TrimSpace(string(r[start:]))
}

func splitParagraphs(text string) []string {
	var out []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur = append(cur, strings.TrimRightFunc(line, unicode.IsSpace))
	}
	flush()
	return out
}

// splitSentences breaks after ., ! or ? (also the CJK full stop) that is
// followed by whitespace or end of text.
func splitSentences(p string) []string {
	var out []string
	r := []rune(p)
	start := 0
	for i := 0; i < len(r); i++ {
		end := r[i] == '.' || r[i] == '!' || r[i] == '?' || r[i] == '。'
		if end && (i+1 == len(r) || unicode.IsSpace(r[i+1])) {
			if s := strings.TrimSpace(string(r[start : i+1])); s != "" {
				out = append(out, s)
			}
			start = i + 1
		}
	}
	if s := strings.TrimSpace(string(r[start:])); s != "" {
		out = append(out, s)
	}
	return out
}

// splitWords packs words into pieces of at most max runes, cutting inside a
// word only when the word alone is longer than max.
func splitWords(s string, max int) []string {
	var out []string
	cur, curLen := "", 0
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur, curLen = "", 0
		}
	}
	for _, w := range strings.Fields(s) {
		wr := []rune(w)
		for len(wr) > max {
			flush()
			out = append(out, string(wr[:max]))
			wr = wr[max:]
		}
		w = string(wr)
		wl := len(wr)
		switch {
		case cur == "":
			cur, curLen = w, wl
		case curLen+1+wl <= max:
			cur += " " + w
			curLen += 1 + wl
		default:
			flush()
			cur, curLen = w, wl
		}
	}
	flush()
	return out
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }
