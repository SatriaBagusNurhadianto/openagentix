package store

import "testing"

func TestCanonicalForm(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Basalt", "basalt"},
		{"Team Basalt", "basalt"},
		{"team  BASALT", "basalt"},
		{"Basalt team", "basalt"},
		{"cluster Teal", "teal"},
		{"Cluster Teal", "teal"},
		{"Teal", "teal"},
		{"the Teal cluster", "teal"},
		{"Ledger API Quill", "ledger api quill"}, // an inner category word is part of the name
		{"Quill API", "quill"},
		{"Lisbon office", "lisbon"},
		{"Vane-3", "vane 3"},
		{"region Vane-3", "vane 3"},
		{"Mira Okafor", "mira okafor"},
		{"Mira Lindqvist", "mira lindqvist"},
		{"Team", "team"},               // nothing else remains: kept
		{"Cluster Service", "service"}, // strips down to one word, never to nothing
		{"Payments service team", "payments"},
		{"  ", ""},
	} {
		if got := CanonicalForm(c.in); got != c.want {
			t.Errorf("CanonicalForm(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCanonicalFormKeepsDistinctNamesDistinct(t *testing.T) {
	for _, pair := range [][2]string{
		{"Mira Okafor", "Mira Lindqvist"},
		{"Vane-1", "Vane-2"}, {"Vane-2", "Vane-3"}, {"Vane-1", "Vane-3"},
		{"Quill", "Quire"},
		{"Team Basalt", "Team Granite"},
	} {
		if CanonicalForm(pair[0]) == CanonicalForm(pair[1]) {
			t.Errorf("%q and %q must not share a canonical form", pair[0], pair[1])
		}
	}
}

func TestCategoryWords(t *testing.T) {
	for w := range categoryWords {
		if w != MatchForm(w) || w == "" {
			t.Errorf("category word %q is not in match form", w)
		}
	}
	for _, w := range []string{"service", "team", "person", "system", "location", "organization", "product", "event", "concept",
		"cluster", "region", "api", "group", "squad", "department", "office"} {
		if !categoryWords[w] {
			t.Errorf("%q missing from the category words", w)
		}
	}
}

func TestIsGenericOnly(t *testing.T) {
	for _, n := range []string{"database", "the wiki", "Wiki", "dashboard", "provider", "on-call engineer", "internal wiki", "product database",
		"storage layer", "maintenance ticket", "Team", "cluster", "the service", "second uplink", "dedicated database", "queue"} {
		if !IsGenericOnly(n) {
			t.Errorf("IsGenericOnly(%q) = false", n)
		}
	}
	for _, n := range []string{"Basalt", "Team Basalt", "Orion", "Vane-3", "power outage on 3 March", "Mira Okafor", "Wiki 2", "database Orion", "Postgres database"} {
		if IsGenericOnly(n) {
			t.Errorf("IsGenericOnly(%q) = true", n)
		}
	}
}

func TestHasRoleWord(t *testing.T) {
	for _, n := range []string{"on-call engineer", "Granite team lead", "Pylon author", "finance engineering duty officer", "release manager"} {
		if !HasRoleWord(n) {
			t.Errorf("HasRoleWord(%q) = false", n)
		}
	}
	for _, n := range []string{"Mira Okafor", "Aiko Brandt", "Dev Okonkwo"} {
		if HasRoleWord(n) {
			t.Errorf("HasRoleWord(%q) = true", n)
		}
	}
}

func TestBetterName(t *testing.T) {
	for _, c := range []struct {
		cur, cand string
		want      bool
	}{
		{"Basalt", "Team Basalt", true}, // more words wins
		{"Team Basalt", "Basalt", false},
		{"cluster Teal", "Cluster Teal", true}, // same number of words: the smaller byte string wins, "C" < "c"
		{"Cluster Teal", "cluster Teal", false},
		{"Teal", "Teal", false},
	} {
		if got := BetterName(c.cur, c.cand); got != c.want {
			t.Errorf("BetterName(%q, %q) = %v, want %v", c.cur, c.cand, got, c.want)
		}
	}
}

func TestMergeKeyScopesEventsWithoutADate(t *testing.T) {
	if MergeKey("disk outage", "event", "doc1") == MergeKey("disk outage", "event", "doc2") {
		t.Error("an event with a generic name must be scoped to its document")
	}
	if MergeKey("disk outage", "event", "doc1") != MergeKey("Disk Outage", "event", "doc1") {
		t.Error("the same event of the same document must merge")
	}
	if MergeKey("Power outage on 3 March", "event", "doc1") != MergeKey("power outage on 3 march", "event", "doc2") {
		t.Error("an event whose name has a date must merge across documents")
	}
	if MergeKey("Vane-3 outage", "event", "doc1") != MergeKey("Vane-3 outage", "event", "doc2") {
		t.Error("an identifier with a digit counts as a date")
	}
	if MergeKey("Basalt", "team", "doc1") != MergeKey("Team Basalt", "service", "doc2") {
		t.Error("other types are never scoped and the type is not part of the key")
	}
	if MergeKey("disk outage", "event", "") != "disk outage" {
		t.Error("without a document there is nothing to scope by")
	}
}
