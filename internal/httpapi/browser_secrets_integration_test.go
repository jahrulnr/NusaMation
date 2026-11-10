// Browser-backed integration for [[env:NAME]] resolution + masking through the
// real /api/browser-use handler. Gated like the browser package tests:
//
//	NUSAMATION_BROWSER_TEST=1 go test ./internal/httpapi -run Integration
package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/browser"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/mcp/grants"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func secretFixtureURL() string {
	html := `<input id="p" value="">` +
		`<input id="s1" value="keep-me">` +
		`<input id="s2" value="also-keep">` +
		`<div id="lit">literal [[env:DOES_NOT_EXIST]]</div>` +
		`<script>console.log('boot')</script>`
	return "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html))
}

type buResp struct {
	SessionID string          `json:"sessionId"`
	URL       string          `json:"url"`
	Title     string          `json:"title"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Image     string          `json:"image"`
	Result    json.RawMessage `json:"result"`
	Snapshot  string          `json:"snapshot"`
	Detail    string          `json:"detail"`
	Error     string          `json:"error"`
	Code      string          `json:"code"`
}

type buFixture struct {
	t    *testing.T
	s    *Server
	mgr  *browser.Manager
	sess map[string]*browser.Session
	tok  map[string]string
}

func (f *buFixture) post(tok, body string) (int, buResp, string) {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/browser-use", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	f.s.handleBrowserUse(rec, req)
	var out buResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func (f *buFixture) evalDOM(conv, expr string) string {
	f.t.Helper()
	raw, err := f.sess[conv].Eval(context.Background(), expr)
	if err != nil {
		f.t.Fatalf("eval %s: %v", expr, err)
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func (f *buFixture) refs(conv string, ids ...string) map[string]string {
	f.t.Helper()
	raw, err := f.sess[conv].Eval(context.Background(), `(() => {
	  const out = {};
	  for (const [r, el] of (window.__nusaRefs || [])) { if (el.id) out[el.id] = r; }
	  return out;
	})()`)
	if err != nil {
		f.t.Fatalf("refs: %v", err)
	}
	var m map[string]string
	_ = json.Unmarshal(raw, &m)
	return m
}

func TestBrowserUseSecretIntegration(t *testing.T) {
	if os.Getenv("NUSAMATION_BROWSER_TEST") == "" {
		t.Skip("set NUSAMATION_BROWSER_TEST=1 to run browser-backed tests")
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "nusamation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	ctx := context.Background()

	agA, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "agent-a", Command: "/bin/true", Cwd: dir,
		Env: store.SecretList{
			{Name: "TOKEN", Value: "vSecretA"},
			{Name: "SHORT", Value: "t"},
			{Name: "NUM", Value: "1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	agB, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "agent-b", Command: "/bin/true", Cwd: dir,
		Env: store.SecretList{{Name: "TOKEN", Value: "vSecretB"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	convA, err := st.CreateConversation(ctx, agA.ID, "A", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	convB, err := st.CreateConversation(ctx, agB.ID, "B", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	g := grants.New(filepath.Join(dir, "browser-use-token"))
	mgr := browser.NewManager(browser.Options{DataDir: dir})
	defer mgr.Close()
	s := &Server{store: st, browser: mgr, browserUse: g, cfg: config.Config{DataDir: dir}}
	f := &buFixture{t: t, s: s, mgr: mgr, sess: map[string]*browser.Session{}, tok: map[string]string{
		"A": g.Token(convA.ID), "B": g.Token(convB.ID),
	}}

	sessA, err := mgr.CreateSession(ctx, convA.ID, secretFixtureURL(), 1280, 800)
	if err != nil {
		t.Fatalf("session A: %v", err)
	}
	sessB, err := mgr.CreateSession(ctx, convB.ID, secretFixtureURL(), 1024, 768)
	if err != nil {
		t.Fatalf("session B: %v", err)
	}
	f.sess["A"], f.sess["B"] = sessA, sessB

	// Stamp refs via the real snapshot tool, then map ids to refs.
	if code, _, _ := f.post(f.tok["A"], `{"tool":"tree"}`); code != 200 {
		t.Fatalf("tree A: %d", code)
	}
	if code, _, _ := f.post(f.tok["B"], `{"tool":"tree"}`); code != 200 {
		t.Fatalf("tree B: %d", code)
	}
	rA := f.refs("A", "p", "s1", "s2")
	if len(rA) < 3 {
		t.Fatalf("refs missing: %v", rA)
	}

	// 1. Batch preflight is atomic: a later unknown tag fails the whole call and
	// earlier fields (plain AND secret) keep their original DOM values.
	code, out, body := f.post(f.tok["A"], `{"tool":"fill","fields":[`+
		`{"ref":"`+rA["p"]+`","value":"plainx"},`+
		`{"ref":"`+rA["s1"]+`","value":"[[env:TOKEN]]"},`+
		`{"ref":"`+rA["s2"]+`","value":"[[env:NOPE]]"}]}`)
	if code != 400 || out.Code != "unknown_secret" {
		t.Fatalf("want 400 unknown_secret, got %d %q body %s", code, out.Code, body)
	}
	if v := f.evalDOM("A", `document.getElementById('p').value`); v != "" {
		t.Fatalf("atomicity: earlier plain field mutated to %q", v)
	}
	if v := f.evalDOM("A", `document.getElementById('s1').value`); v != "keep-me" {
		t.Fatalf("atomicity: earlier secret field mutated to %q", v)
	}

	// 2. Valid secret fill: raw value reaches the DOM (ground truth via
	// session eval), the HTTP response never carries it.
	code, _, body = f.post(f.tok["A"], `{"tool":"fill","fields":[{"ref":"`+rA["s1"]+`","value":"[[env:TOKEN]]"}]}`)
	if code != 200 {
		t.Fatalf("fill: %d body %s", code, body)
	}
	if v := f.evalDOM("A", `document.getElementById('s1').value`); v != "vSecretA" {
		t.Fatalf("DOM should hold raw secret, got %q", v)
	}
	if strings.Contains(body, "vSecretA") {
		t.Fatal("handler response leaked the raw secret")
	}

	// 3. Register the short/numeric secrets, then prove envelope survives:
	// width/height decode as ints even though "1" is a registered secret.
	code, _, _ = f.post(f.tok["A"], `{"tool":"fill","fields":[{"ref":"`+rA["s2"]+`","value":"x[[env:SHORT]][[env:NUM]]"}]}`)
	if code != 200 {
		t.Fatalf("short fill: %d", code)
	}
	if v := f.evalDOM("A", `document.getElementById('s2').value`); v != "xt1" {
		t.Fatalf("short/numeric fill: %q", v)
	}
	code, out, _ = f.post(f.tok["A"], `{"tool":"tree"}`)
	if code != 200 || out.Width != 1280 || out.Height != 800 || out.SessionID == "" {
		t.Fatalf("envelope corrupted by '1'/'t' secrets: %d %+v", code, out)
	}
	if strings.Contains(out.Snapshot, "[[env:DOES_NOT_EXIST]]") {
		// literal page text survives masking (it was never a resolved secret)
	} else {
		t.Fatal("literal page tag text was masked or lost")
	}

	// 4. Handler eval masks nested strings and dynamic keys, keeps types.
	code, out, body = f.post(f.tok["A"], `{"tool":"eval","expression":"(()=>{const o={};o['vSecretA']='deep vSecretA';o.n=42;return o})()"}`)
	if code != 200 {
		t.Fatalf("eval: %d %s", code, body)
	}
	var ev map[string]any
	if err := json.Unmarshal(out.Result, &ev); err != nil {
		t.Fatalf("eval result not object: %s", out.Result)
	}
	if ev["[[env:TOKEN]]"] != "deep [[env:TOKEN]]" {
		t.Fatalf("nested string/key not masked: %v", ev)
	}
	if ev["n"] != float64(42) {
		t.Fatalf("numeric field changed: %v", ev)
	}

	// 5. Console output and thrown errors are masked through the writer too.
	f.evalDOM("A", `console.log('vSecretA in console')`)
	code, _, body = f.post(f.tok["A"], `{"tool":"console"}`)
	if code != 200 || strings.Contains(body, "vSecretA") || !strings.Contains(body, "[[env:TOKEN]]") {
		t.Fatalf("console masking: %d %s", code, body)
	}
	_, _, body = f.post(f.tok["A"], `{"tool":"eval","expression":"(()=>{throw new Error('vSecretA')})()"}`)
	if !strings.Contains(body, "[[env:TOKEN]]") || strings.Contains(body, "vSecretA") {
		t.Fatalf("error masking: %s", body)
	}

	// 6. Token selects the conversation's agent; a foreign agentId field in the
	// body cannot retarget resolution (unknown field, ignored by readJSONMode).
	rB := f.refs("B", "s1")
	code, _, body = f.post(f.tok["B"], `{"tool":"fill","agentId":"`+agA.ID+`","fields":[{"ref":"`+rB["s1"]+`","value":"[[env:TOKEN]]"}]}`)
	if code != 200 {
		t.Fatalf("fill B: %d %s", code, body)
	}
	if v := f.evalDOM("B", `document.getElementById('s1').value`); v != "vSecretB" {
		t.Fatalf("conv B resolved wrong agent env: %q", v)
	}
	if strings.Contains(body, "vSecretB") {
		t.Fatalf("conv B response leaked its raw secret: %s", body)
	}
	// Session isolation: B's registry does not know A's value.
	code, _, body = f.post(f.tok["B"], `{"tool":"eval","expression":"'vSecretA'"}`)
	if code != 200 || !strings.Contains(body, "vSecretA") {
		t.Fatalf("A's secret wrongly masked in B's session: %d %s", code, body)
	}
}
