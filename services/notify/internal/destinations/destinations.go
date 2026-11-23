// Package destinations loads and validates the list of places agents may
// notify. It is the only place where chat ids, channel ids, webhook URLs and
// platform credentials exist; everything downstream sees a name, a
// description and a ready-to-use Sender. A configuration problem is reported
// without echoing any value from the file.
package destinations

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/openagentix/openagentix/services/notify/internal/config"
	"github.com/openagentix/openagentix/services/notify/internal/delivery"
	"github.com/openagentix/openagentix/services/notify/internal/sanitize"
	"github.com/openagentix/openagentix/services/notify/internal/slack"
	"github.com/openagentix/openagentix/services/notify/internal/telegram"
	"github.com/openagentix/openagentix/services/notify/internal/webhook"
)

// ErrNotConfigured means there is no destinations file to read (no path, or
// the file does not exist). The service then runs with zero destinations.
var ErrNotConfigured = errors.New("no destinations file")

const (
	maxFileBytes    = 1 << 20
	maxDestinations = 100
	maxDescription  = 200
	maxSecretLen    = 512
	// A token or signing secret is a few hundred bytes; the bound only stops
	// a wrong path (a log, a device) from being slurped into memory.
	maxSecretFileBytes = 64 << 10
	maxRatePerWindow   = 100000
)

var (
	nameRe         = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	telegramChatRe = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{5,32})$`)
	slackChannelRe = regexp.MustCompile(`^[CGD][A-Z0-9]{2,30}$`)
	backtickRe     = regexp.MustCompile("`[^`]*`")
)

// Destination is one validated, ready-to-use target.
type Destination struct {
	Name        string
	Description string
	Platform    string
	Limits      config.Limits
	Sender      delivery.Sender
}

// Options supply defaults and the plumbing used to build senders.
type Options struct {
	Defaults        config.Limits
	HTTP            *http.Client
	MaxRetryAfter   time.Duration
	TelegramBaseURL string // tests only; empty means the real Bot API
	SlackBaseURL    string // tests only; empty means the real Slack API
	Getenv          func(string) string
}

type rawFile struct {
	Destinations []rawDest `yaml:"destinations"`
}

type rawDest struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Platform    string `yaml:"platform"`

	ChatID    string `yaml:"chatId"`  // telegram
	Channel   string `yaml:"channel"` // slack
	TokenEnv  string `yaml:"tokenEnv"`
	TokenFile string `yaml:"tokenFile"`

	URL               string `yaml:"url"` // webhook
	AllowInsecureHTTP bool   `yaml:"allowInsecureHTTP"`
	AllowPrivateAddr  bool   `yaml:"allowPrivateAddress"`
	SecretEnv         string `yaml:"secretEnv"`
	SecretFile        string `yaml:"secretFile"`

	MaxTextChars int `yaml:"maxTextChars"`
	Rate         *struct {
		PerMinute int `yaml:"perMinute"`
		PerHour   int `yaml:"perHour"`
	} `yaml:"rate"`
}

// Load reads and validates the file at path.
func Load(path string, o Options) ([]Destination, error) {
	if path == "" {
		return nil, ErrNotConfigured
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, errors.New("cannot read the destinations file")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, errors.New("cannot read the destinations file")
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("the destinations file is larger than %d bytes", maxFileBytes)
	}
	return Parse(data, o)
}

// Parse validates YAML or JSON (JSON is YAML) and builds the destinations.
// Every problem found is reported together.
func Parse(data []byte, o Options) ([]Destination, error) {
	var file rawFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("invalid destinations file: %s", scrubYAML(err))
	}
	// A second document would be silently ignored by a single Decode: refuse it.
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid destinations file: it contains more than one YAML document")
	}
	if len(file.Destinations) > maxDestinations {
		return nil, fmt.Errorf("too many destinations (maximum %d)", maxDestinations)
	}
	var errs []error
	var out []Destination
	seen := map[string]bool{}
	for i, r := range file.Destinations {
		label := fmt.Sprintf("destinations[%d]", i)
		if nameRe.MatchString(r.Name) {
			label += " (" + r.Name + ")"
		}
		d, err := build(r, o)
		if err == nil && seen[r.Name] {
			err = errors.New("duplicate name")
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
			continue
		}
		seen[r.Name] = true
		out = append(out, d)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func scrubYAML(err error) string {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		return backtickRe.ReplaceAllString(strings.Join(te.Errors, "; "), "<value>")
	}
	return backtickRe.ReplaceAllString(err.Error(), "<value>")
}

func build(r rawDest, o Options) (Destination, error) {
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if !nameRe.MatchString(r.Name) {
		fail("name must match [a-z0-9][a-z0-9_-]{0,62}")
	}
	desc := strings.TrimSpace(r.Description)
	switch {
	case desc == "":
		fail("description is required")
	case len([]rune(desc)) > maxDescription:
		fail("description is longer than %d characters", maxDescription)
	case sanitize.Line(desc) != strings.Join(strings.Fields(desc), " ") || strings.ContainsFunc(desc, unicode.IsControl):
		fail("description must be printable text on one line")
	}

	lim := o.Defaults
	if r.MaxTextChars != 0 {
		if r.MaxTextChars < 1 || r.MaxTextChars > config.MaxTextCharsCeiling {
			fail("maxTextChars must be between 1 and %d", config.MaxTextCharsCeiling)
		} else {
			lim.MaxTextChars = r.MaxTextChars
		}
	}
	if r.Rate != nil {
		if r.Rate.PerMinute < 0 || r.Rate.PerHour < 0 || r.Rate.PerMinute > maxRatePerWindow || r.Rate.PerHour > maxRatePerWindow {
			fail("rate values must be between 0 and %d", maxRatePerWindow)
		}
		if r.Rate.PerMinute > 0 {
			lim.RatePerMinute = r.Rate.PerMinute
		}
		if r.Rate.PerHour > 0 {
			lim.RatePerHour = r.Rate.PerHour
		}
	}

	forbid := func(field, value string) {
		if value != "" {
			fail("%s does not apply to platform %s", field, r.Platform)
		}
	}
	if r.Platform != "webhook" && r.AllowPrivateAddr {
		fail("allowPrivateAddress does not apply to platform %s", r.Platform)
	}
	var sender delivery.Sender
	switch r.Platform {
	case "telegram":
		forbid("channel", r.Channel)
		forbid("url", r.URL)
		forbid("secretEnv", r.SecretEnv)
		forbid("secretFile", r.SecretFile)
		if !telegramChatRe.MatchString(r.ChatID) {
			fail("chatId is required: a numeric chat id or @channelname")
		}
		tok, err := secret("token", "tokenEnv", "tokenFile", r.TokenEnv, r.TokenFile, true, o)
		if err != nil {
			errs = append(errs, err)
		}
		sender = telegram.New(telegram.Config{BaseURL: o.TelegramBaseURL, Token: tok, ChatID: r.ChatID, HTTP: o.HTTP, MaxRetryAfter: o.MaxRetryAfter})
	case "slack":
		forbid("chatId", r.ChatID)
		forbid("url", r.URL)
		forbid("secretEnv", r.SecretEnv)
		forbid("secretFile", r.SecretFile)
		if !slackChannelRe.MatchString(r.Channel) {
			fail("channel is required and must be a Slack channel id such as C0123456789")
		}
		tok, err := secret("token", "tokenEnv", "tokenFile", r.TokenEnv, r.TokenFile, true, o)
		if err != nil {
			errs = append(errs, err)
		}
		sender = slack.New(slack.Config{BaseURL: o.SlackBaseURL, Token: tok, Channel: r.Channel, HTTP: o.HTTP, MaxRetryAfter: o.MaxRetryAfter})
	case "webhook":
		forbid("chatId", r.ChatID)
		forbid("channel", r.Channel)
		forbid("tokenEnv", r.TokenEnv)
		forbid("tokenFile", r.TokenFile)
		if err := webhook.ValidateURL(r.URL, r.AllowInsecureHTTP, r.AllowPrivateAddr); err != nil {
			errs = append(errs, err)
		}
		sec, err := secret("signing secret", "secretEnv", "secretFile", r.SecretEnv, r.SecretFile, false, o)
		if err != nil {
			errs = append(errs, err)
		}
		sender = webhook.New(webhook.Config{URL: r.URL, Secret: sec, HTTP: o.HTTP})
	default:
		fail("platform must be telegram, slack or webhook")
	}
	if len(errs) > 0 {
		return Destination{}, errors.Join(errs...)
	}
	return Destination{Name: r.Name, Description: desc, Platform: r.Platform, Limits: lim, Sender: sender}, nil
}

var errTooLarge = errors.New("file too large")

// readBounded reads at most limit bytes of path, or reports errTooLarge.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errTooLarge
	}
	return b, nil
}

// secret resolves a credential from exactly one of an environment variable or
// a file (or neither, when optional). Errors name the settings, never values.
func secret(what, envKey, fileKey, envName, file string, required bool, o Options) (string, error) {
	switch {
	case envName != "" && file != "":
		return "", fmt.Errorf("set only one of %s and %s", envKey, fileKey)
	case envName == "" && file == "":
		if required {
			return "", fmt.Errorf("%s is required: set %s or %s", what, envKey, fileKey)
		}
		return "", nil
	}
	var raw string
	if envName != "" {
		getenv := o.Getenv
		if getenv == nil {
			getenv = os.Getenv
		}
		if raw = getenv(envName); strings.TrimSpace(raw) == "" {
			return "", fmt.Errorf("%s: environment variable %s is not set or empty", envKey, envName)
		}
	} else {
		b, err := readBounded(file, maxSecretFileBytes)
		switch {
		case errors.Is(err, errTooLarge):
			return "", fmt.Errorf("%s: the file is larger than %d KiB", fileKey, maxSecretFileBytes>>10)
		case err != nil:
			return "", fmt.Errorf("%s: cannot read the file", fileKey)
		}
		raw = string(b)
	}
	v := strings.TrimSpace(raw)
	if v == "" || len(v) > maxSecretLen || strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", fmt.Errorf("%s must be a single token of 1 to %d characters without whitespace", what, maxSecretLen)
	}
	return v, nil
}
