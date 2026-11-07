package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func stubBins(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if runtime.GOOS == "windows" {
			n += ".exe"
		}
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func TestDetectFindsKnownCLIs(t *testing.T) {
	stubBins(t, "devin", "gemini", "codex-acp")
	found := Detect()
	bySlug := map[string]DetectedCLI{}
	for _, d := range found {
		bySlug[d.Slug] = d
	}
	if len(found) != 3 {
		t.Fatalf("Detect() found %d CLIs: %#v", len(found), found)
	}
	if bySlug["devin"].Command == "" || len(bySlug["devin"].Args) != 1 || bySlug["devin"].Args[0] != "acp" {
		t.Fatalf("devin = %#v", bySlug["devin"])
	}
	if bySlug["gemini"].Args[0] != "--acp" {
		t.Fatalf("gemini args = %#v", bySlug["gemini"].Args)
	}
	// codex-acp binary wins over the codex fallback candidate.
	if strings.TrimSuffix(filepath.Base(bySlug["codex"].Command), ".exe") != "codex-acp" || len(bySlug["codex"].Args) != 0 {
		t.Fatalf("codex = %#v", bySlug["codex"])
	}
	// Catalog order is preserved.
	if found[0].Slug != "devin" || found[1].Slug != "codex" || found[2].Slug != "gemini" {
		t.Fatalf("order = %#v", found)
	}
}

func TestDetectPrefersFirstCandidate(t *testing.T) {
	stubBins(t, "claude-code-acp", "codex")
	found := Detect()
	if len(found) != 1 {
		t.Fatalf("Detect() = %#v", found)
	}
	bySlug := map[string]DetectedCLI{}
	for _, d := range found {
		bySlug[d.Slug] = d
	}
	if strings.TrimSuffix(filepath.Base(bySlug["claude"].Command), ".exe") != "claude-code-acp" {
		t.Fatalf("claude = %#v", bySlug["claude"])
	}
	if _, ok := bySlug["codex"]; ok {
		t.Fatal("plain codex CLI is not an ACP server and must not be seeded")
	}
}

func TestDetectEmptyPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if found := Detect(); len(found) != 0 {
		t.Fatalf("Detect() = %#v", found)
	}
}
