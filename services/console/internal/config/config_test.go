package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestRealConfigNeedsTheFilesAndACluster(t *testing.T) {
	if _, err := Load(env(nil)); err == nil || !strings.Contains(err.Error(), "CONSOLE_TOKEN_FILE") {
		t.Fatal(err)
	}
	m := map[string]string{"CONSOLE_TOKEN_FILE": "/t", "CONSOLE_CATALOG_FILE": "/c", "CONSOLE_POLICY_FILE": "/p"}
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "CONSOLE_KUBE_API") {
		t.Fatal("outside a cluster the API address is required:", err)
	}
	m["KUBERNETES_SERVICE_HOST"], m["KUBERNETES_SERVICE_PORT"] = "10.0.0.1", "443"
	c, err := Load(env(m))
	if err != nil || c.KubeAPI != "https://10.0.0.1:443" || c.KubeCAFile != saDir+"/ca.crt" || c.Addr != ":8080" || c.DevFake || c.Viewer {
		t.Fatalf("%+v %v", c, err)
	}
	m["CONSOLE_VIEWER"] = "true"
	m["CONSOLE_KUBE_API"] = "ftp://x"
	if _, err := Load(env(m)); err == nil {
		t.Fatal("a non-http API address is refused")
	}
}

func TestDemoModeRefusesAnyRealClusterSetting(t *testing.T) {
	c, err := Load(env(map[string]string{"CONSOLE_DEV_FAKE": "1"}))
	if err != nil || !c.DevFake || c.Addr != "127.0.0.1:8080" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, k := range []string{"CONSOLE_KUBE_API", "CONSOLE_CATALOG_FILE", "CONSOLE_POLICY_FILE", "KUBERNETES_SERVICE_HOST", "CONSOLE_KUBE_TOKEN_FILE"} {
		_, err := Load(env(map[string]string{"CONSOLE_DEV_FAKE": "1", k: "x"}))
		if err == nil || !strings.Contains(err.Error(), k) || !strings.Contains(err.Error(), "refuses to start") {
			t.Errorf("%s: %v", k, err)
		}
	}
}

func realEnv(extra map[string]string) map[string]string {
	m := map[string]string{"CONSOLE_TOKEN_FILE": "/t", "CONSOLE_CATALOG_FILE": "/c", "CONSOLE_POLICY_FILE": "/p", "KUBERNETES_SERVICE_HOST": "10.0.0.1"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestPlainHTTPKubeAPIIsRefusedWithoutAnExplicitDevOverride(t *testing.T) {
	_, err := Load(env(realEnv(map[string]string{"CONSOLE_KUBE_API": "http://127.0.0.1:8001"})))
	if err == nil || !strings.Contains(err.Error(), "https") || !strings.Contains(err.Error(), "CONSOLE_ALLOW_INSECURE_KUBE_API") {
		t.Fatalf("the service account token would cross the network in clear text: %v", err)
	}
	c, err := Load(env(realEnv(map[string]string{"CONSOLE_KUBE_API": "http://127.0.0.1:8001", "CONSOLE_ALLOW_INSECURE_KUBE_API": "1"})))
	if err != nil || c.KubeAPI != "http://127.0.0.1:8001" || !c.AllowInsecureKube {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := Load(env(realEnv(map[string]string{"CONSOLE_KUBE_API": "https://k8s.example:6443"}))); err != nil {
		t.Fatal(err)
	}
}

func TestProtectionSettingsHaveDefaultsAndAreChecked(t *testing.T) {
	c, err := Load(env(realEnv(nil)))
	if err != nil || len(c.AllowedHosts) != 0 || c.AuthFailLimit != 10 || c.AuthFailLimitGlobal != 100 || c.ValidatePerMinute != 60 || c.HSTS || c.ListMax != 500 {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = Load(env(realEnv(map[string]string{"CONSOLE_ALLOWED_HOSTS": " console.example.com, ingress.internal:8443 ,", "CONSOLE_AUTH_FAIL_LIMIT": "5",
		"CONSOLE_AUTH_FAIL_LIMIT_GLOBAL": "50", "CONSOLE_VALIDATE_PER_MINUTE": "20", "CONSOLE_HSTS": "true", "CONSOLE_LIST_MAX": "200"})))
	if err != nil || len(c.AllowedHosts) != 2 || c.AllowedHosts[1] != "ingress.internal:8443" || c.AuthFailLimit != 5 || c.AuthFailLimitGlobal != 50 || c.ValidatePerMinute != 20 || !c.HSTS || c.ListMax != 200 {
		t.Fatalf("%+v %v", c, err)
	}
	for _, kv := range [][2]string{{"CONSOLE_AUTH_FAIL_LIMIT", "0"}, {"CONSOLE_AUTH_FAIL_LIMIT", "x"}, {"CONSOLE_AUTH_FAIL_LIMIT_GLOBAL", "-1"}, {"CONSOLE_VALIDATE_PER_MINUTE", "0"},
		{"CONSOLE_LIST_MAX", "0"}, {"CONSOLE_LIST_MAX", "100000"}, {"CONSOLE_ALLOWED_HOSTS", "http://console.example.com"}, {"CONSOLE_ALLOWED_HOSTS", "a b"}} {
		if _, err := Load(env(realEnv(map[string]string{kv[0]: kv[1]}))); err == nil || !strings.Contains(err.Error(), kv[0]) {
			t.Errorf("%s=%s: %v", kv[0], kv[1], err)
		}
	}
}
