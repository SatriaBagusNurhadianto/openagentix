package rules

import (
	"strings"
	"testing"
)

const sample = `{"callers":[
 {"name":"bridge","serviceAccount":{"namespace":"openagentix-gateway","name":"telegram-bridge"},"agents":["kagent/assistant"],"userId":{"prefix":"telegram:"}},
 {"name":"schedule-x","serviceAccount":{"namespace":"openagentix-schedules","name":"schedule-x"},"agents":["kagent/assistant"],"userId":{"exact":"schedule:x"}},
 {"name":"kourier-b","key":true,"agents":["kagent/assistant"],"userId":{"fixed":"kourier:b"}}
]}`

func TestParseOK(t *testing.T) {
	r, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Callers) != 3 {
		t.Fatalf("callers = %d", len(r.Callers))
	}
	if c := r.ByServiceAccount("openagentix-gateway", "telegram-bridge"); c == nil || c.Name != "bridge" {
		t.Fatalf("lookup by service account: %+v", c)
	}
	if r.ByServiceAccount("openagentix-gateway", "other") != nil {
		t.Fatal("unknown service account must not match")
	}
	if c := r.ByName("kourier-b"); c == nil || !c.Key {
		t.Fatalf("lookup by name: %+v", c)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"empty":                ``,
		"not json":             `callers: []`,
		"unknown field":        `{"callers":[],"extra":1}`,
		"unknown caller field": `{"callers":[{"name":"a","key":true,"agents":["k/a"],"userId":{"fixed":"x"},"admin":true}]}`,
		"no name":              `{"callers":[{"key":true,"agents":["k/a"],"userId":{"fixed":"x"}}]}`,
		"dup name":             `{"callers":[{"name":"a","key":true,"agents":["k/a"],"userId":{"fixed":"x"}},{"name":"a","key":true,"agents":["k/a"],"userId":{"fixed":"y"}}]}`,
		"no auth":              `{"callers":[{"name":"a","agents":["k/a"],"userId":{"fixed":"x"}}]}`,
		"both auth":            `{"callers":[{"name":"a","key":true,"serviceAccount":{"namespace":"n","name":"s"},"agents":["k/a"],"userId":{"fixed":"x"}}]}`,
		"no agents":            `{"callers":[{"name":"a","key":true,"agents":[],"userId":{"fixed":"x"}}]}`,
		"bad agent":            `{"callers":[{"name":"a","key":true,"agents":["assistant"],"userId":{"fixed":"x"}}]}`,
		"no user rule":         `{"callers":[{"name":"a","key":true,"agents":["k/a"],"userId":{}}]}`,
		"two user rules":       `{"callers":[{"name":"a","key":true,"agents":["k/a"],"userId":{"exact":"x","prefix":"y"}}]}`,
		"same sa twice":        `{"callers":[{"name":"a","serviceAccount":{"namespace":"n","name":"s"},"agents":["k/a"],"userId":{"fixed":"x"}},{"name":"b","serviceAccount":{"namespace":"n","name":"s"},"agents":["k/a"],"userId":{"fixed":"y"}}]}`,
		"name with space":      `{"callers":[{"name":"a b","key":true,"agents":["k/a"],"userId":{"fixed":"x"}}]}`,
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestAllowsAgent(t *testing.T) {
	r, _ := Parse([]byte(sample))
	c := r.ByName("bridge")
	if !c.AllowsAgent("kagent", "assistant") {
		t.Error("assistant must be allowed")
	}
	if c.AllowsAgent("kagent", "other") || c.AllowsAgent("other", "assistant") {
		t.Error("other agents must be refused")
	}
}

func TestResolveUser(t *testing.T) {
	r, _ := Parse([]byte(sample))
	bridge, sched, kourier := r.ByName("bridge"), r.ByName("schedule-x"), r.ByName("kourier-b")
	long := strings.Repeat("a", 300)
	tests := []struct {
		name   string
		c      *Caller
		in     []string
		want   string
		wantOK bool
	}{
		{"prefix ok", bridge, []string{"telegram:123"}, "telegram:123", true},
		{"prefix wrong", bridge, []string{"admin@kagent.dev"}, "", false},
		{"prefix only", bridge, []string{"telegram:"}, "", false},
		{"prefix absent", bridge, nil, "", false},
		{"prefix two headers", bridge, []string{"telegram:1", "telegram:2"}, "", false},
		{"prefix too long", bridge, []string{"telegram:" + long}, "", false},
		{"prefix control char", bridge, []string{"telegram:1\x00"}, "", false},
		{"exact ok", sched, []string{"schedule:x"}, "schedule:x", true},
		{"exact other", sched, []string{"schedule:y"}, "", false},
		{"exact absent is filled in", sched, nil, "schedule:x", true},
		{"fixed overwrites", kourier, []string{"anyone"}, "kourier:b", true},
		{"fixed absent", kourier, nil, "kourier:b", true},
		{"fixed two headers", kourier, []string{"a", "b"}, "kourier:b", true},
	}
	for _, tc := range tests {
		got, ok := tc.c.ResolveUser(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestMethodsDefaultAndExplicit(t *testing.T) {
	r, err := Parse([]byte(`{"callers":[
 {"name":"a","key":true,"agents":["k/a"],"userId":{"fixed":"x"}},
 {"name":"b","key":true,"agents":["k/a"],"userId":{"fixed":"x"},"methods":["SendMessage","GetTask"]}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	a, b := r.ByName("a"), r.ByName("b")
	for _, m := range DefaultMethods {
		if !a.AllowsMethod(m) {
			t.Errorf("the default list must allow %s", m)
		}
	}
	for _, m := range []string{"GetTask", "CancelTask", "ListTasks", "SubscribeToTask", "CreateTaskPushNotificationConfig", "", "message/send"} {
		if a.AllowsMethod(m) {
			t.Errorf("the default list must not allow %q", m)
		}
	}
	if !b.AllowsMethod("GetTask") || b.AllowsMethod("SendStreamingMessage") {
		t.Error("an explicit list replaces the default")
	}
}

func TestMethodsRejectBadValues(t *testing.T) {
	for name, m := range map[string]string{"empty list": `[]`, "empty name": `[""]`, "space": `["Send Message"]`, "duplicate": `["A","A"]`, "star": `["*"]`} {
		_, err := Parse([]byte(`{"callers":[{"name":"a","key":true,"agents":["k/a"],"userId":{"fixed":"x"},"methods":` + m + `}]}`))
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestMemoryFlag(t *testing.T) {
	r, err := Parse([]byte(`{"callers":[
 {"name":"a","key":true,"agents":["k/a"],"userId":{"fixed":"x"},"memory":true},
 {"name":"b","key":true,"agents":["k/a"],"userId":{"fixed":"y"},"memory":false},
 {"name":"c","key":true,"agents":["k/a"],"userId":{"fixed":"z"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !r.ByName("a").Memory || r.ByName("b").Memory || r.ByName("c").Memory {
		t.Fatal("memory must be true only where the rule says so (default false)")
	}
	if _, err := Parse([]byte(`{"callers":[{"name":"a","key":true,"agents":["k/a"],"userId":{"fixed":"x"},"memory":"yes"}]}`)); err == nil {
		t.Fatal("memory must be a boolean")
	}
}
