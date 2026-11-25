package toolhive

import (
	"testing"

	"github.com/openagentix/openagentix/services/control-plane/internal/catalog"
)

func TestResolvesProfilesFromCatalog(t *testing.T) {
	cat, err := catalog.LoadCatalog("../../../testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var r catalog.ToolResolver = New(cat)
	p, ok := r.Profile("lookup")
	if !ok || p.ToolServer != "toolhive-vmcp-lookup" || len(p.Tools) != 4 {
		t.Fatalf("%+v %v", p, ok)
	}
	if _, ok := r.Profile("nope"); ok {
		t.Fatal("unknown profile must not resolve")
	}
	if r.SharedServer() != "toolhive-vmcp" {
		t.Fatal(r.SharedServer())
	}
	if got := r.Profiles(); len(got) != 1 || got[0] != "lookup" {
		t.Fatal(got)
	}
}

func TestProfileIsACopy(t *testing.T) {
	cat := &catalog.Catalog{Profiles: []catalog.Profile{{Name: "a", Tools: []string{"x"}}}}
	p, _ := New(cat).Profile("a")
	p.Tools[0] = "changed"
	if cat.Profiles[0].Tools[0] != "x" {
		t.Fatal("caller can change the catalog")
	}
}
