package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// routeTestChrome mirrors browserTestContext's opt-in gate but returns the
// executable for a Manager, since route tests need a real Session.
func routeTestChrome(t *testing.T) string {
	t.Helper()
	if os.Getenv("NUSAMATION_BROWSER_TEST") == "" {
		t.Skip("set NUSAMATION_BROWSER_TEST=1 to run browser-backed tests")
	}
	if bin := os.Getenv("NUSAMATION_BROWSER_TEST_BIN"); bin != "" {
		return bin
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome", "msedge"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("no Chrome/Chromium binary found")
	return ""
}

func routeTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!doctype html><title>routes</title><p id="ready">ready</p>`)
		case "/api/real":
			fmt.Fprint(w, "real")
		case "/api/slow":
			fmt.Fprint(w, "slow")
		case "/rewritten":
			fmt.Fprint(w, "rewritten")
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newRouteTestSession(t *testing.T, bin, pageURL string) (*Manager, *Session) {
	t.Helper()
	m := NewManager(Options{
		ExecPath: bin,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess, err := m.CreateSession(ctx, "cv_route_test", pageURL, 800, 600)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	ready := false
	for i := 0; i < 100 && !ready; i++ {
		raw, err := sess.Eval(ctx, `document.readyState`)
		if err == nil && string(raw) == `"complete"` {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("page did not reach readyState complete")
	}
	return m, sess
}

// evalJSON runs an expression and decodes its JSON result into out.
func evalJSON(t *testing.T, ctx context.Context, sess *Session, expr string, out any) {
	t.Helper()
	raw, err := sess.Eval(ctx, expr)
	if err != nil {
		t.Fatalf("eval %s: %v", expr, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("eval %s returned %s: %v", expr, raw, err)
	}
}

type fetchResult struct {
	Status int    `json:"status"`
	Type   string `json:"type"`
	Body   string `json:"body"`
}

// routeFetchExpr fetches a path and reports status, content-type, and body;
// a network error comes back as {"error": "..."} so the test can assert it.
func routeFetchExpr(path string) string {
	return fmt.Sprintf(`(async () => {
  try {
    const r = await fetch(%q, {cache: "no-store"});
    return {status: r.status, type: r.headers.get("content-type") || "", body: await r.text()};
  } catch (e) {
    return {error: String(e)};
  }
})()`, path)
}

func TestRouteInterception(t *testing.T) {
	bin := routeTestChrome(t)
	srv := routeTestServer(t)
	_, sess := newRouteTestSession(t, bin, srv.URL+"/")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Baseline: without rules the page reaches the server.
	var base fetchResult
	evalJSON(t, ctx, sess, routeFetchExpr("/api/real"), &base)
	if base.Body != "real" {
		t.Fatalf("baseline body = %q, want real", base.Body)
	}

	// fulfill: synthetic JSON with an inferred Content-Type.
	if _, err := sess.AddRoute(RouteRule{
		Pattern: "**/api/mock", Action: RouteFulfill, Body: `{"mocked":true}`,
	}); err != nil {
		t.Fatalf("add fulfill rule: %v", err)
	}
	var mocked fetchResult
	evalJSON(t, ctx, sess, routeFetchExpr("/api/mock"), &mocked)
	if mocked.Status != 200 || mocked.Type != "application/json" || mocked.Body != `{"mocked":true}` {
		t.Fatalf("fulfilled = %+v", mocked)
	}

	// Unmatched traffic still reaches the server while interception is armed.
	var after fetchResult
	evalJSON(t, ctx, sess, routeFetchExpr("/api/real"), &after)
	if after.Body != "real" {
		t.Fatalf("unmatched body = %q, want real", after.Body)
	}

	// abort: the page sees a network error.
	if _, err := sess.AddRoute(RouteRule{
		Pattern: "**/api/blocked", Action: RouteAbort, ErrorReason: "internetdisconnected",
	}); err != nil {
		t.Fatalf("add abort rule: %v", err)
	}
	var blocked map[string]any
	evalJSON(t, ctx, sess, routeFetchExpr("/api/blocked"), &blocked)
	if blocked["error"] == nil {
		t.Fatalf("abort should fail the fetch, got %+v", blocked)
	}

	// continue with a rewritten URL.
	if _, err := sess.AddRoute(RouteRule{
		Pattern: "**/api/rewrite", Action: RouteContinue, TargetURL: srv.URL + "/rewritten",
	}); err != nil {
		t.Fatalf("add rewrite rule: %v", err)
	}
	var rewritten fetchResult
	evalJSON(t, ctx, sess, routeFetchExpr("/api/rewrite"), &rewritten)
	if rewritten.Body != "rewritten" {
		t.Fatalf("rewritten body = %q, want rewritten", rewritten.Body)
	}

	// delay: the response waits for the rule's budget.
	if _, err := sess.AddRoute(RouteRule{
		Pattern: "**/api/slow", Action: RouteDelay, DelayMS: 300,
	}); err != nil {
		t.Fatalf("add delay rule: %v", err)
	}
	start := time.Now()
	var slow fetchResult
	evalJSON(t, ctx, sess, routeFetchExpr("/api/slow"), &slow)
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("delayed fetch returned after %s, want >= 250ms", elapsed)
	}
	if slow.Body != "slow" {
		t.Fatalf("delayed body = %q", slow.Body)
	}

	// Hit counts are visible to the agent.
	rules := sess.Routes()
	if len(rules) != 4 {
		t.Fatalf("rules = %d, want 4", len(rules))
	}
	for _, r := range rules {
		if r.Hits == 0 {
			t.Errorf("rule %s (%s) has no hits", r.ID, r.Pattern)
		}
		if r.LastError != "" {
			t.Errorf("rule %s disposition error: %s", r.ID, r.LastError)
		}
	}

	// times=1 matches once, then stops without being removed.
	once, err := sess.AddRoute(RouteRule{Pattern: "**/api/once", Action: RouteFulfill, Times: 1, Body: "first"})
	if err != nil {
		t.Fatalf("add once rule: %v", err)
	}
	var first fetchResult
	evalJSON(t, ctx, sess, routeFetchExpr("/api/once"), &first)
	if first.Body != "first" {
		t.Fatalf("first once body = %q", first.Body)
	}
	var second fetchResult
	evalJSON(t, ctx, sess, routeFetchExpr("/api/once"), &second)
	if second.Status != http.StatusNotFound {
		t.Fatalf("spent rule still matched: %+v", second)
	}
	for _, r := range sess.Routes() {
		if r.ID == once.ID && (r.Hits != 1 || r.Remaining != 0) {
			t.Fatalf("once rule state = %+v, want hits 1 remaining 0", r)
		}
	}

	// Clearing everything disarms interception: the same URL reaches the
	// server again.
	if n := sess.ClearRoutes(""); n != 5 {
		t.Fatalf("cleared = %d, want 5", n)
	}
	var off fetchResult
	evalJSON(t, ctx, sess, routeFetchExpr("/api/mock"), &off)
	if off.Status != http.StatusNotFound {
		t.Fatalf("interception still active after clear: %+v", off)
	}
}

func TestRouteSurvivesNavigation(t *testing.T) {
	bin := routeTestChrome(t)
	srv := routeTestServer(t)
	_, sess := newRouteTestSession(t, bin, srv.URL+"/")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := sess.AddRoute(RouteRule{Pattern: "**/api/mock", Action: RouteFulfill, Body: "kept"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Navigate(ctx, srv.URL+"/?again=1"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	ready := false
	for i := 0; i < 100 && !ready; i++ {
		if raw, err := sess.Eval(ctx, `document.readyState`); err == nil && string(raw) == `"complete"` {
			ready = true
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	var got fetchResult
	evalJSON(t, ctx, sess, routeFetchExpr("/api/mock"), &got)
	if strings.TrimSpace(got.Body) != "kept" {
		t.Fatalf("rule did not survive navigation: %+v", got)
	}
}

// TestRouteRejectsWebSocketRules keeps browser_route from accepting a rule
// the Fetch domain can never match: WebSocket handshakes do not reach it, so
// the agent must use browser_route_ws instead of silently registering a dead
// rule.
func TestRouteRejectsWebSocketRules(t *testing.T) {
	bin := routeTestChrome(t)
	srv := routeTestServer(t)
	_, sess := newRouteTestSession(t, bin, srv.URL+"/")
	_, err := sess.AddRoute(RouteRule{
		Pattern: "**/ws", ResourceType: "websocket", Action: RouteContinue,
		TargetURL: "ws://127.0.0.1:9/ws",
	})
	if !errors.Is(err, ErrRouteInvalid) || !strings.Contains(err.Error(), "browser_route_ws") {
		t.Fatalf("error = %v, want ErrRouteInvalid pointing at browser_route_ws", err)
	}
	if rules := sess.Routes(); len(rules) != 0 {
		t.Fatalf("rejected rule was stored: %+v", rules)
	}
}

// TestWSRouteRewrite verifies the document-start shim: the page asks for its
// own origin's socket and the constructor sends it to the fake hub instead,
// while ws.url still reports the original URL.
func TestWSRouteRewrite(t *testing.T) {
	bin := routeTestChrome(t)

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = c.Write(context.Background(), websocket.MessageText, []byte("from-hub"))
	}))
	defer other.Close()

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>ws</title><script>
window.__msgs = [];
const ws = new WebSocket((location.protocol === "https:" ? "wss://" : "ws://") + location.host + "/api/ws");
window.__url = ws.url;
ws.onmessage = e => window.__msgs.push(String(e.data));
ws.onerror = () => window.__msgs.push("error");
</script>`)
	}))
	defer page.Close()

	_, sess := newRouteTestSession(t, bin, page.URL+"/")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	target := "ws" + strings.TrimPrefix(other.URL, "http") + "/ws"
	if _, err := sess.AddWSRoute(ctx, WSRouteRule{Pattern: "**/api/ws", Target: target}, true); err != nil {
		t.Fatalf("add ws route: %v", err)
	}
	var msgs []string
	for range 100 {
		evalJSON(t, ctx, sess, `window.__msgs`, &msgs)
		if len(msgs) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(msgs) == 0 || msgs[0] != "from-hub" {
		t.Fatalf("messages = %v, want from-hub", msgs)
	}
	var reportedURL string
	evalJSON(t, ctx, sess, `window.__url`, &reportedURL)
	if !strings.HasSuffix(reportedURL, "/api/ws") {
		t.Fatalf("ws.url = %q, want the original URL", reportedURL)
	}
}

// TestInitScriptBeforePageCode proves an init script runs before the page's
// own scripts and can be removed again.
func TestInitScriptBeforePageCode(t *testing.T) {
	bin := routeTestChrome(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>init</title><script>
window.__seen = window.__nusaProbe || "missing";
</script>`)
	}))
	defer page.Close()

	_, sess := newRouteTestSession(t, bin, page.URL+"/")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	script, err := sess.AddInitScript(ctx, `window.__nusaProbe = "before";`, "", false)
	if err != nil {
		t.Fatalf("add init script: %v", err)
	}
	if _, err := sess.History(ctx, "reload"); err != nil {
		t.Fatalf("reload: %v", err)
	}
	var seen string
	evalJSON(t, ctx, sess, `window.__seen`, &seen)
	if seen != "before" {
		t.Fatalf("page saw %q, want before", seen)
	}

	if !sess.RemoveInitScript(script.ID) {
		t.Fatal("remove returned false")
	}
	if _, err := sess.History(ctx, "reload"); err != nil {
		t.Fatalf("reload after removal: %v", err)
	}
	evalJSON(t, ctx, sess, `window.__seen`, &seen)
	if seen != "missing" {
		t.Fatalf("page saw %q after removal, want missing", seen)
	}
}

// TestInitScriptRunImmediately covers the runImmediately flag on an already
// loaded page.
func TestInitScriptRunImmediately(t *testing.T) {
	bin := routeTestChrome(t)
	srv := routeTestServer(t)
	_, sess := newRouteTestSession(t, bin, srv.URL+"/")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := sess.AddInitScript(ctx, `window.__now = 42;`, "now", true); err != nil {
		t.Fatalf("add init script: %v", err)
	}
	var got int
	evalJSON(t, ctx, sess, `window.__now`, &got)
	if got != 42 {
		t.Fatalf("runImmediately value = %d, want 42", got)
	}
}
