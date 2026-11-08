package agent

import (
	"testing"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/browseruse"
)

func TestBrowserUseServerConfig(t *testing.T) {
	m := NewManager(nil, nil)
	if _, ok := m.browserUseServer("cv"); ok {
		t.Fatal("disabled hook registered a server")
	}
	g := browseruse.NewGrants()
	m.EnableBrowserUse(g, "/usr/bin/nusamation", "http://127.0.0.1:8787/")
	cfg, ok := m.browserUseServer("cv_1")
	if !ok || cfg.Name != "browser_use" || cfg.Command != "/usr/bin/nusamation" {
		t.Fatalf("cfg=%+v ok=%v", cfg, ok)
	}
	if len(cfg.Args) != 1 || cfg.Args[0] != "browser-use" {
		t.Fatalf("args=%v", cfg.Args)
	}
	if cfg.Env[0].Name != browseruse.EnvURL || cfg.Env[0].Value != "http://127.0.0.1:8787" {
		t.Fatalf("url env=%+v", cfg.Env)
	}
	if cfg.Env[1].Value == "" || cfg.Env[1].Value != g.Token("cv_1") {
		t.Fatal("token was not the conversation grant")
	}
	replaced := replaceMCP([]acp.MCPServerConfig{{Name: "other"}, {Name: "browser_use", Command: "old"}}, cfg)
	if len(replaced) != 2 || replaced[1].Command != "/usr/bin/nusamation" {
		t.Fatalf("replace = %+v", replaced)
	}
}
