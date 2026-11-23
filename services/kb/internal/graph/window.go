// Package graph extracts a knowledge graph (entities and relations) from
// document text with an LLM reached through the model gateway, and stores it
// through store.Store. Extraction runs in a background worker, never in the
// ingest request.
package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/openagentix/openagentix/services/kb/internal/store"
)

const chunkSep = "\n\n"

// Window is a run of whole consecutive chunks that is extracted in one model
// call.
type Window struct {
	Idx        int
	FirstChunk int
	ChunkCount int
	Text       string // the chunks joined by a blank line
	Hash       string // sha256 of Text, hex
	starts     []int  // byte offset of each chunk in Text
	ends       []int  // byte offset just after each chunk
}

// Plan packs chunks into windows of at most windowChars characters. A chunk is
// never split: one longer than the limit gets a window of its own.
func Plan(chunks []string, windowChars int) []Window {
	var out []Window
	for i := 0; i < len(chunks); {
		size := utf8.RuneCountInString(chunks[i])
		j := i + 1
		for j < len(chunks) {
			add := utf8.RuneCountInString(chunks[j]) + len(chunkSep)
			if size+add > windowChars {
				break
			}
			size += add
			j++
		}
		out = append(out, newWindow(len(out), i, chunks[i:j]))
		i = j
	}
	return out
}

func newWindow(idx, first int, chunks []string) Window {
	w := Window{Idx: idx, FirstChunk: first, ChunkCount: len(chunks), Text: strings.Join(chunks, chunkSep)}
	off := 0
	for _, c := range chunks {
		w.starts = append(w.starts, off)
		w.ends = append(w.ends, off+len(c))
		off += len(c) + len(chunkSep)
	}
	sum := sha256.Sum256([]byte(w.Text))
	w.Hash = hex.EncodeToString(sum[:])
	return w
}

// Rebuild is Plan for one window whose position is already known (a claimed
// job): chunks[first : first+count].
func Rebuild(chunks []string, idx, first, count int) (Window, bool) {
	if first < 0 || count <= 0 || first+count > len(chunks) {
		return Window{}, false
	}
	return newWindow(idx, first, chunks[first:first+count]), true
}

// ChunkAt returns the document chunk index that contains the byte offset in
// Text. An offset inside the separator belongs to the chunk before it.
func (w Window) ChunkAt(offset int) int {
	k := 0
	for i, s := range w.starts {
		if s <= offset {
			k = i
		}
	}
	return w.FirstChunk + k
}

// Specs converts windows to the store's job specs.
func Specs(ws []Window) []store.JobSpec {
	out := make([]store.JobSpec, len(ws))
	for i, w := range ws {
		out[i] = store.JobSpec{WindowIdx: w.Idx, WindowHash: w.Hash, FirstChunk: w.FirstChunk, ChunkCount: w.ChunkCount}
	}
	return out
}
