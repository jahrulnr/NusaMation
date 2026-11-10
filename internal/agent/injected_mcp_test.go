package agent

import (
	"testing"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/mcp/browseruse"
	"github.com/jahrulnr/nusamation/internal/mcp/grants"
)

func TestInjectedServers(t *testing.T) {
	m := NewManager(nil, nil)
	if got := m.injectedServers("cv"); len(got) != 0 {
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

	got := m.injectedServers("cv_1")
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
	if len(m.injectedServers("")) != 0 {
		t.Fatal("empty conversation should not receive injected servers")
	}

	replaced := replaceMCP([]acp.MCPServerConfig{{Name: "other"}, {Name: "browser_use", Command: "old"}}, got[0])
	if len(replaced) != 2 || replaced[1].Command != "/usr/bin/nusamation" {
		t.Fatalf("replace = %+v", replaced)
	}
}
