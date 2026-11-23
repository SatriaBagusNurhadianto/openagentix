package store

import (
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrGraphDisabled is returned by graph methods of a store opened without
	// graph support.
	ErrGraphDisabled = errors.New("knowledge graph is not enabled on this store")
	// ErrJobGone is returned when an extraction result is applied to a job that
	// no longer exists or is no longer pending (its document was replaced or
	// deleted, or another worker already completed it).
	ErrJobGone = errors.New("extraction job is gone or already finished")
)

// Graph limits.
const (
	// MaxHops is the longest traversal any graph query accepts.
	MaxHops = 3
	// DefaultPathLimit and MaxPathLimit bound the number of paths returned.
	DefaultPathLimit = 50
	MaxPathLimit     = 500
	// DefaultFanOut and MaxFanOut bound how many edges a traversal follows out
	// of one entity, so a hub cannot blow a query up.
	DefaultFanOut = 25
	MaxFanOut     = 100

	OriginExtracted = "extracted"
)

// Job states.
const (
	JobPending = "pending"
	JobDone    = "done"
	JobFailed  = "failed"
)

// NormalizeName is the merge key of an entity name: lower case, surrounding
// space trimmed, inner whitespace runs collapsed to one space.
func NormalizeName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// Entity is a node of the knowledge graph. Entities are never shared across
// collections. Aliases are stored in MatchForm.
type Entity struct {
	ID          int64
	Collection  string
	Name        string
	NormName    string
	Type        string
	Description string
	Aliases     []string
	Origin      string
}

// EntityInput creates or merges an entity. The merge key is
// (Collection, Type, NormalizeName(Name)). On merge the existing name and
// origin win, an empty description is filled, aliases are united and a missing
// name embedding is set.
type EntityInput struct {
	Collection    string
	Name          string
	Type          string
	Description   string
	Aliases       []string
	Origin        string // default OriginExtracted
	NameEmbedding []float32
}

// Relation is a directed, typed edge with the evidence it was extracted from.
type Relation struct {
	ID            int64
	Collection    string
	SrcID, DstID  int64
	Predicate     string
	Evidence      string
	DocumentID    string
	ChunkIndex    int
	PromptVersion string
	Model         string
	Origin        string // default OriginExtracted
}

// EntityChunk records that an entity is mentioned in one chunk.
type EntityChunk struct {
	EntityID   int64
	DocumentID string
	ChunkIndex int
}

// Citation points at the chunk an edge was derived from.
type Citation struct {
	DocumentID string
	ChunkIndex int
	SourceURI  string
}

// Step is one traversed edge. Forward is true when it was walked from Edge.SrcID
// to Edge.DstID, false when walked against its direction.
type Step struct {
	Edge    Relation
	Cite    Citation
	Forward bool
}

// Path is a walk without repeated entities: len(Entities) == len(Steps)+1.
type Path struct {
	Entities []Entity
	Steps    []Step
}

// ExpandQuery asks for every simple path of 1..Hops edges (edges are followed in
// both directions) that starts at one of SeedIDs.
type ExpandQuery struct {
	Collection string
	SeedIDs    []int64
	Hops       int // 1..MaxHops; larger values are clamped, <= 0 means 1
	Limit      int // max paths; <= 0 means DefaultPathLimit, clamped to MaxPathLimit
	FanOut     int // edges followed per entity, lowest edge ids first; <= 0 means DefaultFanOut, clamped to MaxFanOut
}

// PathQuery asks for the shortest paths between two entities.
type PathQuery struct {
	Collection string
	FromID     int64
	ToID       int64
	MaxHops    int // 1..MaxHops, as ExpandQuery.Hops
	Limit      int
	FanOut     int // as ExpandQuery.FanOut
}

// EntityHit is a seed candidate. Higher Score is better.
type EntityHit struct {
	Entity Entity
	Score  float64
}

// JobSpec is one planned extraction window of a document: chunks
// [FirstChunk, FirstChunk+ChunkCount) whose joined text hashes to WindowHash.
type JobSpec struct {
	WindowIdx  int
	WindowHash string
	FirstChunk int
	ChunkCount int
}

// JobKey identifies a job.
type JobKey struct {
	Collection string
	DocumentID string
	WindowIdx  int
}

// Job is an extraction job.
type Job struct {
	JobKey
	WindowHash    string
	FirstChunk    int
	ChunkCount    int
	PromptVersion string
	State         string
	Attempts      int
	LastErrorCode string
	UpdatedAt     time.Time
}

// ExtractedEntity is one validated entity of an extraction result. Chunks lists
// the chunk indexes that mention it.
type ExtractedEntity struct {
	Name          string
	Type          string
	Description   string
	Aliases       []string
	NameEmbedding []float32
	Chunks        []int
}

// ExtractedRelation refers to entities by index into ExtractionResult.Entities.
type ExtractedRelation struct {
	Src, Dst   int
	Predicate  string
	Evidence   string
	ChunkIndex int
}

// ExtractionResult is everything one successful job produced. ApplyExtraction
// stores it and completes the job in one transaction.
type ExtractionResult struct {
	Job           JobKey
	Model         string
	PromptVersion string
	Entities      []ExtractedEntity
	Relations     []ExtractedRelation
}

// GraphStats counts graph rows.
type GraphStats struct {
	Entities, Relations, Mentions     int
	JobsPending, JobsDone, JobsFailed int
	// LastErrorCode is the error code of the most recently updated job of the
	// collection that has one (never a message), or empty.
	LastErrorCode string
}

// ClampHops maps a requested hop count into 1..MaxHops.
func ClampHops(h int) int {
	switch {
	case h <= 0:
		return 1
	case h > MaxHops:
		return MaxHops
	}
	return h
}

// ClampFanOut maps a requested per-entity edge cap into 1..MaxFanOut.
func ClampFanOut(f int) int {
	switch {
	case f <= 0:
		return DefaultFanOut
	case f > MaxFanOut:
		return MaxFanOut
	}
	return f
}

// ClampLimit maps a requested path limit into 1..MaxPathLimit.
func ClampLimit(l int) int {
	switch {
	case l <= 0:
		return DefaultPathLimit
	case l > MaxPathLimit:
		return MaxPathLimit
	}
	return l
}

// MatchForm is the form names are compared in by FindEntitiesByName: lower
// case with every run of non-alphanumeric characters turned into one space.
func MatchForm(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSuffix(b.String(), " ")
}

// MatchAliases returns the aliases of an entity in match form: sorted, unique,
// without empty ones and without the entity's own match name.
func MatchAliases(name string, aliases []string) []string {
	own := MatchForm(name)
	seen := map[string]bool{own: true, "": true}
	var out []string
	for _, a := range aliases {
		m := MatchForm(a)
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

func containsWord(hay, needle string) bool {
	return strings.Contains(" "+hay+" ", " "+needle+" ")
}

// NameScore rates how well an entity (match-form name and aliases) fits a
// match-form query: 1 for an equal name, 0.95 an equal alias, then up to 0.9
// for an entity named inside the query, then up to 0.6 for a query that is part
// of a name; 0 for no match. Names shorter than two characters never match by
// containment. The Postgres store computes the same score in SQL.
func NameScore(q, name string, aliases []string) float64 {
	if len([]rune(q)) < 2 {
		return 0
	}
	best := 0.0
	rate := func(s string, equal, inQuery, queryIn float64) {
		n := float64(utf8.RuneCountInString(s))
		ql := float64(utf8.RuneCountInString(q))
		switch {
		case s == q:
			best = max(best, equal)
		case n >= 2 && containsWord(q, s):
			best = max(best, inQuery+0.2*n/ql)
		case containsWord(s, q):
			best = max(best, queryIn+0.1*ql/n)
		}
	}
	rate(name, 1, 0.7, 0.5)
	for _, a := range aliases {
		rate(a, 0.95, 0.65, 0.45)
	}
	return best
}

// SortPaths orders paths by length, then by entity ids, then by edge ids.
func SortPaths(ps []Path) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if len(a.Steps) != len(b.Steps) {
			return len(a.Steps) < len(b.Steps)
		}
		for k := range a.Entities {
			if a.Entities[k].ID != b.Entities[k].ID {
				return a.Entities[k].ID < b.Entities[k].ID
			}
		}
		for k := range a.Steps {
			if a.Steps[k].Edge.ID != b.Steps[k].Edge.ID {
				return a.Steps[k].Edge.ID < b.Steps[k].Edge.ID
			}
		}
		return false
	})
}
