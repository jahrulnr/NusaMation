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
	"net/url"
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

func TestProjectHTTPContract(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
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
		Name: "API Agent", Slug: "api-agent", Command: "echo", Cwd: dir, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.NewHub(logger)
	agentMgr := agent.NewManager(st, logger)
	t.Cleanup(agentMgr.StopAll)
	svc := chat.New(chat.Options{
		Store: st, Agents: agentMgr, Channels: channels.NewManager(hub, logger), Events: hub, Log: logger,
		UploadDir: filepath.Join(dir, "uploads"), Workspace: dir,
	})
	srv, err := New(Options{Config: config.Config{DataDir: dir}, Store: st, Chat: svc, Agents: agentMgr, Hub: hub, Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
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
	if err := json.NewDecoder(res.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	do := func(method, path string, body any) (*http.Response, []byte) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, ts.URL+path, &buf)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", login.CSRF)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		return res, raw
	}

	folder := t.TempDir()
	extra := t.TempDir()
	res, raw := do(http.MethodPost, "/api/projects", map[string]any{"sources": []string{folder}})
	var denied struct {
		Code  string   `json:"code"`
		Added []string `json:"added"`
	}
	if err := json.Unmarshal(raw, &denied); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusConflict || denied.Code != "confirmation_required" || len(denied.Added) != 1 {
		t.Fatalf("confirm status=%d body=%s", res.StatusCode, raw)
	}
	res, raw = do(http.MethodGet, "/api/projects", nil)
	var empty struct {
		Projects []store.Project `json:"projects"`
	}
	if err := json.Unmarshal(raw, &empty); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || len(empty.Projects) != 0 {
		t.Fatalf("list after decline status=%d body=%s", res.StatusCode, raw)
	}

	res, raw = do(http.MethodPost, "/api/projects", map[string]any{"name": "Local", "sources": []string{folder, extra}, "foldersConfirmed": true})
	var created struct {
		Project store.Project `json:"project"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusCreated || created.Project.ID == "" || len(created.Project.RootPaths) != 2 {
		t.Fatalf("create status=%d body=%s", res.StatusCode, raw)
	}

	res, raw = do(http.MethodPost, "/api/conversations", map[string]any{"agentId": ag.ID, "projectId": created.Project.ID})
	var conv struct {
		Conversation store.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(raw, &conv); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusCreated || conv.Conversation.ProjectID != created.Project.ID {
		t.Fatalf("conversation status=%d body=%s", res.StatusCode, raw)
	}
	res, raw = do(http.MethodPost, "/api/conversations", map[string]any{"agentId": ag.ID, "projectId": "prj_missing"})
	var missing struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &missing); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusNotFound || missing.Code != "not_found" {
		t.Fatalf("unknown project status=%d body=%s", res.StatusCode, raw)
	}

	res, raw = do(http.MethodPatch, "/api/projects/"+created.Project.ID, map[string]any{
		"sources": []string{extra}, "updatedAt": created.Project.UpdatedAt - 1, "foldersConfirmed": true,
	})
	var conflict struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &conflict); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusConflict || conflict.Code != "conflict" {
		t.Fatalf("conflict status=%d body=%s", res.StatusCode, raw)
	}

	res, raw = do(http.MethodPatch, "/api/projects/"+created.Project.ID, map[string]any{"pinned": true})
	var pinned struct {
		Project store.Project `json:"project"`
	}
	if err := json.Unmarshal(raw, &pinned); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !pinned.Project.Pinned {
		t.Fatalf("pin status=%d body=%s", res.StatusCode, raw)
	}

	res, raw = do(http.MethodGet, "/api/fs/list?path="+url.QueryEscape(folder), nil)
	var listed struct {
		Path   string `json:"path"`
		Parent string `json:"parent"`
		Dirs   []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"dirs"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || listed.Path == "" || listed.Parent == "" || listed.Dirs == nil {
		t.Fatalf("list dirs status=%d body=%s", res.StatusCode, raw)
	}
	child := filepath.Join(folder, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, raw = do(http.MethodGet, "/api/fs/list?path="+url.QueryEscape(folder), nil)
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Dirs) != 1 || listed.Dirs[0].Name != "child" {
		t.Fatalf("dirs = %#v", listed.Dirs)
	}

	res, raw = do(http.MethodGet, "/api/bootstrap", nil)
	if res.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte(`"projects"`)) {
		t.Fatalf("bootstrap status=%d", res.StatusCode)
	}
}
