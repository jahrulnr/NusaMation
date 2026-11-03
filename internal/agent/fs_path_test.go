package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/acp"
)

func TestReadFileOutsideWorkspaceAllowedInBypass(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(outside, []byte("ops doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(acp.FSReadRequest{SessionID: "sess", Path: outside})
	h := &runtimeHandler{r: &Runtime{byACP: map[string]*Session{
		"sess": {
			roots: []string{root},
			modes: &acp.SessionModeState{CurrentModeID: "bypass"},
		},
	}}}
	got, rpcErr := h.readFile(raw)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	body, ok := got.(acp.FSReadResponse)
	if !ok || body.Content != "ops doc" {
		t.Fatalf("body = %#v", got)
	}
}

func TestReadFileOutsideWorkspaceDeniedWithoutBypass(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "README.md")
	raw, _ := json.Marshal(acp.FSReadRequest{SessionID: "sess", Path: outside})
	h := &runtimeHandler{r: &Runtime{byACP: map[string]*Session{
		"sess": {
			roots: []string{root},
			modes: &acp.SessionModeState{CurrentModeID: "ask"},
		},
	}}}
	_, rpcErr := h.readFile(raw)
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "outside the session workspace") {
		t.Fatalf("err = %#v", rpcErr)
	}
}

func TestBypassStillRejectsSensitiveWrite(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), ".env")
	raw, _ := json.Marshal(acp.FSWriteRequest{SessionID: "sess", Path: target, Content: "SECRET=1"})
	h := &runtimeHandler{r: &Runtime{byACP: map[string]*Session{
		"sess": {
			roots: []string{root},
			modes: &acp.SessionModeState{CurrentModeID: "bypass"},
		},
	}}}
	_, rpcErr := h.writeFile(raw)
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "secret") {
		t.Fatalf("err = %#v", rpcErr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("sensitive file was written")
	}
}

func TestBypassModeFromConfigWhenModesMissing(t *testing.T) {
	s := &Session{configOptions: json.RawMessage(`[{"id":"mode","category":"mode","currentValue":"bypass"}]`)}
	if !s.bypassesPermissions() {
		t.Fatal("config mode bypass was ignored")
	}
	s.configOptions = json.RawMessage(`[{"id":"mode","category":"mode","currentValue":"ask"}]`)
	if s.bypassesPermissions() {
		t.Fatal("ask mode was treated as bypass")
	}
}
