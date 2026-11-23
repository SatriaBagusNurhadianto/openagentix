package store

import (
	"strings"
	"unicode"
)

// Entity resolution.
//
// Two mentions are the same entity when they have the same CanonicalForm: the
// match form of the name without category words at its ends. The type is not
// part of the key, because a model that sees "Ink" in one document and "the
// catalogue API Ink" in another will call it a service once and a system once.
// There is no fuzzy matching: "Mira Okafor" and "Mira Lindqvist", "Vane-2" and
// "Vane-3", "Quill" and "Quire" stay apart. The word lists below are the only
// language knowledge in the graph: English, lower case, in one place.

// categoryWords are words that say what kind of thing a name denotes and are
// dropped from the start and the end of a name when something else remains:
// "Team Basalt" and "Basalt", "cluster Teal" and "Teal". Justification: writers
// name a thing once and then refer to it with or without its category, and a
// model copies whichever form the sentence used. The entity types are always
// on the list; the other words are the categories infrastructure and
// organisation documents use most. Inner words are kept ("Ledger API Quill").
var categoryWords = wordSet(
	// the entity types (graph.EntityTypes)
	"service", "team", "person", "system", "location", "organization", "product", "event", "concept",
	// other common categories
	"cluster", "region", "api", "group", "squad", "department", "office",
	// articles
	"the", "a", "an",
)

// genericNouns are class nouns that name a kind of thing, not a thing: what a
// model emits for "a database", "the wiki", "a dashboard" when the text gives
// no proper name. A name made only of these (and category and role words) is
// not an entity: from different documents it would merge into one hub node that
// links unrelated facts. Deliberately small and about infrastructure and
// organisations; it is a safety net behind the extraction prompt, not a
// dictionary.
var genericNouns = wordSet(
	"database", "db", "server", "servers", "wiki", "dashboard", "dashboards", "queue", "cache", "storage", "layer",
	"provider", "vendor", "ticket", "tickets", "tool", "tools", "interface", "application", "app", "platform", "network",
	"outage", "incident", "incidents", "job", "jobs", "runner", "runners", "controller", "alert", "alerts",
	"notification", "notifications", "replica", "replicas", "standby", "uplink", "data", "user", "users", "customer",
	"customers", "company", "page", "pages", "document", "documents", "report", "reports", "log", "logs", "node", "nodes",
	"volume", "volumes", "disk", "disks", "host", "hosts", "machine", "endpoint", "channel", "bug", "backend", "frontend",
	// modifiers that only qualify a class noun
	"internal", "external", "main", "primary", "secondary", "dedicated", "shared", "central", "local", "global", "new",
	"old", "second", "first", "nightly", "daily", "weekly", "monthly", "backup", "maintenance", "object", "health", "build",
	"admin", "read", "write",
	// connectives
	"of", "for", "on", "in", "at", "to", "and", "or", "with", "our", "their", "its", "this", "that", "call", "oncall",
)

// roleWords describe what someone does, not who they are. A "person" called
// "on-call engineer" or "Granite team lead" denotes whoever holds the role, and
// nodes for roles merge strangers.
var roleWords = wordSet(
	"engineer", "engineers", "lead", "leads", "author", "authors", "officer", "officers", "manager", "managers",
	"owner", "owners", "contact", "contacts", "member", "members", "administrator", "administrators", "developer",
	"developers", "operator", "operators", "duty", "staff", "responder", "responders", "oncall",
)

func wordSet(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[w] = true
	}
	return m
}

func isGenericToken(t string) bool { return categoryWords[t] || genericNouns[t] || roleWords[t] }

// CanonicalForm is the match form of a name without category words at its start
// or end, as long as at least one other word remains.
func CanonicalForm(name string) string {
	ts := strings.Fields(MatchForm(name))
	for len(ts) > 1 {
		switch {
		case categoryWords[ts[0]]:
			ts = ts[1:]
		case categoryWords[ts[len(ts)-1]]:
			ts = ts[:len(ts)-1]
		default:
			return strings.Join(ts, " ")
		}
	}
	return strings.Join(ts, " ")
}

// IsGenericOnly reports whether every word of a name is a category, generic or
// role word, so that the name says what kind of thing it is but not which one.
func IsGenericOnly(name string) bool {
	ts := strings.Fields(MatchForm(name))
	if len(ts) == 0 {
		return false
	}
	for _, t := range ts {
		if !isGenericToken(t) {
			return false
		}
	}
	return true
}

// HasRoleWord reports whether a name contains a role word or "on call".
func HasRoleWord(name string) bool {
	m := MatchForm(name)
	if containsWord(m, "on call") {
		return true
	}
	for _, t := range strings.Fields(m) {
		if roleWords[t] {
			return true
		}
	}
	return false
}

// HasDigit reports whether s has a digit: a date, a version or an identifier.
func HasDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// MergeKey is what an entity is merged on within its collection: its canonical
// form, whatever its type. The exception is an event whose name has no digit
// ("disk outage"): that name does not identify one occurrence, so such an event
// is scoped to the document that mentions it and two documents never share it.
// An event with a date or identifier in its name ("power outage on 3 March")
// merges across documents like any other entity.
func MergeKey(name, typ, documentID string) string {
	k := CanonicalForm(name)
	if typ == "event" && documentID != "" && !HasDigit(k) {
		return k + "\x1f" + documentID
	}
	return k
}

// BetterName reports whether cand should replace cur as the displayed name of
// an entity whose names share one canonical form: the form with more words is
// the more specific ("Team Basalt" over "Basalt"); with as many, the one that
// sorts first byte-wise, so that the result does not depend on arrival order.
func BetterName(cur, cand string) bool {
	nc, nn := len(strings.Fields(MatchForm(cur))), len(strings.Fields(MatchForm(cand)))
	if nn != nc {
		return nn > nc
	}
	return cand < cur
}
