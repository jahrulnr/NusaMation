// Observations through the real /api/browser-use path: the always-on tap
// feed, synthesized navigations, and netLog failures reach drive-tool
// responses while Chrome runs. NUSAMATION_BROWSER_TEST=1 to run.
package httpapi

import (
	"context"
	"encoding/json"
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

func TestObservationsIntegration(t *testing.T) {
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
	ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "obs-agent", Command: "/bin/true", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "obs", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}

	pageHTML := `<html><body>` +
		`<button id="b">Go</button>` +
		`<script>` +
		`document.getElementById('b').addEventListener('click', () => {` +
		`  console.error('user click broke things');` +
		`  fetch('/missing').catch(() => {});` +
		`});` +
		`</script>` +
		`</body></html>`
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/two":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(pageHTML))
		case "/favicon.ico":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	g := grants.New(filepath.Join(dir, "browser-use-token"))
	mgr := browser.NewManager(browser.Options{DataDir: dir, RecordingsDir: filepath.Join(dir, "recordings")})
	defer mgr.Close()
	s := &Server{store: st, browser: mgr, browserUse: g, cfg: config.Config{DataDir: dir}}
	tok := g.Token(conv.ID)
	post := func(body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/api/browser-use", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		s.handleBrowserUse(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}

	sess, err := mgr.CreateSession(ctx, conv.ID, "about:blank", 1280, 800)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close()

	// No recording live — the always-on feed must still collect.
	code, out := post(`{"tool":"navigate","url":"` + origin.URL + `"}`)
	if code != 200 {
		t.Fatalf("navigate: %d", code)
	}
	// The tool's own navigation is reported by its url/title fields, not
	// re-reported as an observation.
	if obs, _ := out["observations"].([]any); len(obs) != 0 {
		t.Fatalf("first call should report no observations, got %v", obs)
	}
	// A user-side navigation between calls surfaces as a synthesized nav
	// (the tap's buffer resets on document unload).
	if _, err := sess.Eval(ctx, `location.assign('/two')`); err != nil {
		t.Fatalf("user nav: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	code, out = post(`{"tool":"browser_take_screenshot"}`)
	if code != 200 {
		t.Fatalf("snapshot: %d", code)
	}
	obs, _ := out["observations"].([]any)
	if !obsHas(obs, "nav ") || !obsHas(obs, "/two") {
		t.Fatalf("expected synthesized nav to /two in %v", obs)
	}

	// A user-side click: real DOM events reach the tap without CDP input.
	if _, err := sess.Eval(ctx, `document.getElementById('b').click()`); err != nil {
		t.Fatalf("page click: %v", err)
	}
	// The tap flushes on a 300ms tick — wait past it before the next call.
	time.Sleep(600 * time.Millisecond)
	_, out = post(`{"tool":"browser_take_screenshot"}`)
	obs, _ = out["observations"].([]any)
	if !obsHas(obs, "click") {
		t.Fatalf("expected a click observation, got %v", obs)
	}
	if !obsHas(obs, "console.error") && !obsHas(obs, "console.error user click broke things") {
		t.Fatalf("expected the console.error line, got %v", obs)
	}
	if !obsHas(obs, "net 404") {
		t.Fatalf("expected the failed request line, got %v", obs)
	}
	// The drain consumed them — a quiet call reports nothing new.
	_, out = post(`{"tool":"browser_take_screenshot"}`)
	if obs, _ := out["observations"].([]any); len(obs) != 0 {
		t.Fatalf("quiet call should report nothing, got %v", obs)
	}
}

func obsHas(obs []any, want string) bool {
	for _, o := range obs {
		if s, ok := o.(string); ok && strings.Contains(s, want) {
			return true
		}
	}
	return false
}
