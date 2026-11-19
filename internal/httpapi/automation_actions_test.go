package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/realtime"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeRunner) RunNow(jobID string) error {
	f.mu.Lock()
	f.calls = append(f.calls, jobID)
	f.mu.Unlock()
	return nil
}

type notifyingChannel struct {
	mu   sync.Mutex
	last string
}

func (c *notifyingChannel) Name() string                { return "wa" }
func (c *notifyingChannel) Label() string               { return "WA" }
func (c *notifyingChannel) Start(context.Context) error { return nil }
func (c *notifyingChannel) Stop() error                 { return nil }
func (c *notifyingChannel) Status() channels.Status     { return channels.Status{Name: "wa"} }
func (c *notifyingChannel) Begin(context.Context, channels.Target, string) (channels.Delivery, error) {
	return nil, nil
}
func (c *notifyingChannel) Notify(_ context.Context, _ channels.Target, text string) error {
	c.mu.Lock()
	c.last = text
	c.mu.Unlock()
	return nil
}

func (c *notifyingChannel) sent() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func newAutomationServer(t *testing.T) (*httptest.Server, *apiClient, *fakeRunner, *notifyingChannel, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	if _, _, err := auth.EnsureBootstrap(context.Background(), st, "test-password-123"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := realtime.NewHub(logger)
	agentMgr := agent.NewManager(st, logger)
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Fake", Slug: "automation-agent", Command: "/bin/true", Cwd: dir, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	chanMgr := channels.NewManager(hub, logger)
	rec := &notifyingChannel{}
	chanMgr.Register(rec)
	svc := chat.New(chat.Options{Store: st, Agents: agentMgr, Channels: chanMgr, Events: hub, Log: logger, Workspace: dir})
	agentMgr.SetSink(svc)
	runner := &fakeRunner{}
	cfg := config.Config{Addr: "127.0.0.1:0", DataDir: dir}
	srv, err := New(Options{Config: cfg, Store: st, Chat: svc, Agents: agentMgr, Channels: chanMgr, Hub: hub, Log: logger, Automations: runner})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	jar, _ := cookiejar.New(nil)
	login, _ := json.Marshal(map[string]any{"password": "test-password-123"})
	res, err := (&http.Client{Jar: jar}).Post(ts.URL+"/api/auth/login", "application/json", bytes.NewReader(login))
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	_ = json.NewDecoder(res.Body).Decode(&session)
	res.Body.Close()
	if res.StatusCode != 200 || session.CSRF == "" {
		t.Fatalf("login status = %d csrf=%q", res.StatusCode, session.CSRF)
	}
	return ts, &apiClient{t: t, client: &http.Client{Jar: jar}, csrf: session.CSRF}, runner, rec, ag.ID
}

type apiClient struct {
	t      *testing.T
	client *http.Client
	csrf   string
}

func (c *apiClient) post(path string, body []byte) (int, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf)
	res, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

// createAutomation saves a job through the API so the server's store owns it.
func (c *apiClient) createAutomation(ts *httptest.Server, agentID string, extra map[string]any) string {
	c.t.Helper()
	job := map[string]any{
		"name": "nightly", "prompt": "hello", "schedule": "once", "agentId": agentID,
		"runAt": time.Now().Add(time.Hour).UnixMilli(), "timezone": "UTC", "sessionMode": "isolated",
	}
	maps.Copy(job, extra)
	body, _ := json.Marshal(job)
	code, out := c.post(ts.URL+"/api/automations", body)
	if code != 201 {
		c.t.Fatalf("create status = %d body=%s", code, out)
	}
	var parsed struct {
		Automation struct {
			ID string `json:"id"`
		} `json:"automation"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		c.t.Fatal(err)
	}
	return parsed.Automation.ID
}

func TestAutomationRunEndpointUsesScheduler(t *testing.T) {
	ts, cs, runner, _, agentID := newAutomationServer(t)
	id := cs.createAutomation(ts, agentID, nil)
	if code, out := cs.post(ts.URL+"/api/automations/"+id+"/run", nil); code != 200 {
		t.Fatalf("run status = %d body=%s", code, out)
	}
	if got := runner.calls; len(got) != 1 || got[0] != id {
		t.Fatalf("RunNow calls = %#v", got)
	}
	if code, _ := cs.post(ts.URL+"/api/automations/auto_missing/run", nil); code != 404 {
		t.Fatal("unknown job run should be 404")
	}
}

func TestAutomationTestNotifyEndpoint(t *testing.T) {
	ts, cs, _, rec, agentID := newAutomationServer(t)
	id := cs.createAutomation(ts, agentID, map[string]any{"notifyChannel": "wa", "notifyTarget": "628123@s.whatsapp.net"})
	if code, out := cs.post(ts.URL+"/api/automations/"+id+"/test-notify", nil); code != 200 {
		t.Fatalf("test-notify status = %d body=%s", code, out)
	}
	if sent := rec.sent(); !strings.Contains(sent, "NusaMation test") || !strings.Contains(sent, "nightly") {
		t.Fatalf("test message = %q", sent)
	}

	// A job without a result chat is refused with a clear error.
	plain := cs.createAutomation(ts, agentID, nil)
	if code, out := cs.post(ts.URL+"/api/automations/"+plain+"/test-notify", nil); code != 400 {
		t.Fatalf("test-notify without result chat status = %d body=%s", code, out)
	}
}
