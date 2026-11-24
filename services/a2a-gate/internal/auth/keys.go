package auth

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DirKeys reads key hashes from a directory (a mounted Secret): one file per caller, named like the caller,
// holding the SHA-256 of the key as 64 hex characters. The directory is read again at most every TTL.
type DirKeys struct {
	Dir string
	TTL time.Duration
	Now func() time.Time
	// OnProblem, if set, is told about a file that cannot be used (unreadable, or not 64 hex characters), once per
	// file until the problem changes. It never receives file content.
	OnProblem func(name, why string)

	mu     sync.Mutex
	loaded time.Time
	cache  map[string]string
	probs  map[string]string
}

// Hashes returns caller name -> hash. A missing directory or an unreadable file gives fewer entries, never an error:
// the gate then refuses the key, which is the safe direction.
func (d *DirKeys) Hashes() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	if d.cache != nil && now().Sub(d.loaded) < d.TTL {
		return d.cache
	}
	out := map[string]string{}
	probs := map[string]string{}
	entries, _ := os.ReadDir(d.Dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // the Secret volume's ..data links
		}
		b, err := os.ReadFile(filepath.Join(d.Dir, e.Name()))
		if err != nil {
			probs[e.Name()] = "cannot read the file"
			continue
		}
		h := strings.ToLower(strings.TrimSpace(string(b)))
		if len(h) == 64 && isHex(h) {
			out[e.Name()] = h
		} else {
			probs[e.Name()] = "the content is not a SHA-256 in hex"
		}
	}
	if d.OnProblem != nil {
		for n, why := range probs {
			if d.probs[n] != why {
				d.OnProblem(n, why)
			}
		}
	}
	d.probs = probs
	d.cache, d.loaded = out, now()
	return out
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
