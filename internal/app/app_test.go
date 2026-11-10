package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/store"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{Addr: "127.0.0.1:8787", DataDir: t.TempDir()}
}

func TestSeedWorkspaceWritesAgentsWithOpsSection(t *testing.T) {
	cfg := testConfig(t)
	if err := os.MkdirAll(cfg.WorkspaceDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := seedWorkspace(cfg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(cfg.WorkspaceDir(), "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, opsMarker) {
		t.Fatal("AGENTS.md missing ops section")
	}
	if !strings.Contains(s, cfg.DocsDir()) || !strings.Contains(s, cfg.DataDir) {
		t.Fatal("ops section missing resolved paths")
	}
	if _, err := os.Stat(filepath.Join(cfg.WorkspaceDir(), "README.md")); err != nil {
		t.Fatal("workspace README.md not seeded")
	}
}

func TestSeedWorkspaceAppendsOpsOnce(t *testing.T) {
	cfg := testConfig(t)
	ws := cfg.WorkspaceDir()
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(ws, "AGENTS.md")
	if err := os.WriteFile(agents, []byte("# Custom\n\nuser notes, no trailing newline"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := seedWorkspace(cfg); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(agents)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "user notes, no trailing newline") {
		t.Fatal("user content lost")
	}
	if !strings.Contains(s, "newline\n\n"+opsMarker) {
		t.Fatal("ops section not separated by a blank line")
	}
	if strings.Count(s, opsMarker) != 1 {
		t.Fatal("ops section appended more than once")
	}
}

func TestSeedDocsWritesAndSyncs(t *testing.T) {
	cfg := testConfig(t)
	dir := cfg.DocsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := seedDocs(dir); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(dir, "README.md")
	if _, err := os.Stat(readme); err != nil {
		t.Fatal("docs/README.md not seeded")
	}
	info1, _ := os.Stat(readme)

	// Identical content is left untouched (mtime preserved).
	if err := seedDocs(dir); err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(readme)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatal("unchanged doc rewritten")
	}

	// Drifted content is re-synced.
	if err := os.WriteFile(readme, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := seedDocs(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(readme)
	if string(b) == "stale" {
		t.Fatal("drifted doc not re-synced")
	}

	// Files not shipped by the binary (e.g. user notes) are preserved.
	stale := filepath.Join(dir, "user-notes.md")
	if err := os.WriteFile(stale, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := seedDocs(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(stale)
	if err != nil || string(b) != "keep me" {
		t.Fatal("user file in docs dir was removed or modified")
	}
}

func TestMCPFingerprintTracksConfiguration(t *testing.T) {
	cfg := testConfig(t)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	initial, err := a.mcpFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server, err := a.store.CreateMCP(ctx, store.MCPServer{Name: "fixture", Command: "fixture", Enabled: true, Env: store.SecretList{{Name: "TOKEN", Value: "first"}}})
	if err != nil {
		t.Fatal(err)
	}
	added, err := a.mcpFingerprint(ctx)
	if err != nil || added == initial {
		t.Fatal("MCP addition not detected", err)
	}
	_, err = a.store.UpdateMCP(ctx, server.ID, store.MCPServer{Enabled: true, EnabledSet: true, Env: store.SecretList{{Name: "TOKEN", Value: "second"}}})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := a.mcpFingerprint(ctx)
	if err != nil || changed == added {
		t.Fatal("secret update not detected", err)
	}
	if err := a.store.DeleteMCP(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	removed, err := a.mcpFingerprint(ctx)
	if err != nil || removed != initial {
		t.Fatal("MCP removal not detected", err)
	}
}
