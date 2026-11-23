// Command a2a-call sends one prompt to an A2A agent and exits 0 only when the task completed.
//
// Configuration comes from the environment (the CronJobs of the OpenAgentix chart set it):
//
//	A2A_URL              agent endpoint, http or https (required)
//	A2A_USER_ID          caller identity sent as X-User-Id (required: kagent scopes sessions and memory by it)
//	PROMPT_FILE          file holding the prompt; wins over PROMPT
//	PROMPT               the prompt itself
//	A2A_TIMEOUT          whole-call timeout, Go duration (default 300s)
//	MAX_RESPONSE_BYTES   response body limit (default 4194304)
//	TRACEPARENT          optional W3C traceparent to continue; otherwise the run starts a new trace
//	PRINT_ANSWER         "true" adds the agent's answer to the summary; off by default, answers may be private
//
// stdout gets one JSON summary line; problems go to stderr. Exit codes: 0 completed, 1 the call or the task failed,
// 2 invalid configuration.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openagentix/openagentix/tools/a2a-call/internal/call"
)

const defaultTimeout = 300 * time.Second

func main() {
	os.Exit(run(os.Getenv, os.Stdout, os.Stderr))
}

func run(getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, printAnswer, err := loadConfig(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "a2a-call: invalid configuration: %v\n", err)
		return 2
	}
	res, err := call.Run(context.Background(), cfg)
	sum := map[string]any{
		"state":       res.State,
		"taskId":      res.TaskID,
		"contextId":   res.ContextID,
		"traceId":     res.TraceID,
		"userId":      cfg.UserID,
		"durationMs":  res.Duration.Milliseconds(),
		"answerChars": len([]rune(res.Answer)),
	}
	if err != nil {
		sum["error"] = err.Error()
		if errors.Is(err, call.ErrTimeout) {
			err = fmt.Errorf("%w (limit %s)", err, cfg.Timeout)
		}
	} else if printAnswer {
		sum["answer"] = res.Answer
	}
	b, _ := json.Marshal(sum)
	fmt.Fprintln(stdout, string(b))
	if err != nil {
		fmt.Fprintf(stderr, "a2a-call: FAILED: %v\n", err)
		return 1
	}
	return 0
}

func loadConfig(getenv func(string) string) (cfg call.Config, printAnswer bool, err error) {
	cfg.URL = strings.TrimSpace(getenv("A2A_URL"))
	if cfg.URL == "" {
		return cfg, false, errors.New("A2A_URL is required")
	}
	if u, perr := url.Parse(cfg.URL); perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return cfg, false, errors.New("A2A_URL must be an http or https URL")
	}
	cfg.Traceparent = strings.TrimSpace(getenv("TRACEPARENT"))
	cfg.UserID = strings.TrimSpace(getenv("A2A_USER_ID"))
	if cfg.UserID == "" {
		return cfg, false, errors.New("A2A_USER_ID is required (kagent keys sessions and memory by it)")
	}
	if f := getenv("PROMPT_FILE"); f != "" {
		b, rerr := os.ReadFile(f)
		if rerr != nil {
			return cfg, false, fmt.Errorf("cannot read PROMPT_FILE: %w", rerr)
		}
		cfg.Prompt = strings.TrimSpace(string(b))
	} else {
		cfg.Prompt = strings.TrimSpace(getenv("PROMPT"))
	}
	if cfg.Prompt == "" {
		return cfg, false, errors.New("the prompt is empty (set PROMPT_FILE or PROMPT)")
	}
	cfg.Timeout = defaultTimeout
	if v := getenv("A2A_TIMEOUT"); v != "" {
		d, derr := time.ParseDuration(v)
		if derr != nil || d <= 0 {
			return cfg, false, fmt.Errorf("A2A_TIMEOUT %q is not a positive duration such as 300s", v)
		}
		cfg.Timeout = d
	}
	if v := getenv("MAX_RESPONSE_BYTES"); v != "" {
		n, nerr := strconv.ParseInt(v, 10, 64)
		if nerr != nil || n <= 0 {
			return cfg, false, fmt.Errorf("MAX_RESPONSE_BYTES %q is not a positive integer", v)
		}
		cfg.MaxResponse = n
	}
	if v := getenv("PRINT_ANSWER"); v != "" {
		printAnswer, err = strconv.ParseBool(v)
		if err != nil || (v != "true" && v != "false") {
			return cfg, false, fmt.Errorf("PRINT_ANSWER %q must be true or false", v)
		}
	}
	return cfg, printAnswer, nil
}
