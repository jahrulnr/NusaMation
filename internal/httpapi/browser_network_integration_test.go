// The network-control tools through the real /api/browser-use path: route
// registration, fulfillment, the network log, response waiting, and init
// scripts reach Chrome exactly as an agent would drive them.
// NUSAMATION_BROWSER_TEST=1 to run.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/browser"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/mcp/grants"
	"github.com/jahrulnr/nusamation/internal/store"
)

type browserUseResponse struct {
	Result json.RawMessage `json:"result"`
	Detail string          `json:"detail"`
	Error  string          `json:"error"`
	Code   string          `json:"code"`
}

func TestBrowserUseNetworkControlIntegration(t *testing.T) {
	if os.Getenv("NUSAMATION_BROWSER_TEST") == "" {
		t.Skip("set NUSAMATION_BROWSER_TEST=1 to run browser-backed tests")
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "nusamation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "net-agent", Command: "/bin/true", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "net", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<!doctype html><title>net</title><p id="ready">ready</p>`)
		case "/api/real":
			fmt.Fprint(w, "real")
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	g := grants.New(filepath.Join(dir, "browser-use-token"))
	mgr := browser.NewManager(browser.Options{DataDir: dir})
	defer mgr.Close()
	s := &Server{store: st, browser: mgr, browserUse: g, cfg: config.Config{DataDir: dir}}
	tok := g.Token(conv.ID)
	call := func(t *testing.T, body string) (int, browserUseResponse) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/browser-use", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		s.handleBrowserUse(rr, req)
		var out browserUseResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	evalString := func(t *testing.T, expr string) string {
		t.Helper()
		body, err := json.Marshal(map[string]any{"tool": "browser_evaluate", "expression": expr})
		if err != nil {
			t.Fatal(err)
		}
		code, out := call(t, string(body))
		if code != http.StatusOK {
			t.Fatalf("eval %q: %d %s", expr, code, out.Error)
		}
		var got string
		if err := json.Unmarshal(out.Result, &got); err != nil {
			t.Fatalf("eval %q result %s: %v", expr, out.Result, err)
		}
		return got
	}

	sess, err := mgr.CreateSession(ctx, conv.ID, origin.URL+"/", 1280, 800)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close()
	// CreateSession returns before the page commits; wait for the document.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if evalString(t, `document.readyState`) == "complete" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	code, out := call(t, `{"tool":"browser_route","pattern":"**/api/mock","action":"fulfill","body":"{\"mocked\":true}"}`)
	if code != http.StatusOK {
		t.Fatalf("route: %d %s", code, out.Error)
	}
	fetchExpr := `(async () => { const r = await fetch("/api/mock", {cache:"no-store"}); return (await r.text()) })()`
	if got := evalString(t, fetchExpr); got != `{"mocked":true}` {
		t.Fatalf("fulfilled body = %q", got)
	}

	// The request still lands in the network log the agent can read.
	code, out = call(t, `{"tool":"browser_network_log","pattern":"**/api/mock","limit":5}`)
	if code != http.StatusOK {
		t.Fatalf("network log: %d %s", code, out.Error)
	}
	var logOut struct {
		Entries []browser.NetEntry `json:"entries"`
		Next    int64              `json:"next"`
	}
	if err := json.Unmarshal(out.Result, &logOut); err != nil {
		t.Fatalf("network log result %s: %v", out.Result, err)
	}
	if len(logOut.Entries) == 0 {
		t.Fatalf("network log has no entry for the fulfilled request: %s", out.Result)
	}
	if logOut.Entries[0].URL != origin.URL+"/api/mock" {
		t.Fatalf("log entry URL = %q", logOut.Entries[0].URL)
	}

	// wait_for_response sees the traffic that already arrived.
	code, out = call(t, `{"tool":"browser_wait_for_response","pattern":"**/api/mock","timeout":2}`)
	if code != http.StatusOK {
		t.Fatalf("wait response: %d %s", code, out.Error)
	}
	var waited browser.NetEntry
	if err := json.Unmarshal(out.Result, &waited); err != nil {
		t.Fatalf("wait result %s: %v", out.Result, err)
	}
	if waited.URL != origin.URL+"/api/mock" {
		t.Fatalf("wait entry = %+v", waited)
	}

	// An init script runs before the page's own scripts on the next load.
	code, out = call(t, `{"tool":"browser_add_init_script","source":"window.__probe = \"installed\";"}`)
	if code != http.StatusOK {
		t.Fatalf("add init script: %d %s", code, out.Error)
	}
	var addedScript struct {
		Script browser.AgentInitScript `json:"script"`
	}
	if err := json.Unmarshal(out.Result, &addedScript); err != nil || addedScript.Script.ID == "" {
		t.Fatalf("add init script result %s: %v", out.Result, err)
	}
	code, out = call(t, `{"tool":"browser_navigate","url":"`+origin.URL+`/"}`)
	if code != http.StatusOK {
		t.Fatalf("navigate: %d %s", code, out.Error)
	}
	if got := evalString(t, `window.__probe`); got != "installed" {
		t.Fatalf("init script did not run: %q", got)
	}

	// route_list exposes both transports and the init script id.
	code, out = call(t, `{"tool":"browser_route_list"}`)
	if code != http.StatusOK {
		t.Fatalf("route list: %d %s", code, out.Error)
	}
	var listed struct {
		Rules       []browser.RouteState      `json:"rules"`
		InitScripts []browser.AgentInitScript `json:"initScripts"`
	}
	if err := json.Unmarshal(out.Result, &listed); err != nil {
		t.Fatalf("route list result %s: %v", out.Result, err)
	}
	if len(listed.Rules) != 1 || listed.Rules[0].Hits == 0 {
		t.Fatalf("listed rules = %+v", listed.Rules)
	}
	if len(listed.InitScripts) != 1 || listed.InitScripts[0].ID == "" {
		t.Fatalf("listed init scripts = %+v", listed.InitScripts)
	}

	// WebSocket rules take the shim path; reload:false keeps this test from
	// navigating away, and clear removes it again.
	code, out = call(t, `{"tool":"browser_route_ws","pattern":"**/api/ws","target":"ws://127.0.0.1:9/ws","reload":false}`)
	if code != http.StatusOK {
		t.Fatalf("route ws: %d %s", code, out.Error)
	}
	code, out = call(t, `{"tool":"browser_route_list"}`)
	if code != http.StatusOK {
		t.Fatalf("route list after ws: %d %s", code, out.Error)
	}
	var withWS struct {
		WebSocketRules []browser.WSRouteState `json:"webSocketRules"`
	}
	if err := json.Unmarshal(out.Result, &withWS); err != nil {
		t.Fatalf("route list result %s: %v", out.Result, err)
	}
	if len(withWS.WebSocketRules) != 1 || withWS.WebSocketRules[0].Target != "ws://127.0.0.1:9/ws" {
		t.Fatalf("websocket rules = %+v", withWS.WebSocketRules)
	}
	if code, out = call(t, `{"tool":"browser_route_ws_clear","reload":false}`); code != http.StatusOK {
		t.Fatalf("route ws clear: %d %s", code, out.Error)
	}

	// Removing the init script is reported back.
	removeBody := `{"tool":"browser_remove_init_script","id":"` + addedScript.Script.ID + `"}`
	code, out = call(t, removeBody)
	if code != http.StatusOK {
		t.Fatalf("remove init script: %d %s", code, out.Error)
	}
	var removed struct {
		Removed bool `json:"removed"`
	}
	if err := json.Unmarshal(out.Result, &removed); err != nil || !removed.Removed {
		t.Fatalf("remove init script result %s: %v", out.Result, err)
	}

	// A bad rule is a 400 the agent can correct.
	if code, out := call(t, `{"tool":"browser_route","pattern":"**/x","action":"nope"}`); code != http.StatusBadRequest {
		t.Fatalf("bad route code = %d, want 400 (%s)", code, out.Error)
	}
	// Clearing disarms interception.
	if code, out := call(t, `{"tool":"browser_route_clear"}`); code != http.StatusOK {
		t.Fatalf("route clear: %d %s", code, out.Error)
	}
	if got := evalString(t, `(async () => { const r = await fetch("/api/mock", {cache:"no-store"}); return String(r.status) })()`); got != "404" {
		t.Fatalf("cleared route still intercepted: %q", got)
	}
}
