package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/notify/internal/config"
	"github.com/openagentix/openagentix/services/notify/internal/delivery"
	"github.com/openagentix/openagentix/services/notify/internal/httpx"
)

const (
	fakeTGToken    = "111111:FAKE-TELEGRAM-TOKEN"
	fakeSlackToken = "xoxb-FAKE-SLACK-TOKEN"
	fakeSecret     = "FAKE-HMAC-SECRET"
)

func opts(env map[string]string) Options {
	return Options{
		Defaults:      config.Limits{MaxTextChars: 4000, RatePerMinute: 6, RatePerHour: 60},
		HTTP:          httpx.NewClient(time.Second),
		MaxRetryAfter: time.Second,
		Getenv:        func(k string) string { return env[k] },
	}
}

var goodEnv = map[string]string{"TG_TOKEN": fakeTGToken, "SLACK_TOKEN": fakeSlackToken, "HOOK_SECRET": fakeSecret}

const goodYAML = `
destinations:
  - name: ops-telegram
    description: On-call chat for operations
    platform: telegram
    chatId: -1001234567890
    tokenEnv: TG_TOKEN
  - name: team-slack
    description: "#engineering updates"
    platform: slack
    channel: C0FAKE12345
    tokenEnv: SLACK_TOKEN
    maxTextChars: 1000
    rate: {perMinute: 2, perHour: 10}
  - name: audit-hook
    description: Audit receiver
    platform: webhook
    url: https://hooks.example.com/notify/abc
    secretEnv: HOOK_SECRET
`

func TestParse_ValidFileYieldsDestinationsWithDefaultsAndOverrides(t *testing.T) {
	ds, err := Parse([]byte(goodYAML), opts(goodEnv))
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 {
		t.Fatalf("got %d destinations", len(ds))
	}
	by := map[string]Destination{}
	for _, d := range ds {
		by[d.Name] = d
		if d.Sender == nil {
			t.Fatalf("%s has no sender", d.Name)
		}
	}
	if d := by["ops-telegram"]; d.Platform != "telegram" || d.Limits != (config.Limits{MaxTextChars: 4000, RatePerMinute: 6, RatePerHour: 60}) {
		t.Fatalf("telegram = %+v", d)
	}
	if d := by["team-slack"]; d.Limits != (config.Limits{MaxTextChars: 1000, RatePerMinute: 2, RatePerHour: 10}) || d.Description != "#engineering updates" {
		t.Fatalf("slack = %+v", d)
	}
	if by["audit-hook"].Platform != "webhook" {
		t.Fatalf("webhook = %+v", by["audit-hook"])
	}
}

func TestParse_AcceptsJSON(t *testing.T) {
	j, _ := json.Marshal(map[string]any{"destinations": []map[string]any{
		{"name": "ops", "description": "d", "platform": "telegram", "chatId": -100123, "tokenEnv": "TG_TOKEN"},
	}})
	ds, err := Parse(j, opts(goodEnv))
	if err != nil || len(ds) != 1 {
		t.Fatalf("ds=%v err=%v", ds, err)
	}
}

func TestParse_EmptyFileOrNoDestinationsIsZeroNotError(t *testing.T) {
	for _, in := range []string{"", "destinations: []\n", "destinations:\n"} {
		ds, err := Parse([]byte(in), opts(nil))
		if err != nil || len(ds) != 0 {
			t.Fatalf("%q: ds=%v err=%v", in, ds, err)
		}
	}
}

func TestParse_Rejects(t *testing.T) {
	tg := func(extra string) string {
		return "destinations:\n  - name: ops\n    description: d\n    platform: telegram\n    chatId: \"-1001\"\n    tokenEnv: TG_TOKEN\n" + extra
	}
	cases := map[string]struct{ yaml, want string }{
		"unknown field":           {tg("    chatID: \"5\"\n"), "chatID"},
		"inline token":            {tg("    token: abc\n"), "token"},
		"uppercase name":          {strings.Replace(tg(""), "name: ops", "name: Ops", 1), "name"},
		"name with space":         {strings.Replace(tg(""), "name: ops", "name: \"my chat\"", 1), "name"},
		"name too long":           {strings.Replace(tg(""), "name: ops", "name: "+strings.Repeat("a", 64), 1), "name"},
		"empty description":       {strings.Replace(tg(""), "description: d", "description: \"\"", 1), "description"},
		"unknown platform":        {strings.Replace(tg(""), "platform: telegram", "platform: sms", 1), "platform"},
		"missing chat id":         {strings.Replace(tg(""), "    chatId: \"-1001\"\n", "", 1), "chatId"},
		"bad chat id":             {strings.Replace(tg(""), "\"-1001\"", "\"not-a-chat\"", 1), "chatId"},
		"both token sources":      {tg("    tokenFile: /x\n"), "tokenEnv"},
		"no token source":         {strings.Replace(tg(""), "    tokenEnv: TG_TOKEN\n", "", 1), "token"},
		"unset token env":         {strings.Replace(tg(""), "TG_TOKEN", "NOPE", 1), "NOPE"},
		"missing token file":      {strings.Replace(tg(""), "tokenEnv: TG_TOKEN", "tokenFile: /nonexistent/token", 1), "tokenFile"},
		"slack field on telegram": {tg("    channel: C0FAKE12345\n"), "channel"},
		"max text over ceiling":   {tg("    maxTextChars: 999999\n"), "maxTextChars"},
		"negative rate":           {tg("    rate: {perMinute: -1}\n"), "rate"},
	}
	for name, c := range cases {
		_, err := Parse([]byte(c.yaml), opts(goodEnv))
		if err == nil {
			t.Errorf("%s: want error", name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q should mention %q", name, err, c.want)
		}
	}
}

func TestParse_DuplicateNames(t *testing.T) {
	one := "  - name: ops\n    description: d\n    platform: webhook\n    url: https://x.example/h\n"
	_, err := Parse([]byte("destinations:\n"+one+one), opts(nil))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v", err)
	}
}

func TestParse_SlackChannelMustBeAnID(t *testing.T) {
	y := "destinations:\n  - name: s\n    description: d\n    platform: slack\n    channel: \"#general\"\n    tokenEnv: SLACK_TOKEN\n"
	if _, err := Parse([]byte(y), opts(goodEnv)); err == nil || !strings.Contains(err.Error(), "channel") {
		t.Fatalf("err = %v", err)
	}
}

func TestParse_WebhookHTTPNeedsExplicitFlag(t *testing.T) {
	y := "destinations:\n  - name: h\n    description: d\n    platform: webhook\n    url: http://recv.ns.svc.cluster.local/hook\n"
	if _, err := Parse([]byte(y), opts(nil)); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Parse([]byte(y+"    allowInsecureHTTP: true\n"), opts(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestParse_TokenFileIsReadAndTrimmed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tok")
	if err := os.WriteFile(p, []byte("  "+fakeTGToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	y := "destinations:\n  - name: ops\n    description: d\n    platform: telegram\n    chatId: \"1\"\n    tokenFile: " + p + "\n"
	if _, err := Parse([]byte(y), opts(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestParse_TokenWithInnerWhitespaceIsRejectedWithoutEchoingIt(t *testing.T) {
	env := map[string]string{"TG_TOKEN": "two words " + fakeTGToken}
	y := "destinations:\n  - name: ops\n    description: d\n    platform: telegram\n    chatId: \"1\"\n    tokenEnv: TG_TOKEN\n"
	_, err := Parse([]byte(y), opts(env))
	if err == nil || strings.Contains(err.Error(), fakeTGToken) || strings.Contains(err.Error(), "two words") {
		t.Fatalf("err = %v", err)
	}
}

func TestParse_ReportsEveryProblemAtOnce(t *testing.T) {
	y := "destinations:\n  - name: A\n    description: d\n    platform: sms\n  - name: b\n    description: d\n    platform: webhook\n    url: http://x\n"
	_, err := Parse([]byte(y), opts(nil))
	if err == nil || strings.Count(err.Error(), "\n") < 1 {
		t.Fatalf("err = %v", err)
	}
}

func TestParse_ErrorsNeverEchoChatIdsOrURLs(t *testing.T) {
	y := "destinations:\n  - name: ops\n    description: d\n    platform: telegram\n    chatId: [oops, -1001234567890]\n    tokenEnv: TG_TOKEN\n"
	_, err := Parse([]byte(y), opts(goodEnv))
	if err == nil || strings.Contains(err.Error(), "1234567890") {
		t.Fatalf("err = %v", err)
	}
	y = "destinations:\n  - name: h\n    description: d\n    platform: webhook\n    url: http://secret-host.example/hook/SECRETPATH\n"
	_, err = Parse([]byte(y), opts(nil))
	if err == nil || strings.Contains(err.Error(), "SECRETPATH") || strings.Contains(err.Error(), "secret-host") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoad_NoPathOrNoFileIsNotConfigured(t *testing.T) {
	if _, err := Load("", opts(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("empty path: %v", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), opts(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestLoad_MalformedFileIsAHardErrorNotNotConfigured(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.yaml")
	_ = os.WriteFile(p, []byte("destinations: [unclosed"), 0o600)
	_, err := Load(p, opts(nil))
	if err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoad_ReadsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.yaml")
	_ = os.WriteFile(p, []byte(goodYAML), 0o600)
	ds, err := Load(p, opts(goodEnv))
	if err != nil || len(ds) != 3 {
		t.Fatalf("ds=%d err=%v", len(ds), err)
	}
}

func TestLoad_OversizedFileIsRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.yaml")
	_ = os.WriteFile(p, []byte(strings.Repeat("# pad\n", 300000)), 0o600)
	if _, err := Load(p, opts(nil)); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestSenderUsesConfiguredChatIDAndTokenAgainstTheFakeAPI(t *testing.T) {
	var path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	o := opts(goodEnv)
	o.TelegramBaseURL = srv.URL
	ds, err := Parse([]byte(goodYAML), o)
	if err != nil {
		t.Fatal(err)
	}
	err = ds[0].Sender.Send(context.Background(), delivery.Message{Destination: "ops-telegram", Header: "H", Text: "hello", SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/bot"+fakeTGToken+"/sendMessage" || body["chat_id"] != "-1001234567890" {
		t.Fatalf("path=%q chat_id=%v", path, body["chat_id"])
	}
}

func TestParse_WebhookToLoopbackOrMetadataNeedsExplicitFlag(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:9000/hook", "https://169.254.169.254/x", "http://[::1]/x", "http://0.0.0.0/x"} {
		y := "destinations:\n  - name: h\n    description: d\n    platform: webhook\n    allowInsecureHTTP: true\n    url: " + u + "\n"
		if _, err := Parse([]byte(y), opts(nil)); err == nil || !strings.Contains(err.Error(), "allowPrivateAddress") || strings.Contains(err.Error(), "169.254") {
			t.Fatalf("%s: err = %v", u, err)
		}
		if _, err := Parse([]byte(y+"    allowPrivateAddress: true\n"), opts(nil)); err != nil {
			t.Fatalf("%s with the flag: %v", u, err)
		}
	}
}

func TestParse_AllowPrivateAddressOnlyAppliesToWebhooks(t *testing.T) {
	y := "destinations:\n  - name: s\n    description: d\n    platform: slack\n    channel: C0123456789\n    tokenEnv: SLACK_TOKEN\n    allowPrivateAddress: true\n"
	if _, err := Parse([]byte(y), opts(goodEnv)); err == nil || !strings.Contains(err.Error(), "allowPrivateAddress") {
		t.Fatalf("err = %v", err)
	}
}

func TestParse_MultipleYAMLDocumentsAreRejected(t *testing.T) {
	y := "destinations:\n  - name: a\n    description: d\n    platform: webhook\n    url: https://hooks.example.com/a\n---\ndestinations:\n  - name: b\n    description: d\n    platform: webhook\n    url: https://hooks.example.com/b\n"
	_, err := Parse([]byte(y), opts(nil))
	if err == nil || !strings.Contains(err.Error(), "more than one YAML document") {
		t.Fatalf("err = %v", err)
	}
	// A single document with a leading marker is fine.
	if _, err := Parse([]byte("---\n"+strings.SplitN(y, "---\n", 2)[0]), opts(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestParse_SecretFileOverTheLimitIsRejectedWithoutReadingItAll(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big")
	if err := os.WriteFile(p, []byte(strings.Repeat("a", 70<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	y := "destinations:\n  - name: t\n    description: d\n    platform: telegram\n    chatId: \"-100123\"\n    tokenFile: " + p + "\n"
	_, err := Parse([]byte(y), opts(nil))
	if err == nil || !strings.Contains(err.Error(), "larger than 64 KiB") || strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v", err)
	}
}
