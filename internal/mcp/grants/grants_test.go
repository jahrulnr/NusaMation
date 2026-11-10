package grants

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRawTokensSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	g := New(path)
	tok := g.Token("cv_a")
	if tok == "" {
		t.Fatal("empty token")
	}
	restarted := New(path)
	if id, ok := restarted.Conversation(tok); !ok || id != "cv_a" {
		t.Fatalf("token not restored: id=%q ok=%v", id, ok)
	}
	restarted.Revoke("cv_a")
	if _, ok := New(path).Conversation(tok); ok {
		t.Fatal("revoked token restored")
	}
}

func TestRawTokenIsStable(t *testing.T) {
	g := New("")
	a := g.Token("cv_a")
	b := g.Token("cv_b")
	if a == "" || b == "" || a == b {
		t.Fatalf("tokens a=%q b=%q", a, b)
	}
	if g.Token("cv_a") != a {
		t.Fatal("token changed")
	}
	id, ok := g.Conversation(a)
	if !ok || id != "cv_a" {
		t.Fatalf("conversation=%q ok=%v", id, ok)
	}
	if _, ok := g.Conversation("nope"); ok {
		t.Fatal("unknown token resolved")
	}
	g.Revoke("cv_a")
	if _, ok := g.Conversation(a); ok {
		t.Fatal("revoked token still resolved")
	}
}

func TestHashedFileDoesNotContainRawToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	g := NewHashed(path)
	tok := g.Token("cv_a")
	if tok == "" {
		t.Fatal("empty token")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), tok) {
		t.Fatalf("raw token persisted: %s", raw)
	}
	if !strings.Contains(string(raw), "sha256:") {
		t.Fatalf("digest missing: %s", raw)
	}
	restarted := NewHashed(path)
	if id, ok := restarted.Conversation(tok); !ok || id != "cv_a" {
		t.Fatalf("token not restored: id=%q ok=%v", id, ok)
	}
}

func TestHashedRestartMintsANewToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	first := NewHashed(path).Token("cv_a")
	restarted := NewHashed(path)
	second := restarted.Token("cv_a")
	if second == "" || second == first {
		t.Fatalf("expected a fresh token, first=%q second=%q", first, second)
	}
	if _, ok := restarted.Conversation(first); ok {
		t.Fatal("old raw token still resolves after a fresh issue")
	}
	if id, ok := restarted.Conversation(second); !ok || id != "cv_a" {
		t.Fatalf("new token not resolved: id=%q ok=%v", id, ok)
	}
}

func TestLegacyRawFileMigratesToHashed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	raw := `{"cv_a":"legacy-token"}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	g := NewHashed(path)
	if id, ok := g.Conversation("legacy-token"); !ok || id != "cv_a" {
		t.Fatalf("legacy token lost: id=%q ok=%v", id, ok)
	}
	g.Revoke("cv_a")
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "legacy-token") {
		t.Fatalf("legacy token left in file: %s", after)
	}
}

func TestRevokeHashedToken(t *testing.T) {
	g := NewHashed("")
	tok := g.Token("cv_a")
	g.Revoke("cv_a")
	if _, ok := g.Conversation(tok); ok {
		t.Fatal("revoked hashed token resolved")
	}
	if tok := g.Token("cv_a"); tok == "" {
		t.Fatal("token not reissued after revoke")
	}
}
