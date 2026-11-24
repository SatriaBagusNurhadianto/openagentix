package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/a2a-gate/internal/rules"
)

const rulesJSON = `{"callers":[
 {"name":"bridge","serviceAccount":{"namespace":"openagentix-gateway","name":"telegram-bridge"},"agents":["kagent/assistant"],"userId":{"prefix":"telegram:"}},
 {"name":"kourier-b","key":true,"agents":["kagent/assistant"],"userId":{"fixed":"kourier:b"}}
]}`

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

type fakeKeys map[string]string

func (f fakeKeys) Hashes() map[string]string { return f }

type fakeReviewer struct {
	mu    sync.Mutex
	calls int
	user  string
	err   error
}

func (f *fakeReviewer) Review(_ context.Context, token, audience string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.user, f.err
}

func newAuth(t *testing.T, rv Reviewer, keys Keys, now func() time.Time) *Authenticator {
	t.Helper()
	r, err := rules.Parse([]byte(rulesJSON))
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Rules: r, Audience: "openagentix-a2a", Keys: keys, Reviewer: rv, Now: now})
}

// jwtShaped is what a projected ServiceAccount token looks like to the pre-filter: audience, issuer and expiry present.
var jwtShaped = makeJWT(map[string]any{"aud": []string{"openagentix-a2a"}, "iss": "https://kubernetes.default.svc", "exp": time.Now().Add(time.Hour).Unix(), "sub": "a"})

// makeJWT builds an unsigned, JWT-shaped string with the given payload (the gate never checks the signature itself).
func makeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(b) + ".c2ln"
}

func TestStaticKey(t *testing.T) {
	rv := &fakeReviewer{}
	a := newAuth(t, rv, fakeKeys{"kourier-b": sum("s3cret-key")}, nil)
	c, err := a.Authenticate(context.Background(), "s3cret-key")
	if err != nil || c.Name != "kourier-b" {
		t.Fatalf("got %v %v", c, err)
	}
	if _, err := a.Authenticate(context.Background(), "wrong-key"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong key: %v", err)
	}
	if rv.calls != 0 {
		t.Fatal("a token that is not JWT-shaped must not cost a TokenReview")
	}
}

func TestStaticKeyForCallerWithoutKeyRuleIsRefused(t *testing.T) {
	// a hash file named like a ServiceAccount caller must not let a key stand in for the token
	a := newAuth(t, &fakeReviewer{}, fakeKeys{"bridge": sum("k"), "nobody": sum("n")}, nil)
	for _, k := range []string{"k", "n"} {
		if _, err := a.Authenticate(context.Background(), k); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("key %q: %v", k, err)
		}
	}
}

func TestServiceAccountToken(t *testing.T) {
	rv := &fakeReviewer{user: "system:serviceaccount:openagentix-gateway:telegram-bridge"}
	now := time.Unix(1000, 0)
	a := newAuth(t, rv, fakeKeys{}, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		c, err := a.Authenticate(context.Background(), jwtShaped)
		if err != nil || c.Name != "bridge" {
			t.Fatalf("got %v %v", c, err)
		}
	}
	if rv.calls != 1 {
		t.Fatalf("TokenReview calls = %d, want 1 (cached)", rv.calls)
	}
	now = now.Add(29 * time.Second)
	_, _ = a.Authenticate(context.Background(), jwtShaped)
	if rv.calls != 1 {
		t.Fatalf("still cached at 29 s, calls = %d", rv.calls)
	}
	now = now.Add(2 * time.Second)
	_, _ = a.Authenticate(context.Background(), jwtShaped)
	if rv.calls != 2 {
		t.Fatalf("cache must expire after 30 s, calls = %d", rv.calls)
	}
}

func TestServiceAccountFailures(t *testing.T) {
	cases := []struct {
		name string
		user string
		err  error
		want error
	}{
		{"not authenticated", "", ErrUnauthenticated, ErrUnauthenticated},
		{"api error", "", errors.New("boom"), ErrUnavailable},
		{"sa without rule", "system:serviceaccount:other:thing", nil, ErrUnknownCaller},
		{"not a service account", "alice", nil, ErrUnknownCaller},
	}
	for _, tc := range cases {
		a := newAuth(t, &fakeReviewer{user: tc.user, err: tc.err}, fakeKeys{}, nil)
		if _, err := a.Authenticate(context.Background(), jwtShaped); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, err, tc.want)
		}
	}
}

func TestRejectedTokenIsRememberedBriefly(t *testing.T) {
	rv := &fakeReviewer{err: ErrUnauthenticated}
	now := time.Unix(1000, 0)
	a := newAuth(t, rv, fakeKeys{}, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		_, _ = a.Authenticate(context.Background(), jwtShaped)
	}
	if rv.calls != 1 {
		t.Fatalf("calls = %d, want 1", rv.calls)
	}
	now = now.Add(6 * time.Second)
	_, _ = a.Authenticate(context.Background(), jwtShaped)
	if rv.calls != 2 {
		t.Fatalf("calls = %d, want 2 after the short negative TTL", rv.calls)
	}
}

func TestEmptyToken(t *testing.T) {
	a := newAuth(t, &fakeReviewer{}, fakeKeys{}, nil)
	if _, err := a.Authenticate(context.Background(), ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
}

func TestTokenReviewClient(t *testing.T) {
	var gotAuth, gotToken string
	var gotAud []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" {
			http.Error(w, "bad", 400)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		var in struct {
			Spec struct {
				Token     string   `json:"token"`
				Audiences []string `json:"audiences"`
			} `json:"spec"`
		}
		_ = json.Unmarshal(b, &in)
		gotToken, gotAud = in.Spec.Token, in.Spec.Audiences
		switch in.Spec.Token {
		case "good":
			io.WriteString(w, `{"status":{"authenticated":true,"user":{"username":"system:serviceaccount:a:b"},"audiences":["openagentix-a2a"]}}`)
		case "wrong-aud":
			io.WriteString(w, `{"status":{"authenticated":true,"user":{"username":"system:serviceaccount:a:b"},"audiences":["https://kubernetes.default.svc"]}}`)
		case "bad":
			io.WriteString(w, `{"status":{"authenticated":false,"error":"invalid"}}`)
		default:
			http.Error(w, "forbidden", 403)
		}
	}))
	defer srv.Close()
	c := &TokenReviewClient{BaseURL: srv.URL, HTTP: srv.Client(), BearerToken: func() (string, error) { return "gate-sa-token", nil }}
	user, err := c.Review(context.Background(), "good", "openagentix-a2a")
	if err != nil || user != "system:serviceaccount:a:b" {
		t.Fatalf("good: %q %v", user, err)
	}
	if gotAuth != "Bearer gate-sa-token" || gotToken != "good" || len(gotAud) != 1 || gotAud[0] != "openagentix-a2a" {
		t.Fatalf("request: auth=%q token=%q aud=%v", gotAuth, gotToken, gotAud)
	}
	for _, tok := range []string{"bad", "wrong-aud"} {
		if _, err := c.Review(context.Background(), tok, "openagentix-a2a"); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s: %v", tok, err)
		}
	}
	if _, err := c.Review(context.Background(), "other", "openagentix-a2a"); err == nil || errors.Is(err, ErrUnauthenticated) {
		t.Errorf("an API error must not read as a bad token: %v", err)
	}
}

func TestDirKeys(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "kourier-b"), []byte(sum("k")+"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "short"), []byte("abc"), 0o600)
	os.WriteFile(filepath.Join(dir, "..data"), []byte(sum("z")), 0o600)
	os.Mkdir(filepath.Join(dir, "sub"), 0o700)
	k := &DirKeys{Dir: dir, TTL: time.Hour, Now: time.Now}
	h := k.Hashes()
	if len(h) != 1 || h["kourier-b"] != sum("k") {
		t.Fatalf("hashes = %v", h)
	}
	if (&DirKeys{Dir: filepath.Join(dir, "missing")}).Hashes() == nil {
		t.Fatal("a missing directory is an empty set, not nil")
	}
}

func TestDirKeysReportsUnreadableFilesOnce(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "locked"), []byte(sum("k")), 0o000)
	os.WriteFile(filepath.Join(dir, "bad"), []byte("not-a-hash"), 0o600)
	var got []string
	now := time.Unix(1000, 0)
	k := &DirKeys{Dir: dir, TTL: time.Second, Now: func() time.Time { return now }, OnProblem: func(name, why string) { got = append(got, name+": "+why) }}
	k.Hashes()
	now = now.Add(2 * time.Second)
	k.Hashes()
	if os.Getuid() == 0 {
		t.Skip("root can read a 0000 file")
	}
	if len(got) != 2 {
		t.Fatalf("problems = %v (want one for each file, reported once)", got)
	}
	for _, g := range got {
		if strings.Contains(g, sum("k")) {
			t.Fatal("a hash was reported")
		}
	}
}

func TestOversizedBearerIsRefusedWithoutReview(t *testing.T) {
	rv := &fakeReviewer{user: "system:serviceaccount:openagentix-gateway:telegram-bridge"}
	a := newAuth(t, rv, fakeKeys{}, nil)
	big := makeJWT(map[string]any{"aud": []string{"openagentix-a2a"}, "iss": "i", "exp": time.Now().Add(time.Hour).Unix(), "pad": strings.Repeat("x", MaxTokenBytes)})
	if len(big) <= MaxTokenBytes {
		t.Fatal("test token is not big enough")
	}
	if _, err := a.Authenticate(context.Background(), big); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("got %v", err)
	}
	if rv.calls != 0 {
		t.Fatalf("an oversized token cost %d TokenReviews", rv.calls)
	}
}

func TestPreFilterSpendsNoReviewOnImplausibleTokens(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	future, past := now.Add(time.Hour).Unix(), now.Add(-time.Hour).Unix()
	cases := map[string]string{
		"wrong audience":   makeJWT(map[string]any{"aud": []string{"other"}, "iss": "i", "exp": future}),
		"no audience":      makeJWT(map[string]any{"iss": "i", "exp": future}),
		"no issuer":        makeJWT(map[string]any{"aud": []string{"openagentix-a2a"}, "exp": future}),
		"expired":          makeJWT(map[string]any{"aud": []string{"openagentix-a2a"}, "iss": "i", "exp": past}),
		"no expiry":        makeJWT(map[string]any{"aud": []string{"openagentix-a2a"}, "iss": "i"}),
		"payload not b64":  "aaa.!!!.ccc",
		"payload not json": "aaa." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".ccc",
		"three dots":       "a.b.c.d",
	}
	for name, tok := range cases {
		rv := &fakeReviewer{user: "system:serviceaccount:openagentix-gateway:telegram-bridge"}
		a := newAuth(t, rv, fakeKeys{}, func() time.Time { return now })
		if _, err := a.Authenticate(context.Background(), tok); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s: got %v", name, err)
		}
		if rv.calls != 0 {
			t.Errorf("%s: %d TokenReviews, want 0", name, rv.calls)
		}
	}
	// the audience may be a plain string, and an array may hold other audiences too
	for name, aud := range map[string]any{"string": "openagentix-a2a", "array": []string{"x", "openagentix-a2a"}} {
		rv := &fakeReviewer{user: "system:serviceaccount:openagentix-gateway:telegram-bridge"}
		a := newAuth(t, rv, fakeKeys{}, func() time.Time { return now })
		tok := makeJWT(map[string]any{"aud": aud, "iss": "i", "exp": future})
		if c, err := a.Authenticate(context.Background(), tok); err != nil || c.Name != "bridge" {
			t.Errorf("aud as %s: %v %v", name, c, err)
		}
	}
}

type slowReviewer struct {
	mu          sync.Mutex
	calls       int
	inFlight    int
	maxInFlight int
	release     chan struct{}
	user        string
	err         error
}

func (s *slowReviewer) Review(ctx context.Context, token, _ string) (string, error) {
	s.mu.Lock()
	s.calls++
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	s.mu.Unlock()
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()
	return s.user, s.err
}

func uniqueJWT(i int) string {
	return makeJWT(map[string]any{"aud": []string{"openagentix-a2a"}, "iss": "i", "exp": time.Now().Add(time.Hour).Unix(), "n": i})
}

func TestSameTokenIsReviewedOnceWhileInFlight(t *testing.T) {
	rv := &slowReviewer{release: make(chan struct{}), user: "system:serviceaccount:openagentix-gateway:telegram-bridge"}
	a := newAuth(t, rv, fakeKeys{}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, err := a.Authenticate(context.Background(), jwtShaped); err != nil || c.Name != "bridge" {
				t.Errorf("%v %v", c, err)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(rv.release)
	wg.Wait()
	if rv.calls != 1 {
		t.Fatalf("TokenReview calls = %d, want 1 (singleflight)", rv.calls)
	}
}

func TestConcurrentReviewsAreLimited(t *testing.T) {
	rv := &slowReviewer{release: make(chan struct{}), err: ErrUnauthenticated}
	r, _ := rules.Parse([]byte(rulesJSON))
	a := New(Options{Rules: r, Audience: "openagentix-a2a", Keys: fakeKeys{}, Reviewer: rv, MaxReviews: 2})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, _ = a.Authenticate(context.Background(), uniqueJWT(i)) }(i)
	}
	time.Sleep(150 * time.Millisecond)
	close(rv.release)
	wg.Wait()
	if rv.maxInFlight > 2 || rv.maxInFlight == 0 {
		t.Fatalf("max concurrent reviews = %d, want 1..2", rv.maxInFlight)
	}
}

func TestReviewQueueGivesUpWhenFull(t *testing.T) {
	rv := &slowReviewer{release: make(chan struct{}), err: ErrUnauthenticated}
	r, _ := rules.Parse([]byte(rulesJSON))
	a := New(Options{Rules: r, Audience: "openagentix-a2a", Keys: fakeKeys{}, Reviewer: rv, MaxReviews: 1, ReviewWait: 50 * time.Millisecond})
	go func() { _, _ = a.Authenticate(context.Background(), uniqueJWT(1)) }()
	time.Sleep(50 * time.Millisecond)
	if _, err := a.Authenticate(context.Background(), uniqueJWT(2)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable (fail closed, no unbounded queue)", err)
	}
	close(rv.release)
}

func TestNegativeFloodDoesNotEvictPositiveEntries(t *testing.T) {
	good := &countingReviewer{}
	a := newAuth(t, good, fakeKeys{}, nil)
	if _, err := a.Authenticate(context.Background(), jwtShaped); err != nil {
		t.Fatal(err)
	}
	good.user, good.err = "", ErrUnauthenticated
	for i := 0; i < 5000; i++ {
		_, _ = a.Authenticate(context.Background(), uniqueJWT(i))
	}
	before := good.calls
	if _, err := a.Authenticate(context.Background(), jwtShaped); err != nil {
		t.Fatal(err)
	}
	if good.calls != before {
		t.Fatalf("the positive entry was evicted by a flood of bad tokens (%d extra TokenReviews)", good.calls-before)
	}
	a.mu.Lock()
	n := len(a.neg)
	a.mu.Unlock()
	if n > 2048 {
		t.Fatalf("negative cache holds %d entries, want a bounded number", n)
	}
}

type countingReviewer struct {
	mu    sync.Mutex
	calls int
	user  string
	err   error
}

func (c *countingReviewer) Review(context.Context, string, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return "", c.err
	}
	return "system:serviceaccount:openagentix-gateway:telegram-bridge", nil
}
