package builtin

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/console/ext"
)

func req(auth string) *http.Request {
	r, _ := http.NewRequest("GET", "http://x/", nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}

func TestTokenAuthReReadsTheFileWhenItChanges(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(p, []byte("first\n"), 0o600)
	a := &TokenAuth{Path: p}
	if _, err := a.Authenticate(req("Bearer first")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(req("bearer first")); err != nil {
		t.Fatal("the scheme is case-insensitive:", err)
	}
	_ = os.WriteFile(p, []byte("second-longer"), 0o600)
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(p, later, later)
	if _, err := a.Authenticate(req("Bearer first")); err == nil {
		t.Fatal("the old token still works after rotation")
	}
	if id, err := a.Authenticate(req("Bearer second-longer")); err != nil || id.Subject != Operator {
		t.Fatal(err, id)
	}
	_ = os.Remove(p)
	if _, err := a.Authenticate(req("Bearer second-longer")); err == nil {
		t.Fatal("a missing token file must refuse everybody")
	}
	_ = os.WriteFile(p, []byte("  \n"), 0o600)
	if _, err := a.Authenticate(req("Bearer ")); err == nil {
		t.Fatal("an empty token file must refuse everybody")
	}
}

func TestTokenAuthRefusals(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(p, []byte("secret"), 0o600)
	a := &TokenAuth{Path: p}
	for _, h := range []string{"", "Bearer", "Bearer secre", "Bearer secretx", "Basic secret", "secret", "Bearer " + strings.Repeat("a", 9000)} {
		if _, err := a.Authenticate(req(h)); err == nil {
			t.Errorf("%.20q accepted", h)
		}
	}
}

func TestRoleAuthz(t *testing.T) {
	op, viewer := RoleAuthz{}, RoleAuthz{Viewer: true}
	for _, a := range []ext.Action{ext.ActionRead, ext.ActionValidate, ext.ActionCreate, ext.ActionUpdate, ext.ActionDelete} {
		if !op.Authorize(context.Background(), ext.Identity{}, a, ext.Resource{}).Allowed {
			t.Errorf("operator refused %s", a)
		}
		d := viewer.Authorize(context.Background(), ext.Identity{}, a, ext.Resource{})
		if d.Allowed == a.Writes() || (!d.Allowed && d.Reason == "") {
			t.Errorf("viewer %s: %+v", a, d)
		}
	}
}

func TestLogAuditWritesOneJSONLine(t *testing.T) {
	var b bytes.Buffer
	LogAudit{Log: slog.New(slog.NewJSONHandler(&b, nil))}.Record(context.Background(), ext.AuditEvent{Subject: "console", Action: ext.ActionCreate,
		Resource: ext.Resource{Kind: "agentdefinition", Name: "x"}, Outcome: "ok", Status: 201, RequestID: "r1"})
	if strings.Count(b.String(), "\n") != 1 || !strings.Contains(b.String(), `"agent":"x"`) || !strings.Contains(b.String(), `"outcome":"ok"`) {
		t.Fatal(b.String())
	}
}
