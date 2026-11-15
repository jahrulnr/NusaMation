//go:build windows

package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/store"
)

// These are the path spellings real agents produce on Windows: the Markdown
// "/absolute/path" habit yields /C:/..., tool output often uses backslashes,
// and the profile directory contains a space.
func TestResolveOutboundPathWindowsSpellings(t *testing.T) {
	svc, ws, _ := testOutboundService(t)
	dir := filepath.Join(ws, "My Files")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	shot := filepath.Join(dir, "shot one.png")
	if err := os.WriteFile(shot, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(shot)
	if err != nil {
		t.Fatal(err)
	}
	a := &activeTurn{conv: store.Conversation{Cwd: ws}}
	roots := svc.allowedOutboundRoots(a)
	slashed := filepath.ToSlash(resolved)
	backslashed := strings.ReplaceAll(slashed, "/", `\`)
	refs := []string{
		resolved,             // native C:\Users\Some Name\shot one.png
		slashed,              // C:/Users/Some Name/shot one.png
		"/" + slashed,        // /C:/Users/Some Name/shot one.png (Markdown form)
		backslashed,          // C:\Users\Some Name\shot one.png
		"/" + backslashed,    // \C:\Users\Some Name\shot one.png
		"file:///" + slashed, // file:///C:/Users/Some Name/shot one.png
		"/" + strings.ReplaceAll(slashed, " ", "%20"), // percent-encoded Markdown
	}
	for _, ref := range refs {
		p, ok := svc.resolveOutboundPath(ref, roots)
		if !ok || !strings.EqualFold(p, resolved) {
			t.Errorf("spelling %q resolved to %q ok=%v, want %q", ref, p, ok, resolved)
		}
	}
	if _, ok := svc.resolveOutboundPath(filepath.Join(ws, "missing.png"), roots); ok {
		t.Error("missing file must stay refused")
	}
}

// A Windows reply attachment must reach outbound collection and leave the
// delivered text, which is the regression behind "the image never arrived".
func TestCollectOutboundFromTextWindowsReply(t *testing.T) {
	svc, ws, _ := testOutboundService(t)
	dir := filepath.Join(ws, "My Files")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	shot := filepath.Join(dir, "shot one.png")
	if err := os.WriteFile(shot, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(shot)
	if err != nil {
		t.Fatal(err)
	}
	slashed := filepath.ToSlash(resolved)
	reply := "foto: ![shot](" + resolved + ") dan [cadangan](/" + slashed + ")"
	a := &activeTurn{conv: store.Conversation{Cwd: ws}, turn: store.Turn{ID: "turn_x"}}
	a.assistantAll.WriteString(reply)
	svc.collectOutboundFromText(a)
	got := a.outboundSnapshot()
	if len(got) != 1 || !strings.EqualFold(got[0].Path, resolved) || string(got[0].Data) != "png-bytes" {
		t.Fatalf("windows attachment not collected: %#v", got)
	}
	text := formatDeliveredText(reply, a.outboundRefs())
	if strings.Contains(strings.ToLower(text), "shot one.png") || strings.Contains(text, "](") {
		t.Fatalf("delivered text kept the reference: %q", text)
	}
}
