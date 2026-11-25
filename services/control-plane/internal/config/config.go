// Package config reads the controller's settings from environment variables. The catalog and the policy are files
// (package catalog); this holds only the paths and the timing.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type Config struct {
	CatalogFile string
	PolicyFile  string
	HealthAddr  string
	Resync      time.Duration
	Backoff     time.Duration
	LogLevel    string
}

func or(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func duration(name, v string, def, min time.Duration) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < min {
		return 0, fmt.Errorf("%s must be a duration of at least %s, got %q", name, min, v)
	}
	return d, nil
}

// Load reads configuration using getenv (for example os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		CatalogFile: getenv("CONTROL_PLANE_CATALOG_FILE"),
		PolicyFile:  getenv("CONTROL_PLANE_POLICY_FILE"),
		HealthAddr:  or(getenv("CONTROL_PLANE_HEALTH_ADDR"), ":8081"),
		LogLevel:    or(getenv("CONTROL_PLANE_LOG_LEVEL"), "info"),
	}
	var errs []error
	if c.CatalogFile == "" {
		errs = append(errs, errors.New("CONTROL_PLANE_CATALOG_FILE is required"))
	}
	if c.PolicyFile == "" {
		errs = append(errs, errors.New("CONTROL_PLANE_POLICY_FILE is required"))
	}
	var err error
	if c.Resync, err = duration("CONTROL_PLANE_RESYNC", getenv("CONTROL_PLANE_RESYNC"), 60*time.Second, time.Second); err != nil {
		errs = append(errs, err)
	}
	if c.Backoff, err = duration("CONTROL_PLANE_BACKOFF", getenv("CONTROL_PLANE_BACKOFF"), 10*time.Second, time.Millisecond); err != nil {
		errs = append(errs, err)
	}
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(c.LogLevel)); err != nil {
		errs = append(errs, fmt.Errorf("CONTROL_PLANE_LOG_LEVEL must be debug, info, warn or error, got %q", c.LogLevel))
	}
	return c, errors.Join(errs...)
}
