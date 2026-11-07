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
	"os"
	"path/filepath"
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

func TestConversationMetaAPI(t *testing.T) {
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
	loginBody, _ := json.Marshal(map[string]any{"password": "test-password-123"})
	res, err := client.Post(ts.URL+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	var login struct {
		CSRF string `json:"csrf"`
	}
	_ = json.NewDecoder(res.Body).Decode(&login)
	_ = res.Body.Close()

	createBody, _ := json.Marshal(map[string]any{"agentId": ag.ID})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/conversations", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", login.CSRF)
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		Conversation store.Conversation `json:"conversation"`
	}
	_ = json.NewDecoder(res.Body).Decode(&created)
	_ = res.Body.Close()
	convID := created.Conversation.ID

	put := func(body string) (int, convMeta) {
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/conversations/"+convID+"/meta", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", login.CSRF)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Meta convMeta `json:"meta"`
		}
		_ = json.NewDecoder(res.Body).Decode(&out)
		_ = res.Body.Close()
		return res.StatusCode, out.Meta
	}
	get := func() convMeta {
		res, err := client.Get(ts.URL + "/api/conversations/" + convID + "/meta")
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Meta convMeta `json:"meta"`
		}
		_ = json.NewDecoder(res.Body).Decode(&out)
		_ = res.Body.Close()
		return out.Meta
	}

	// Empty meta is a 200 with an empty object, not a 404.
	if m := get(); m.Title != "" || m.Draft != nil || m.Browser != nil {
		t.Fatalf("expected empty meta, got %+v", m)
	}

	// Merge patch: draft + lastUrl land; a later title-only patch keeps them.
	status, m := put(`{"draft":{"text":"hello","sendDelivery":"steer"},"browser":{"lastUrl":"https://ex.test/"}}`)
	if status != 200 || m.Draft == nil || m.Draft.Text != "hello" || m.Browser == nil || m.Browser.LastURL != "https://ex.test/" {
		t.Fatalf("put draft status=%d meta=%+v", status, m)
	}
	status, m = put(`{"title":"Renamed"}`)
	if status != 200 || m.Title != "Renamed" || m.Draft == nil || m.Browser == nil {
		t.Fatalf("merge lost fields status=%d meta=%+v", status, m)
	}

	// draft:null clears only the draft.
	status, m = put(`{"draft":null}`)
	if status != 200 || m.Draft != nil || m.Title != "Renamed" {
		t.Fatalf("draft null status=%d meta=%+v", status, m)
	}

	// The file exists on disk where an operator would look.
	if _, err := os.Stat(filepath.Join(dir, "conversations", convID, "meta.json")); err != nil {
		t.Fatalf("meta.json not written: %v", err)
	}

	// Unknown conversation → 404, no file created.
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/api/conversations/nope/meta", bytes.NewReader([]byte(`{"title":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", login.CSRF)
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown conv status = %d", res.StatusCode)
	}

	// Traversal guard: convMetaPath rejects separators and dots.
	for _, id := range []string{"", "..", "a/b", `a\b`, "../x"} {
		if _, err := srv.convMetaPath(id); err == nil {
			t.Fatalf("convMetaPath(%q) accepted", id)
		}
	}
}
