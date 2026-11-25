// Package config loads the console's settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	saDir         = "/var/run/secrets/kubernetes.io/serviceaccount"
	DemoToken     = "demo-token"
	defaultKagent = "api.kagent.dev/v1alpha3"
)

// Config holds all settings.
type Config struct {
	Addr           string
	TokenFile      string
	CatalogFile    string
	PolicyFile     string
	Namespace      string // empty: the catalog's agents namespace
	Viewer         bool
	KubeAPI        string
	KubeTokenFile  string
	KubeCAFile     string
	KagentAPI      string
	Context        string
	RequestTimeout time.Duration
	LogLevel       string

	DevFake       bool
	DevFakeSettle time.Duration
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func or(v, d string) string {
	if v != "" {
		return v
	}
	return d
}

// Load reads configuration using getenv (for example os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr: getenv("CONSOLE_ADDR"), TokenFile: getenv("CONSOLE_TOKEN_FILE"), CatalogFile: getenv("CONSOLE_CATALOG_FILE"),
		PolicyFile: getenv("CONSOLE_POLICY_FILE"), Namespace: getenv("CONSOLE_NAMESPACE"), Viewer: truthy(getenv("CONSOLE_VIEWER")),
		KubeAPI: getenv("CONSOLE_KUBE_API"), KubeTokenFile: or(getenv("CONSOLE_KUBE_TOKEN_FILE"), saDir+"/token"),
		KubeCAFile: getenv("CONSOLE_KUBE_CA_FILE"), KagentAPI: or(getenv("CONSOLE_KAGENT_API"), defaultKagent),
		Context: getenv("CONSOLE_CONTEXT"), RequestTimeout: 15 * time.Second, LogLevel: or(getenv("CONSOLE_LOG_LEVEL"), "info"),
		DevFake: truthy(getenv("CONSOLE_DEV_FAKE")), DevFakeSettle: 8 * time.Second,
	}
	if v := getenv("CONSOLE_REQUEST_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > 5*time.Minute {
			return c, errors.New("CONSOLE_REQUEST_TIMEOUT must be a duration between 1s and 5m")
		}
		c.RequestTimeout = d
	}
	if v := getenv("CONSOLE_DEV_FAKE_SETTLE"); v != "" {
		s, err := strconv.Atoi(v)
		if err != nil || s < 0 {
			return c, errors.New("CONSOLE_DEV_FAKE_SETTLE must be a number of seconds, 0 or more")
		}
		c.DevFakeSettle = time.Duration(s) * time.Second
	}

	if c.DevFake {
		// A demo console must never be one that could be pointed at a cluster by accident.
		var clash []string
		for _, k := range []string{"CONSOLE_KUBE_API", "CONSOLE_CATALOG_FILE", "CONSOLE_POLICY_FILE", "CONSOLE_KUBE_TOKEN_FILE", "CONSOLE_KUBE_CA_FILE", "KUBERNETES_SERVICE_HOST"} {
			if getenv(k) != "" {
				clash = append(clash, k)
			}
		}
		if len(clash) > 0 {
			return c, fmt.Errorf("CONSOLE_DEV_FAKE is set together with real cluster settings (%s); the demo mode refuses to start so that fake data is never shown for a real cluster", strings.Join(clash, ", "))
		}
		c.Addr = or(c.Addr, "127.0.0.1:8080")
		c.Namespace = or(c.Namespace, "kagent")
		c.Context = or(c.Context, "demo · kagent")
		return c, nil
	}

	c.Addr = or(c.Addr, ":8080")
	if c.TokenFile == "" {
		return c, errors.New("CONSOLE_TOKEN_FILE is required")
	}
	if c.CatalogFile == "" || c.PolicyFile == "" {
		return c, errors.New("CONSOLE_CATALOG_FILE and CONSOLE_POLICY_FILE are required")
	}
	if c.KubeAPI == "" {
		h, p := getenv("KUBERNETES_SERVICE_HOST"), or(getenv("KUBERNETES_SERVICE_PORT"), "443")
		if h == "" {
			return c, errors.New("CONSOLE_KUBE_API is required outside a cluster (KUBERNETES_SERVICE_HOST is not set)")
		}
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}
		c.KubeAPI = "https://" + h + ":" + p
		c.KubeCAFile = or(c.KubeCAFile, saDir+"/ca.crt")
	}
	if !strings.HasPrefix(c.KubeAPI, "https://") && !strings.HasPrefix(c.KubeAPI, "http://") {
		return c, errors.New("CONSOLE_KUBE_API must be an http or https URL")
	}
	return c, nil
}
