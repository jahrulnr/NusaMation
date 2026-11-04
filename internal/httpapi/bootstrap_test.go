package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/realtime"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

// The bootstrap payload must only contain the public view of MCP servers:
// env and header names may be listed, but never their secret values.
func TestBootstrapDoesNotLeakMCPSecrets(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	if _, _, err := auth.EnsureBootstrap(context.Background(), st, "test-password-123"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "API Agent", Slug: "api-agent", Command: "/bin/echo", Cwd: dir, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "super-secret-mcp-token-xyz"
	if _, err := st.CreateMCP(context.Background(), store.MCPServer{
		AgentID: ag.ID, Name: "secret-mcp", Transport: "stdio", Command: "/bin/echo",
		Env:     store.SecretList{{Name: "API_KEY", Value: secret}},
		Headers: store.SecretList{{Name: "Authorization", Value: secret}},
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	hub := realtime.NewHub(logger)
	agentMgr := agent.NewManager(st, logger)
	chanMgr := channels.NewManager(hub, logger)
	svc := chat.New(chat.Options{
		Store: st, Agents: agentMgr, Channels: chanMgr, Events: hub, Log: logger,
		UploadDir: filepath.Join(dir, "uploads"), Workspace: dir,
	})
	agentMgr.SetSink(svc)
	cfg := config.Config{Addr: "127.0.0.1:0", DataDir: dir}
	srv, err := New(Options{Config: cfg, Store: st, Chat: svc, Agents: agentMgr, Channels: chanMgr, Hub: hub, Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	loginBody, _ := json.Marshal(map[string]any{"password": "test-password-123"})
	res, err := client.Post(ts.URL+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", res.StatusCode)
	}

	res, err = client.Get(ts.URL + "/api/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap status = %d body=%s", res.StatusCode, body)
	}
	if strings.Contains(string(body), secret) {
		t.Fatal("bootstrap response leaks MCP secret value")
	}
	if !strings.Contains(string(body), "secret-mcp") || !strings.Contains(string(body), "API_KEY") {
		t.Fatalf("bootstrap response missing public MCP entry: %s", body)
	}
}
