package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestTerminalAuthAgent(t *testing.T) {
	if os.Getenv("NM_TEST_AGENT") != "1" {
		return
	}
	for _, arg := range os.Args {
		if arg == "--login" {
			fmt.Print("Login code: ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(line) != "2468" || os.Getenv("NM_METHOD_ENV") != "set" {
				os.Exit(1)
			}
			_ = os.WriteFile(os.Getenv("NM_TEST_FLAG"), []byte("ok"), 0600)
			os.Exit(0)
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		if msg.Method == "initialize" {
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"protocolVersion\":1,\"agentCapabilities\":{},\"authMethods\":[{\"id\":\"terminal-login\",\"type\":\"terminal\",\"name\":\"Login\",\"args\":[\"--login\"],\"env\":{\"NM_METHOD_ENV\":\"set\"}}]}}\n", msg.ID)
		}
	}
	os.Exit(0)
}

func TestTerminalAuthWebSocket(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	if _, _, err = auth.EnsureBootstrap(context.Background(), st, "test-password"); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	flag := filepath.Join(dir, "login-ok")
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{Name: "Test", Slug: "test", Command: exe, Args: []string{"-test.run=TestTerminalAuthAgent", "--"}, Cwd: dir, Enabled: &enabled, Env: store.SecretList{{Name: "NM_TEST_AGENT", Value: "1"}, {Name: "NM_TEST_FLAG", Value: flag}}})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := agent.NewManager(st, logger)
	defer mgr.StopAll()
	server, err := New(Options{Config: config.Config{DataDir: dir}, Store: st, Agents: mgr, Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	res, err := client.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"test-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("login: %d", res.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, ts.URL+"/api/agents/"+ag.ID+"/auth-terminal?methodId=terminal-login", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	typ, output, err := ws.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || !strings.Contains(string(output), "Login code:") {
		t.Fatalf("terminal prompt: %q %v", output, err)
	}
	if err = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"2468\n"}`)); err != nil {
		t.Fatal(err)
	}
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ == websocket.MessageText {
			var status struct {
				Type string `json:"type"`
				OK   bool   `json:"ok"`
			}
			if json.Unmarshal(data, &status) == nil && status.Type == "exit" {
				if !status.OK {
					t.Fatalf("login failed: %s", data)
				}
				break
			}
		}
	}
	if data, err := os.ReadFile(flag); err != nil || string(data) != "ok" {
		t.Fatalf("login did not complete: %q %v", data, err)
	}
}
