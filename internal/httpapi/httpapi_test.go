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

func TestAuthCSRFAndConversationAPI(t *testing.T) {
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

	// Unauthenticated access is rejected.
	res, err := client.Get(ts.URL + "/api/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bootstrap status = %d", res.StatusCode)
	}
	_ = res.Body.Close()

	// Static app shell and security headers.
	res, err = client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "NusaMation") {
		t.Fatalf("index status=%d body=%q", res.StatusCode, body[:min(len(body), 100)])
	}
	if res.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP header")
	}

	// Login.
	loginBody, _ := json.Marshal(map[string]any{"password": "test-password-123", "remember": true})
	res, err = client.Post(ts.URL+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	var login struct {
		CSRF string `json:"csrf"`
		OK   bool   `json:"ok"`
	}
	if err := json.NewDecoder(res.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 || !login.OK || login.CSRF == "" {
		t.Fatalf("login status=%d body=%#v", res.StatusCode, login)
	}

	// CSRF is required for mutations.
	createBody, _ := json.Marshal(map[string]any{"agentId": ag.ID})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/conversations", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("mutation without CSRF status = %d", res.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/conversations", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", login.CSRF)
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		Conversation store.Conversation `json:"conversation"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated || created.Conversation.ID == "" {
		t.Fatalf("create status=%d body=%#v", res.StatusCode, created)
	}

	res, err = client.Get(ts.URL + "/api/conversations/" + created.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Conversation store.Conversation `json:"conversation"`
		Messages     []messageView      `json:"messages"`
	}
	if err := json.NewDecoder(res.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 || detail.Conversation.ID != created.Conversation.ID {
		t.Fatalf("detail status=%d body=%#v", res.StatusCode, detail)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
