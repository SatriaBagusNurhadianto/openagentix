// Package toolhive is the Tools adapter. In this version it is catalog-backed: the chart renders the profiles and the
// names of the tool servers ToolHive serves, and this adapter only looks them up. It writes nothing.
package toolhive

import (
	"slices"

	"github.com/openagentix/openagentix/services/control-plane/internal/catalog"
)

type Adapter struct{ cat *catalog.Catalog }

// New returns the adapter over a loaded catalog.
func New(c *catalog.Catalog) *Adapter { return &Adapter{cat: c} }

// Profile resolves a profile to its tool server and the tool names it offers (a copy).
func (a *Adapter) Profile(name string) (catalog.Profile, bool) {
	p, ok := a.cat.Profile(name)
	p.Tools = slices.Clone(p.Tools)
	return p, ok
}

func (a *Adapter) Profiles() []string { return a.cat.ProfileNames() }

// SharedServer is the tool server of agents without a profile.
func (a *Adapter) SharedServer() string { return a.cat.ToolServer }
