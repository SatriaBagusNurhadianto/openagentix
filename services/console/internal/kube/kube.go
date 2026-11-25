// Package kube is the smallest possible Kubernetes REST client: it sends a request built by this program, with the
// pod's ServiceAccount token (read again for every request, because the kubelet rotates it) and nothing else. It never
// takes a header from the browser's request, so there is no way to relay one.
package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const maxResponse = 16 << 20

// Client talks to one API server.
type Client struct {
	Base      string // https://host:port
	TokenFile string
	HTTP      *http.Client
}

// New builds a client. caFile may be empty (plain http, for tests, or the system roots).
func New(base, tokenFile, caFile string) (*Client, error) {
	tr := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, MaxIdleConnsPerHost: 4, IdleConnTimeout: 60 * time.Second}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("cluster CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("cluster CA file holds no certificate")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Client{Base: strings.TrimRight(base, "/"), TokenFile: tokenFile, HTTP: &http.Client{Transport: tr}}, nil
}

// Response is a status and a body (at most 16 MiB).
type Response struct {
	Status int
	Body   []byte
}

// Do sends one request. contentType is set only when there is a body.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, contentType string, body []byte) (Response, error) {
	u := c.Base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return Response{}, err
	}
	if c.TokenFile != "" {
		tok, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return Response{}, fmt.Errorf("service account token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "openagentix-console")
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return Response{}, err
	}
	return Response{Status: resp.StatusCode, Body: b}, nil
}
