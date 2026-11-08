package browseruse

import (
	"path/filepath"
	"testing"
)

func TestGrantTokensSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	g := NewGrants(path)
	tok := g.Token("cv_a")
	if tok == "" {
		t.Fatal("empty token")
	}
	restarted := NewGrants(path)
	if id, ok := restarted.Conversation(tok); !ok || id != "cv_a" {
		t.Fatalf("token not restored: id=%q ok=%v", id, ok)
	}
	restarted.Revoke("cv_a")
	if _, ok := NewGrants(path).Conversation(tok); ok {
		t.Fatal("revoked token restored")
	}
}

func TestGrantTokenIsStable(t *testing.T) {
	g := NewGrants("")
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
