package auth

import (
	"context"
	"crypto/sha256"
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

const jwtShaped = "aaa.bbb.ccc"

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
