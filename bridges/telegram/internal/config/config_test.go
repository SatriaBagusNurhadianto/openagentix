package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoad_Defaults(t *testing.T) {
	c, err := Load(env(map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "TELEGRAM_ALLOW_ALL": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.BotToken != "tok" || c.A2AURL != "http://agent" || c.A2AToken != "" {
		t.Fatalf("cfg = %+v", c)
	}
	if c.APIBase != "https://api.telegram.org" || c.AgentTimeout != 120*time.Second || len(c.AllowedChatIDs) != 0 {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestLoad_AllValues(t *testing.T) {
	c, err := Load(env(map[string]string{
		"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "A2A_TOKEN": "bt",
		"TELEGRAM_API_BASE": "http://localhost:1", "AGENT_TIMEOUT": "5s",
		"TELEGRAM_ALLOWED_CHAT_IDS": " 1, -1002, 3 ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.A2AToken != "bt" || c.APIBase != "http://localhost:1" || c.AgentTimeout != 5*time.Second {
		t.Fatalf("cfg = %+v", c)
	}
	if len(c.AllowedChatIDs) != 3 || !c.AllowedChatIDs[1] || !c.AllowedChatIDs[-1002] || !c.AllowedChatIDs[3] {
		t.Fatalf("allowed = %v", c.AllowedChatIDs)
	}
}

func TestLoad_Errors(t *testing.T) {
	base := map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "TELEGRAM_ALLOWED_CHAT_IDS": "1"}
	cases := map[string]struct {
		mutate func(map[string]string)
		want   string
	}{
		"missing token": {func(m map[string]string) { delete(m, "TELEGRAM_BOT_TOKEN") }, "TELEGRAM_BOT_TOKEN"},
		"missing url":   {func(m map[string]string) { delete(m, "A2A_URL") }, "A2A_URL"},
		"bad timeout":   {func(m map[string]string) { m["AGENT_TIMEOUT"] = "soon" }, "AGENT_TIMEOUT"},
		"bad chat id":   {func(m map[string]string) { m["TELEGRAM_ALLOWED_CHAT_IDS"] = "1,abc" }, "TELEGRAM_ALLOWED_CHAT_IDS"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			tc.mutate(m)
			_, err := Load(env(m))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %s", err, tc.want)
			}
		})
	}
}

func TestLoad_FailsClosedWithoutAllowlist(t *testing.T) {
	base := map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent"}
	for _, ids := range []string{"", "  "} {
		m := map[string]string{"TELEGRAM_ALLOWED_CHAT_IDS": ids}
		for k, v := range base {
			m[k] = v
		}
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), "TELEGRAM_ALLOWED_CHAT_IDS") || !strings.Contains(err.Error(), "TELEGRAM_ALLOW_ALL") {
			t.Fatalf("ids=%q err = %v", ids, err)
		}
	}
}

func TestLoad_AllowAllExplicit(t *testing.T) {
	c, err := Load(env(map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "TELEGRAM_ALLOW_ALL": "true"}))
	if err != nil || !c.AllowAll {
		t.Fatalf("c=%+v err=%v", c, err)
	}
}

func TestLoad_AllowAllMalformed(t *testing.T) {
	_, err := Load(env(map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "TELEGRAM_ALLOW_ALL": "yes"}))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_ALLOW_ALL") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoad_EmptyChatIDEntryIsError(t *testing.T) {
	_, err := Load(env(map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "TELEGRAM_ALLOWED_CHAT_IDS": "1,,2"}))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_ALLOWED_CHAT_IDS") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoad_ConcurrencyAndShutdown(t *testing.T) {
	base := map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "TELEGRAM_ALLOW_ALL": "true"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	c, err := Load(env(with()))
	if err != nil || c.MaxConcurrentTurns != 8 || c.ShutdownGrace != 25*time.Second {
		t.Fatalf("defaults: c=%+v err=%v", c, err)
	}
	c, err = Load(env(with("MAX_CONCURRENT_TURNS", "3", "SHUTDOWN_GRACE", "5s")))
	if err != nil || c.MaxConcurrentTurns != 3 || c.ShutdownGrace != 5*time.Second {
		t.Fatalf("explicit: c=%+v err=%v", c, err)
	}
	for _, kv := range [][]string{{"MAX_CONCURRENT_TURNS", "0"}, {"MAX_CONCURRENT_TURNS", "x"}, {"SHUTDOWN_GRACE", "-1s"}, {"SHUTDOWN_GRACE", "soon"}} {
		if _, err := Load(env(with(kv...))); err == nil || !strings.Contains(err.Error(), kv[0]) {
			t.Fatalf("%v: err = %v", kv, err)
		}
	}
}

func TestLoad_HealthAddr(t *testing.T) {
	base := map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "TELEGRAM_ALLOW_ALL": "true"}
	c, err := Load(env(base))
	if err != nil || c.HealthAddr != ":8080" {
		t.Fatalf("default: %+v %v", c, err)
	}
	base["HEALTH_ADDR"] = "127.0.0.1:9000"
	c, err = Load(env(base))
	if err != nil || c.HealthAddr != "127.0.0.1:9000" {
		t.Fatalf("explicit: %+v %v", c, err)
	}
}

func TestLoad_UserIDPrefixDefault(t *testing.T) {
	c, err := Load(env(map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "TELEGRAM_ALLOW_ALL": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.UserIDPrefix != "telegram:" {
		t.Fatalf("UserIDPrefix = %q, want telegram:", c.UserIDPrefix)
	}
}

func TestLoad_UserIDPrefixOverride(t *testing.T) {
	c, err := Load(env(map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "TELEGRAM_ALLOW_ALL": "true", "A2A_USER_ID_PREFIX": "team-b:"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.UserIDPrefix != "team-b:" {
		t.Fatalf("UserIDPrefix = %q", c.UserIDPrefix)
	}
}

func TestLoad_UserIDPrefixRejectsUnsafeCharacters(t *testing.T) {
	for _, bad := range []string{"a b:", "x\r\nY: z", "é:", strings.Repeat("a", 65)} {
		_, err := Load(env(map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "TELEGRAM_ALLOW_ALL": "true", "A2A_USER_ID_PREFIX": bad}))
		if err == nil || !strings.Contains(err.Error(), "A2A_USER_ID_PREFIX") {
			t.Fatalf("prefix %q: err = %v", bad, err)
		}
	}
}

func TestLoad_TokenFile(t *testing.T) {
	base := map[string]string{"TELEGRAM_BOT_TOKEN": "tok", "A2A_URL": "http://agent", "TELEGRAM_ALLOW_ALL": "true"}
	c, err := Load(env(base))
	if err != nil || c.A2ATokenFile != "" {
		t.Fatalf("default: %+v %v", c, err)
	}
	base["A2A_TOKEN_FILE"] = "/var/run/a2a/token"
	c, err = Load(env(base))
	if err != nil || c.A2ATokenFile != "/var/run/a2a/token" {
		t.Fatalf("set: %+v %v", c, err)
	}
}
