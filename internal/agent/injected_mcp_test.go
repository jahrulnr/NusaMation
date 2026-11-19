package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/mcp/browseruse"
	"github.com/jahrulnr/nusamation/internal/mcp/grants"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestInjectedServers(t *testing.T) {
	m := NewManager(nil, nil)
	if got := m.injectedServers(context.Background(), "cv"); len(got) != 0 {
		t.Fatalf("empty manager registered %+v", got)
	}
	g := grants.New("")
	specs := []InjectedMCP{
		{
			Name: "browser_use", Command: "/usr/bin/nusamation", Args: []string{"mcp", "run", "--browser-use"},
			Env: func(conv string) ([]acp.EnvVar, bool) {
				tok := g.Token(conv)
				if tok == "" {
					return nil, false
				}
				return []acp.EnvVar{
					{Name: browseruse.EnvURL, Value: "http://127.0.0.1:8787"},
					{Name: browseruse.EnvToken, Value: tok},
				}, true
			},
		},
		{
			Name: "nusamation_conversation", Command: "/usr/bin/nusamation", Args: []string{"mcp", "run", "--conversation"},
			Env: func(conv string) ([]acp.EnvVar, bool) {
				return []acp.EnvVar{{Name: "NUSAMATION_CONVERSATION_TOKEN", Value: conv}}, conv != ""
			},
		},
		{
			Name: "disabled", Command: "/usr/bin/nusamation",
			Env: func(string) ([]acp.EnvVar, bool) { return nil, false },
		},
	}
	m.EnableInjectedMCP(specs...)

	got := m.injectedServers(context.Background(), "cv_1")
	if len(got) != 2 {
		t.Fatalf("servers = %+v", got)
	}
	if got[0].Name != "browser_use" || got[0].Command != "/usr/bin/nusamation" || len(got[0].Args) != 3 || got[0].Args[0] != "mcp" || got[0].Args[1] != "run" || got[0].Args[2] != "--browser-use" {
		t.Fatalf("browser_use = %+v", got[0])
	}
	if got[0].Env[0].Name != browseruse.EnvURL || got[0].Env[0].Value != "http://127.0.0.1:8787" {
		t.Fatalf("url env = %+v", got[0].Env)
	}
	if got[0].Env[1].Value == "" || got[0].Env[1].Value != g.Token("cv_1") {
		t.Fatal("token was not the conversation grant")
	}
	if got[1].Name != "nusamation_conversation" || len(got[1].Args) != 3 || got[1].Args[0] != "mcp" || got[1].Args[1] != "run" || got[1].Args[2] != "--conversation" {
		t.Fatalf("conversation = %+v", got[1])
	}
	if len(m.injectedServers(context.Background(), "")) != 0 {
		t.Fatal("empty conversation should not receive injected servers")
	}

	replaced := replaceMCP([]acp.MCPServerConfig{{Name: "other"}, {Name: "browser_use", Command: "old"}}, got[0])
	if len(replaced) != 2 || replaced[1].Command != "/usr/bin/nusamation" {
		t.Fatalf("replace = %+v", replaced)
	}
}

// Agents that scope MCP servers by directory (OpenCode) reuse one child for
// every room in that directory. The injected server name must therefore be
// unique per room, or the first room's token answers for all later rooms.
func TestInjectedServersGetPerRoomNames(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "Test", Slug: "test", Command: "/bin/true", Cwd: t.TempDir(), Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.CreateConversation(ctx, ag.ID, "First", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateConversation(ctx, ag.ID, "Second", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetConversationLabel(ctx, first.ID, "eagle"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetConversationLabel(ctx, second.ID, "hawk"); err != nil {
		t.Fatal(err)
	}

	m := NewManager(st, nil)
	m.EnableInjectedMCP(InjectedMCP{
		Name: "nusamation_conversation", Command: "/usr/bin/nusamation",
		PerConversationName: true,
		Env: func(conv string) ([]acp.EnvVar, bool) {
			return []acp.EnvVar{{Name: "TOKEN", Value: conv}}, conv != ""
		},
	})
	gotFirst := m.injectedServers(ctx, first.ID)
	gotSecond := m.injectedServers(ctx, second.ID)
	if len(gotFirst) != 1 || len(gotSecond) != 1 {
		t.Fatalf("servers = %+v / %+v", gotFirst, gotSecond)
	}
	if gotFirst[0].Name != "nusamation_conversation_eagle" {
		t.Fatalf("first server name = %q", gotFirst[0].Name)
	}
	if gotSecond[0].Name != "nusamation_conversation_hawk" {
		t.Fatalf("second server name = %q", gotSecond[0].Name)
	}
	if gotFirst[0].Name == gotSecond[0].Name {
		t.Fatal("two rooms share one injected server name")
	}
	if gotFirst[0].Env[0].Value != first.ID || gotSecond[0].Env[0].Value != second.ID {
		t.Fatalf("tokens = %+v / %+v", gotFirst[0].Env, gotSecond[0].Env)
	}

	// An unlabeled room falls back to its unique conversation id.
	if err := st.SetConversationLabel(ctx, second.ID, ""); err != nil {
		t.Fatal(err)
	}
	fallback := m.injectedServers(ctx, second.ID)
	if len(fallback) != 1 || !strings.HasPrefix(fallback[0].Name, "nusamation_conversation_cv_") || fallback[0].Name == gotFirst[0].Name {
		t.Fatalf("fallback server name = %+v", fallback)
	}
}

func TestSanitizeMCPSuffix(t *testing.T) {
	cases := map[string]string{
		"eagle":   "eagle",
		" Cat-2 ": "cat_2",
		"a b!c":   "abc",
		"---":     "",
	}
	for in, want := range cases {
		if got := sanitizeMCPSuffix(in); got != want {
			t.Errorf("sanitizeMCPSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
