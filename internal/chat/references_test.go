package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/store"
)

func TestSelectedReferencesAreMappedWithoutParsingPlainText(t *testing.T) {
	text := "🌻 @mcp @mcp"
	first := strings.Index(text, "@mcp")
	refs := []store.PromptReference{
		{Kind: "folder", ID: "/project/mcp", Label: "@mcp", Start: first, End: first + 4},
		{Kind: "mcp", ID: "mcp", Label: "@mcp", Start: first + 5, End: first + 9},
	}
	if err := validatePromptReferences(text, refs); err != nil {
		t.Fatal(err)
	}
	got, blocks, command := promptReferences(text, refs)
	if got != text || command || len(blocks) != 1 || !strings.Contains(blocks[0].Text, "folder /project/mcp") || !strings.Contains(blocks[0].Text, "MCP server mcp") {
		t.Fatalf("text=%q blocks=%+v command=%v", got, blocks, command)
	}
	got, blocks, command = promptReferences(text, nil)
	if got != text || len(blocks) != 0 || command {
		t.Fatalf("plain text changed: %q %+v", got, blocks)
	}
	refs[1].Start--
	if validatePromptReferences(text, refs) == nil {
		t.Fatal("overlapping reference accepted")
	}
}

func TestSelectedCommandAndFileBecomeACPContent(t *testing.T) {
	text := "please @review #main.go"
	refs := []store.PromptReference{
		{Kind: "command", ID: "review", Label: "@review", Start: 7, End: 14},
		{Kind: "file", ID: filepath.Join(string(filepath.Separator), "project", "main.go"), Label: "#main.go", Start: 15, End: 23},
	}
	if err := validatePromptReferences(text, refs); err != nil {
		t.Fatal(err)
	}
	prompt, blocks, command := promptReferences(text, refs)
	if prompt != "/review please  #main.go" || !command || len(blocks) != 2 || blocks[0].Type != "resource_link" {
		t.Fatalf("prompt=%q blocks=%+v command=%v", prompt, blocks, command)
	}
}

func TestSlashReferenceDoesNotBecomeCommand(t *testing.T) {
	ref := store.PromptReference{Kind: "file", ID: "/project/readme", Label: "/readme", Start: 0, End: 7}
	if promptIsCommand("/readme", []store.PromptReference{ref}) {
		t.Fatal("selected file classified as command")
	}
	if !promptIsCommand("/readme", nil) {
		t.Fatal("plain slash text should reach agent as command text")
	}
	prompt, blocks, command := promptReferences("/readme", []store.PromptReference{ref})
	if prompt != "Reference: /readme" || command || len(blocks) != 2 {
		t.Fatalf("prompt=%q command=%v blocks=%+v", prompt, command, blocks)
	}
}

func TestReferenceSearchSeparatesSameNameAndUsesProjectRoots(t *testing.T) {
	svc, st, _, ag := newProjectEnv(t, "")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mcp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mcp.md"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(root, ".agents", "skills", "mcp")
	if err := os.MkdirAll(skill, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: mcp\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMCP(context.Background(), store.MCPServer{Name: "mcp", Transport: "stdio", Command: "echo", Enabled: true, EnabledSet: true}); err != nil {
		t.Fatal(err)
	}
	project, err := svc.CreateProject(context.Background(), "refs", []string{root}, true)
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := svc.ReferenceCandidates(context.Background(), "", project.ID, ag.ID, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, item := range items {
		kinds[item.Kind] = true
	}
	if !kinds["folder"] || !kinds["file"] || !kinds["mcp"] || !kinds["skill"] {
		t.Fatalf("candidates: %+v", items)
	}
}

func TestSelectedCommandStoredAsTypedAndDispatchedAsACPCommand(t *testing.T) {
	e := newAfterToolsEnv(t)
	refs := []store.PromptReference{{Kind: "command", ID: "compact", Label: "@compact", Start: 0, End: 8}}
	msg, _, err := e.svc.Submit(context.Background(), SubmitInput{ConversationID: e.conv.ID, Text: "@compact", Channel: "web", References: refs})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Text != "@compact" || len(referencesFromMessage(msg)) != 1 {
		t.Fatalf("stored message: %+v", msg)
	}
	waitFor(t, func() bool {
		for _, line := range e.trace() {
			if strings.HasPrefix(line, "prompt_start ") && strings.HasSuffix(line, " /compact") {
				return true
			}
		}
		return false
	}, "selected command to reach ACP as /compact")
}
