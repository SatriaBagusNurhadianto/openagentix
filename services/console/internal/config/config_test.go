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
