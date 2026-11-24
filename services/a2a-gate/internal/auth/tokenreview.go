package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// TokenReviewClient calls the Kubernetes TokenReview API with the gate's own ServiceAccount (it needs the
// system:auth-delegator ClusterRole). It uses the plain REST API, so the gate has no client library.
type TokenReviewClient struct {
	BaseURL     string                 // https://<host>:<port>
	HTTP        *http.Client           // trusts the cluster CA
	BearerToken func() (string, error) // the gate's own token, read each call (the kubelet rotates it)
}

// InClusterTokenReview builds a client from the pod's ServiceAccount files and environment.
func InClusterTokenReview() (*TokenReviewClient, error) {
	const dir = "/var/run/secrets/kubernetes.io/serviceaccount/"
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not running in a cluster: KUBERNETES_SERVICE_HOST is not set")
	}
	ca, err := os.ReadFile(dir + "ca.crt")
	if err != nil {
		return nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("cluster CA has no certificate")
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return &TokenReviewClient{
		BaseURL: "https://" + host + ":" + port,
		HTTP: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}},
		BearerToken: func() (string, error) {
			b, err := os.ReadFile(dir + "token")
			return strings.TrimSpace(string(b)), err
		},
	}, nil
}

// Review asks the API server whether token is a valid token for audience.
func (c *TokenReviewClient) Review(ctx context.Context, token, audience string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"apiVersion": "authentication.k8s.io/v1", "kind": "TokenReview",
		"spec": map[string]any{"token": token, "audiences": []string{audience}},
	})
	own, err := c.BearerToken()
	if err != nil {
		return "", fmt.Errorf("read own token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/apis/authentication.k8s.io/v1/tokenreviews", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+own)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("tokenreview request: %w", errors.Unwrap(err)) // the URL error text carries no token, but keep it short
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("tokenreview: API server answered HTTP %d", resp.StatusCode)
	}
	var out struct {
		Status struct {
			Authenticated bool     `json:"authenticated"`
			Audiences     []string `json:"audiences"`
			User          struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("tokenreview: bad response: %w", err)
	}
	if !out.Status.Authenticated || !slices.Contains(out.Status.Audiences, audience) {
		return "", ErrUnauthenticated
	}
	return out.Status.User.Username, nil
}
