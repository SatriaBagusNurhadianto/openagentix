package graph

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openagentix/openagentix/services/kb/internal/store"
)

// EntityTypes is the closed list of entity types. Anything else is dropped.
var EntityTypes = []string{"service", "team", "person", "system", "location", "organization", "product", "event", "concept"}

// Bounds on what one extraction may contain.
const (
	MaxNameRunes        = 80
	MaxDescriptionRunes = 200
	MaxAliases          = 5
	MaxPredicateRunes   = 40 // after normalisation
	maxRawPredicateLen  = 60 // before normalisation
	MinEvidenceRunes    = 8
	MaxEvidenceRunes    = 300
)

// Error codes. They are the only description of a failure that is logged or
// stored: never document text, prompts or model output.
const (
	CodeTransport   = "transport"
	CodeHTTP429     = "http_429"
	CodeHTTP5xx     = "http_5xx"
	CodeHTTP4xx     = "http_4xx"
	CodeMalformed   = "malformed_output"
	CodeEmptyOutput = "empty_output"
	CodeStaleWindow = "stale_window"
	CodeStore       = "store"
	CodeTooLarge    = "response_too_large"
)

// Error is a failed extraction attempt.
type Error struct {
	Code       string
	Retryable  bool
	RetryAfter time.Duration // server hint for rate limits; 0 if none
}

func (e *Error) Error() string { return "extraction failed: " + e.Code }

// Limits are the per-window caps.
type Limits struct {
	MaxEntities  int
	MaxRelations int
}

// Extraction is a validated result. Every entity and relation in it satisfies
// the rules in Validate.
type Extraction struct {
	Entities  []store.ExtractedEntity
	Relations []store.ExtractedRelation
	Stats     ValidateStats
}

// ValidateStats counts what Validate threw away.
type ValidateStats struct {
	EntitiesIn, EntitiesDropped   int
	RelationsIn, RelationsDropped int
}

type rawEntity struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Aliases     []string `json:"aliases"`
}

type rawRelation struct {
	Source    string `json:"source"`
	Predicate string `json:"predicate"`
	Target    string `json:"target"`
	Evidence  string `json:"evidence"`
}

type rawOutput struct {
	Entities  []json.RawMessage `json:"entities"`
	Relations []json.RawMessage `json:"relations"`
}

func malformed() *Error { return &Error{Code: CodeMalformed, Retryable: true} }

// Validate parses a model's JSON answer for window w and keeps only what is
// well-formed, grounded in the window text and within the caps. The model is
// not trusted: a top-level shape error fails the attempt, every item that
// breaks a rule is dropped on its own.
//
// Rules: an entity has a type from EntityTypes and a trimmed name of at most 80
// characters that occurs in the window (case-insensitively), as does each
// alias. A relation names two different entities of the same result, has a
// predicate that normalises to a snake_case token of 2 to 40 characters, and
// carries evidence that occurs literally in the window.
func Validate(output []byte, w Window, lim Limits) (Extraction, ValidateStats, error) {
	var st ValidateStats
	var raw rawOutput
	body := unfence(bytes.TrimSpace(output))
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&raw); err != nil || dec.More() {
		return Extraction{}, st, malformed()
	}
	if raw.Entities == nil && raw.Relations == nil && !bytes.HasPrefix(body, []byte("{")) {
		return Extraction{}, st, malformed()
	}
	lowText := strings.ToLower(w.Text)
	grounded := func(s string) bool { return strings.Contains(lowText, strings.ToLower(s)) }

	var ex Extraction
	byName := map[string]int{} // normalised name or alias -> entity index, -1 if ambiguous
	byKey := map[string]int{}  // type + normalised name -> entity index
	st.EntitiesIn = len(raw.Entities)
	for _, item := range raw.Entities {
		var re rawEntity
		if json.Unmarshal(item, &re) != nil {
			continue
		}
		name, ok := cleanName(re.Name)
		typ := strings.ToLower(strings.TrimSpace(re.Type))
		if !ok || !isEntityType(typ) || !grounded(name) {
			continue
		}
		var aliases []string
		for _, a := range re.Aliases {
			if len(aliases) == MaxAliases {
				break
			}
			if a, ok := cleanName(a); ok && grounded(a) {
				aliases = append(aliases, a)
			}
		}
		key := typ + "\x00" + store.NormalizeName(name)
		if i, dup := byKey[key]; dup {
			ex.Entities[i].Aliases = appendUnique(ex.Entities[i].Aliases, aliases, MaxAliases)
			continue
		}
		if len(ex.Entities) >= lim.MaxEntities {
			continue
		}
		byKey[key] = len(ex.Entities)
		ex.Entities = append(ex.Entities, store.ExtractedEntity{
			Name: name, Type: typ, Description: cleanDescription(re.Description), Aliases: aliases, Chunks: chunksMentioning(w, append([]string{name}, aliases...)),
		})
	}
	for i, e := range ex.Entities {
		for _, n := range append([]string{e.Name}, e.Aliases...) {
			k := store.NormalizeName(n)
			if j, seen := byName[k]; seen && j != i {
				byName[k] = -1
			} else {
				byName[k] = i
			}
		}
	}
	st.EntitiesDropped = st.EntitiesIn - len(ex.Entities)

	seen := map[[3]string]bool{}
	st.RelationsIn = len(raw.Relations)
	for _, item := range raw.Relations {
		var rr rawRelation
		if json.Unmarshal(item, &rr) != nil || len(ex.Relations) >= lim.MaxRelations {
			continue
		}
		src, ok1 := byName[store.NormalizeName(rr.Source)]
		dst, ok2 := byName[store.NormalizeName(rr.Target)]
		if !ok1 || !ok2 || src < 0 || dst < 0 || src == dst {
			continue
		}
		pred := normalizePredicate(rr.Predicate)
		evidence := strings.TrimSpace(rr.Evidence)
		n := utf8.RuneCountInString(evidence)
		if pred == "" || n < MinEvidenceRunes || n > MaxEvidenceRunes || !utf8.ValidString(evidence) {
			continue
		}
		off := strings.Index(w.Text, evidence)
		if off < 0 {
			continue
		}
		k := [3]string{ex.Entities[src].Name + "\x00" + ex.Entities[src].Type, pred, ex.Entities[dst].Name + "\x00" + ex.Entities[dst].Type}
		if seen[k] {
			continue
		}
		seen[k] = true
		ex.Relations = append(ex.Relations, store.ExtractedRelation{Src: src, Dst: dst, Predicate: pred, Evidence: evidence, ChunkIndex: w.ChunkAt(off)})
	}
	st.RelationsDropped = st.RelationsIn - len(ex.Relations)
	ex.Stats = st
	return ex, st, nil
}

// unfence removes one surrounding ``` or ```json fence, which some models add
// even in JSON mode.
func unfence(b []byte) []byte {
	if !bytes.HasPrefix(b, []byte("```")) {
		return b
	}
	b = bytes.TrimPrefix(b, []byte("```"))
	b = bytes.TrimPrefix(b, []byte("json"))
	b = bytes.TrimSuffix(bytes.TrimSpace(b), []byte("```"))
	return bytes.TrimSpace(b)
}

func isEntityType(t string) bool {
	for _, x := range EntityTypes {
		if t == x {
			return true
		}
	}
	return false
}

// cleanName trims and collapses whitespace and reports whether the result is a
// usable name: valid UTF-8, no control characters, 1 to 80 characters.
func cleanName(s string) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return "", false
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if n := utf8.RuneCountInString(s); n == 0 || n > MaxNameRunes {
		return "", false
	}
	return s, true
}

func cleanDescription(s string) string {
	if !utf8.ValidString(s) {
		return ""
	}
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if r := []rune(s); len(r) > MaxDescriptionRunes {
		s = strings.TrimSpace(string(r[:MaxDescriptionRunes]))
	}
	return s
}

func appendUnique(dst, add []string, limit int) []string {
	for _, a := range add {
		if len(dst) >= limit {
			break
		}
		dup := false
		for _, d := range dst {
			dup = dup || strings.EqualFold(d, a)
		}
		if !dup {
			dst = append(dst, a)
		}
	}
	return dst
}

// normalizePredicate turns a model's predicate into a snake_case token of 2 to
// 40 characters starting with a letter, or "" if it cannot be one: the raw value
// must be printable ASCII of bounded length.
func normalizePredicate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxRawPredicateLen {
		return ""
	}
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r > unicode.MaxASCII || unicode.IsControl(r):
			return ""
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if sep && b.Len() > 0 {
				b.WriteByte('_')
			}
			sep = false
			b.WriteRune(r)
		default:
			sep = true
		}
	}
	out := b.String()
	if len(out) < 2 || len(out) > MaxPredicateRunes || out[0] < 'a' || out[0] > 'z' {
		return ""
	}
	return out
}

// chunksMentioning returns the document chunk indexes of w whose text contains
// any of the names (case-insensitively); the first chunk if none does on its
// own (a name that straddles the join of two chunks).
func chunksMentioning(w Window, names []string) []int {
	var out []int
	for i := range w.starts {
		part := strings.ToLower(w.Text[w.starts[i]:w.ends[i]])
		for _, n := range names {
			if strings.Contains(part, strings.ToLower(n)) {
				out = append(out, w.FirstChunk+i)
				break
			}
		}
	}
	if len(out) == 0 {
		out = []int{w.FirstChunk}
	}
	return out
}
