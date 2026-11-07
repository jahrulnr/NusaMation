package browser

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/store"
)

func testPick(id, sessionID string) *Pick {
	return &Pick{
		ID: id, SessionID: sessionID, ConversationID: "conv1", Marker: 2,
		Descriptor: store.AnnotationDescriptor{Selector: "div.x", TagName: "div", PageURL: "https://ex.test/"},
		Upload:     store.Upload{ID: "up-" + id, Name: "p.png", MIME: "image/png", Size: 3},
		CreatedAt:  time.Now().UnixMilli(),
	}
}

func TestPickFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := testPick("p1", "s1")
	if err := savePickFile(dir, p); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadPickFile(dir, "p1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.ID != p.ID || got.SessionID != "s1" || got.ConversationID != "conv1" || got.Marker != 2 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.Descriptor.Selector != "div.x" || got.Upload.ID != "up-p1" {
		t.Fatalf("descriptor/upload not restored: %+v", got)
	}
	if _, err := loadPickFile(dir, "missing"); err == nil {
		t.Fatal("expected error for missing pick")
	}
}

// Re-saving the same pick id exercises rename-over — which os.Rename cannot
// do on Windows without removing the destination first (fsutil handles it).
func TestPickFileOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := testPick("p1", "s1")
	if err := savePickFile(dir, p); err != nil {
		t.Fatalf("first save: %v", err)
	}
	p.Marker = 9
	if err := savePickFile(dir, p); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := loadPickFile(dir, "p1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Marker != 9 {
		t.Fatalf("marker = %d, want 9", got.Marker)
	}
}

func TestPickFileRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"", "..", "../x", "a/b", `a\b`} {
		if _, err := loadPickFile(dir, id); err == nil {
			t.Fatalf("loadPickFile(%q) accepted", id)
		}
	}
}

// The restart flow: manager A dies, manager B resolves the same pick from disk.
func TestPickSurvivesManagerRestart(t *testing.T) {
	dir := t.TempDir()
	m1 := &Manager{log: slog.Default(), picks: map[string]*Pick{}, picksDir: dir}
	m1.addPick(testPick("p1", "s1"))

	m2 := &Manager{log: slog.Default(), picks: map[string]*Pick{}, picksDir: dir}
	got, ok := m2.Pick("p1")
	if !ok {
		t.Fatal("pick did not survive restart")
	}
	if got.Upload.ID != "up-p1" || got.Descriptor.PageURL != "https://ex.test/" {
		t.Fatalf("rehydrated pick incomplete: %+v", got)
	}
}

func TestDropSessionPicksDeletesFiles(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{log: slog.Default(), picks: map[string]*Pick{}, picksDir: dir}
	m.addPick(testPick("p1", "s1"))
	m.addPick(testPick("p2", "s2"))
	m.dropSessionPicks("s1")
	if _, ok := m.Pick("p1"); ok {
		t.Fatal("p1 should be gone after session close")
	}
	if _, ok := m.Pick("p2"); !ok {
		t.Fatal("p2 belongs to another session and must survive")
	}
}

func TestSweepPickFiles(t *testing.T) {
	dir := t.TempDir()
	stale := testPick("old", "s1")
	stale.CreatedAt = time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
	fresh := testPick("new", "s1")
	for _, p := range []*Pick{stale, fresh} {
		if err := savePickFile(dir, p); err != nil {
			t.Fatalf("save %s: %v", p.ID, err)
		}
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	if err := sweepPickFiles(dir, cutoff); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.json")); !os.IsNotExist(err) {
		t.Fatal("stale pick not swept")
	}
	if _, err := os.Stat(filepath.Join(dir, "new.json")); err != nil {
		t.Fatal("fresh pick swept by mistake")
	}
}

func TestPickFileMalformedSkipped(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{nope"), 0o600)
	if err := deleteSessionPickFiles(dir, "s1"); err != nil {
		t.Fatalf("malformed file should not fail the sweep: %v", err)
	}
}
