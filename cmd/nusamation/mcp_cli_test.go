package main

import (
	"context"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestMCPRunSelection(t *testing.T) {
	if err := mcpCmd([]string{"run", "--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
	for _, args := range [][]string{{"run"}, {"run", "--browser-use", "--conversation"}, {"run", "--unknown"}} {
		if err := mcpCmd(args); err == nil {
			t.Fatalf("expected usage error for %v", args)
		}
	}
	t.Setenv("NUSAMATION_BROWSER_USE_URL", "")
	t.Setenv("NUSAMATION_CONVERSATION_URL", "")
	for _, mode := range []string{"--browser-use", "--conversation"} {
		if err := mcpCmd([]string{"run", mode}); err == nil || !strings.Contains(err.Error(), mode) {
			t.Fatalf("%s: %v", mode, err)
		}
	}
}

func TestMCPCLILifecycle(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		args = append(args, "--data-dir", dir)
		if err := mcpCmd(args); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "--name", "demo", "--command", "example", "--arg", "--serve", "--env", "TOKEN=first", "--header", "Authorization=abc")
	run("set", "demo", "--env", "TOKEN=second", "--env", "OTHER=value", "--header-rm", "Authorization")
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.ListMCP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Command != "example" || len(list[0].Args) != 1 || len(list[0].Env) != 2 || list[0].Env[0].Value != "second" || len(list[0].Headers) != 0 {
		t.Fatalf("unexpected MCP state: %#v", list)
	}
	var sealed string
	if err := st.DB().QueryRowContext(context.Background(), "SELECT env_enc FROM mcp_servers WHERE id=?", list[0].ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "second") || strings.Contains(sealed, "value") {
		t.Fatal("environment stored as plaintext")
	}
	st.Close()
	run("remove", "demo")
	st, err = openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	list, err = st.ListMCP(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("remove: %v count=%d", err, len(list))
	}
}
