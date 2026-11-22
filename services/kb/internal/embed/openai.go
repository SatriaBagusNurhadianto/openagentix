package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OpenAIConfig configures an OpenAI-compatible embeddings client.
type OpenAIConfig struct {
	// URL is the endpoint, or a base URL ("http://host/v1") to which
	// "/embeddings" is appended.
	URL    string
	Model  string
	APIKey string
	Dim    int

	BatchSize        int           // texts per request; default 64
	Timeout          time.Duration // per request; default 30s
	RetryDelay       time.Duration // pause before the single retry; default 500ms
	MaxResponseBytes int64         // default 16 MiB
	HTTPClient       *http.Client  // optional
}

// OpenAI calls an OpenAI-compatible /embeddings endpoint.
type OpenAI struct {
	cfg      OpenAIConfig
	endpoint string
	client   *http.Client
}

// NewOpenAI validates cfg and returns the client.
func NewOpenAI(cfg OpenAIConfig) (*OpenAI, error) {
	if cfg.URL == "" {
		return nil, errors.New("embeddings URL is required")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("embeddings URL %q must be an http(s) URL", cfg.URL)
	}
	if cfg.Model == "" {
		return nil, errors.New("embeddings model is required")
	}
	if cfg.Dim <= 0 {
		return nil, errors.New("embeddings dimension must be > 0")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 64
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 500 * time.Millisecond
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 16 << 20
	}
	endpoint := strings.TrimRight(cfg.URL, "/")
	if !strings.HasSuffix(endpoint, "/embeddings") {
		endpoint += "/embeddings"
	}
	c := cfg.HTTPClient
	if c == nil {
		c = &http.Client{}
	}
	return &OpenAI{cfg: cfg, endpoint: endpoint, client: c}, nil
}

func (o *OpenAI) Dimensions() int { return o.cfg.Dim }

// Embed splits texts into batches and returns vectors in input order.
func (o *OpenAI) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += o.cfg.BatchSize {
		end := min(start+o.cfg.BatchSize, len(texts))
		vecs, err := o.batch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// EmbedQuery embeds one text with a single attempt (no retry), bounded by ctx
// and the per-request timeout. It is meant for latency-sensitive query paths.
func (o *OpenAI) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(map[string]any{"model": o.cfg.Model, "input": []string{text}})
	if err != nil {
		return nil, err
	}
	vecs, err := o.once(ctx, body, 1)
	var re retryableError
	if errors.As(err, &re) {
		err = re.error
	}
	if err != nil {
		return nil, fmt.Errorf("embeddings: %w", err)
	}
	return vecs[0], nil
}

type retryableError struct{ error }

func (o *OpenAI) batch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": o.cfg.Model, "input": texts})
	if err != nil {
		return nil, err
	}
	vecs, err := o.once(ctx, body, len(texts))
	var re retryableError
	if errors.As(err, &re) {
		select {
		case <-time.After(o.cfg.RetryDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		vecs, err = o.once(ctx, body, len(texts))
	}
	if err != nil {
		if errors.As(err, &re) {
			err = re.error
		}
		return nil, fmt.Errorf("embeddings: %w", err)
	}
	return vecs, nil
}

func (o *OpenAI) once(parent context.Context, body []byte, n int) ([][]float32, error) {
	ctx, cancel := context.WithTimeout(parent, o.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		if parent.Err() == nil { // not cancelled by the caller: worth one retry
			return nil, retryableError{errors.New("request failed: " + scrub(err))}
		}
		return nil, errors.New("request failed: " + scrub(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, o.cfg.MaxResponseBytes+1))
	if err != nil {
		return nil, errors.New("reading response: " + scrub(err))
	}
	if int64(len(data)) > o.cfg.MaxResponseBytes {
		return nil, errors.New("response too large")
	}
	if resp.StatusCode != http.StatusOK {
		e := fmt.Errorf("status %d: %s", resp.StatusCode, snippet(data))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, retryableError{e}
		}
		return nil, e
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, errors.New("invalid JSON response")
	}
	if len(parsed.Data) != n {
		return nil, fmt.Errorf("expected %d embeddings, got %d", n, len(parsed.Data))
	}
	out := make([][]float32, n)
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= n || out[d.Index] != nil {
			return nil, fmt.Errorf("invalid embedding index %d", d.Index)
		}
		if len(d.Embedding) != o.cfg.Dim {
			return nil, fmt.Errorf("embedding has dimension %d, configured %d", len(d.Embedding), o.cfg.Dim)
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}

// scrub strips the URL (which may embed credentials) from transport errors.
func scrub(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
