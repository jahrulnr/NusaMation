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

	userMsg := store.Message{ConversationID: created.Conversation.ID, Role: "user", Text: "first"}
	if err := st.AppendMessage(context.Background(), &userMsg); err != nil {
		t.Fatal(err)
	}
	turn, err := st.CreateTurn(context.Background(), created.Conversation.ID, userMsg.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	agentMsg := store.Message{ConversationID: created.Conversation.ID, TurnID: turn.ID, Role: "assistant", Text: "answer"}
	if err := st.AppendMessage(context.Background(), &agentMsg); err != nil {
		t.Fatal(err)
	}
	res, err = client.Get(ts.URL + "/api/conversations/" + created.Conversation.ID + "/messages?before=3&limit=2")
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Messages []messageView         `json:"messages"`
		Turns    map[string]store.Turn `json:"turns"`
	}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if len(page.Messages) != 2 || page.Messages[0].ID != userMsg.ID || page.Turns[turn.ID].UserMessageID != userMsg.ID {
		t.Fatalf("history page did not include messages and their turn: %#v", page)
	}
	for _, limit := range []string{"0", "-2"} {
		res, err = client.Get(ts.URL + "/api/conversations/" + created.Conversation.ID + "/messages?after=1&limit=" + limit)
		if err != nil {
			t.Fatal(err)
		}
		var bounded struct {
			Messages []messageView `json:"messages"`
		}
		err = json.NewDecoder(res.Body).Decode(&bounded)
		_ = res.Body.Close()
		if err != nil || res.StatusCode != http.StatusOK || len(bounded.Messages) != 1 {
			t.Fatalf("limit %s: status=%d page=%+v err=%v", limit, res.StatusCode, bounded, err)
		}
	}
	if err := st.DeleteMessage(context.Background(), userMsg.ID); err != nil {
		t.Fatal(err)
	}
	res, err = client.Get(ts.URL + "/api/conversations/" + created.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	var history struct {
		HistoryHasMore bool `json:"historyHasMore"`
	}
	if err := json.NewDecoder(res.Body).Decode(&history); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if history.HistoryHasMore {
		t.Fatal("deleted earlier message must not create phantom history")
	}

	for _, tc := range []struct {
		password string
		want     int
	}{{"abc", http.StatusBadRequest}, {"abcd", http.StatusOK}} {
		payload, _ := json.Marshal(map[string]string{"current": "test-password-123", "new": tc.password})
		req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/auth/password", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", login.CSRF)
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatalf("password length %d: status = %d, want %d", len(tc.password), res.StatusCode, tc.want)
		}
	}
	if !auth.Verify(context.Background(), st, "abcd") {
		t.Fatal("four-character password was not saved")
	}
}

func TestStaticAssetETagRevalidation(t *testing.T) {
	srv, err := New(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	get := func(ifNoneMatch string) *http.Response {
		req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result()
	}

	res := get("")
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || len(body) == 0 {
		t.Fatalf("initial status=%d body=%d bytes", res.StatusCode, len(body))
	}
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}

	res = get(etag)
	rebody, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotModified || len(rebody) != 0 {
		t.Fatalf("matching If-None-Match status=%d body=%d bytes", res.StatusCode, len(rebody))
	}

	res = get(`"0000000000000000000000000000000000000000000000000000000000000000"`)
	rebody, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || len(rebody) != len(body) {
		t.Fatalf("mismatched If-None-Match status=%d body=%d bytes", res.StatusCode, len(rebody))
	}

	req := httptest.NewRequest(http.MethodGet, "/static/does-not-exist.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if res := rec.Result(); res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset status = %d", res.StatusCode)
	}
}
