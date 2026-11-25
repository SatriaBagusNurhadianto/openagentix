package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"CONTROL_PLANE_CATALOG_FILE": "/c.json", "CONTROL_PLANE_POLICY_FILE": "/p.json"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HealthAddr != ":8081" || c.Resync != 60*time.Second || c.Backoff != 10*time.Second || c.LogLevel != "info" {
		t.Fatalf("%+v", c)
	}
}

func TestOverridesAndRefusals(t *testing.T) {
	ok := map[string]string{"CONTROL_PLANE_CATALOG_FILE": "/c.json", "CONTROL_PLANE_POLICY_FILE": "/p.json"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range ok {
			m[a] = b
		}
		m[k] = v
		return m
	}
	c, err := Load(env(with("CONTROL_PLANE_RESYNC", "5s")))
	if err != nil || c.Resync != 5*time.Second {
		t.Fatalf("%+v %v", c, err)
	}
	for name, tc := range map[string]struct {
		m    map[string]string
		want string
	}{
		"no catalog":  {map[string]string{"CONTROL_PLANE_POLICY_FILE": "/p"}, "CONTROL_PLANE_CATALOG_FILE"},
		"no policy":   {map[string]string{"CONTROL_PLANE_CATALOG_FILE": "/c"}, "CONTROL_PLANE_POLICY_FILE"},
		"bad resync":  {with("CONTROL_PLANE_RESYNC", "soon"), "CONTROL_PLANE_RESYNC"},
		"tiny resync": {with("CONTROL_PLANE_RESYNC", "100ms"), "CONTROL_PLANE_RESYNC"},
		"bad backoff": {with("CONTROL_PLANE_BACKOFF", "-1s"), "CONTROL_PLANE_BACKOFF"},
		"bad level":   {with("CONTROL_PLANE_LOG_LEVEL", "loud"), "CONTROL_PLANE_LOG_LEVEL"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(env(tc.m))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
