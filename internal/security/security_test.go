package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafePath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "sub", "file.txt")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SafePath(root, inside, true)
	if err != nil || got != inside {
		t.Fatalf("SafePath inside = %q, %v", got, err)
	}
	if _, err := SafePath(root, filepath.Join(root, "..", "escape"), false); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := SafePath(root, "/etc/passwd", true); err == nil {
		t.Fatal("expected absolute outside path to be rejected")
	}
}

func TestSafePathSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if _, err := SafePath(root, filepath.Join(link, "file"), false); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestSealerRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	s, err := LoadOrCreateSealer(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal([]byte("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if sealed == "hello world" {
		t.Fatal("secret was not encrypted")
	}
	plain, err := s.Open(sealed)
	if err != nil || string(plain) != "hello world" {
		t.Fatalf("Open = %q, %v", plain, err)
	}
	s2, err := LoadOrCreateSealer(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Open(sealed); err != nil {
		t.Fatalf("same key file should decrypt: %v", err)
	}
}

func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password did not match")
	}
	if CheckPassword(hash, "wrong") {
		t.Fatal("wrong password matched")
	}
}
