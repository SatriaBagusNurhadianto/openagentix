package kb

import (
	"context"
	"sync"
	"time"

	"github.com/openagentix/openagentix/services/kb/internal/obs"
)

// GraphGaugeOptions tunes RegisterGraphGauges.
type GraphGaugeOptions struct {
	// TTL is how long a sample is reused; default 15s. A scrape inside the TTL
	// costs nothing.
	TTL time.Duration
	// PerCollection labels the gauges with the collection name instead of one
	// "all" series. Collection names are operator-chosen and unbounded, so this
	// is off by default; it is capped at MaxGaugeCollections (the rest are summed
	// under "other").
	PerCollection bool
	// Timeout bounds one sample (default 3s) so a slow database cannot hold a scrape.
	Timeout time.Duration
	Now     func() time.Time
}

// MaxGaugeCollections caps the per-collection series of the graph gauges.
const MaxGaugeCollections = 50

// RegisterGraphGauges makes m sample the graph size (entities, relations, fact
// sets, pending extraction jobs) when it is scraped, from the store counts the
// admin stats endpoint also reads. Without a graph it does nothing. A failed
// sample keeps the previous values.
func (s *Service) RegisterGraphGauges(m *obs.Metrics, o GraphGaugeOptions) {
	if m == nil || s.Graph == nil {
		return
	}
	if o.TTL <= 0 {
		o.TTL = 15 * time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	var (
		mu   sync.Mutex
		last time.Time
	)
	m.OnScrape(func() {
		mu.Lock()
		defer mu.Unlock()
		if !last.IsZero() && o.Now().Sub(last) < o.TTL {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
		defer cancel()
		type sizes struct{ ent, rel, pend, fs int }
		total := map[string]sizes{}
		cols, err := s.Collections(ctx)
		if err != nil {
			s.Log.Warn("graph gauge sample failed", "step", "list collections")
			return
		}
		for _, c := range cols {
			st, err := s.GraphStats(ctx, c.Name)
			if err != nil {
				s.Log.Warn("graph gauge sample failed", "step", "stats")
				return
			}
			key := "all"
			if o.PerCollection {
				key = c.Name
				if len(total) >= MaxGaugeCollections {
					if _, seen := total[key]; !seen {
						key = "other"
					}
				}
			}
			t := total[key]
			t.ent += st.Entities
			t.rel += st.Relations
			t.pend += st.JobsPending
			t.fs += st.FactSets
			total[key] = t
		}
		if len(total) == 0 {
			total["all"] = sizes{}
		}
		m.ResetGraphSize()
		for k, t := range total {
			m.SetGraphSize(k, t.ent, t.rel, t.pend, t.fs)
		}
		last = o.Now()
	})
}
