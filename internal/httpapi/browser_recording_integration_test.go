// Recording capture through the real /api/browser-use path: tool intent,
// in-page DOM events, the network sidecar, and downloads all reach the
// recording while Chrome runs. NUSAMATION_BROWSER_TEST=1 to run.
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

func TestRecordingCaptureIntegration(t *testing.T) {
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
	ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "rec-agent", Command: "/bin/true", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "rec", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Origin server: a page with a button/input/select, a 404 endpoint, and
	// a download served with Content-Disposition.
	pageHTML := `<html><body>` +
		`<button id="b" style="position:absolute;left:20px;top:20px;width:80px;height:30px">Go</button>` +
		`<input id="i" type="text">` +
		`<a id="dl" href="/file.bin" download>dl</a>` +
		`<script>function boom(){fetch('/missing').catch(()=>{})}</script>` +
		`</body></html>`
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(pageHTML))
		case "/missing":
			http.NotFound(w, r)
		case "/file.bin":
			w.Header().Set("Content-Disposition", `attachment; filename="file.bin"`)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("download-payload"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	g := grants.New(filepath.Join(dir, "browser-use-token"))
	recDir := filepath.Join(dir, "recordings")
	mgr := browser.NewManager(browser.Options{DataDir: dir, RecordingsDir: recDir})
	defer mgr.Close()
	s := &Server{store: st, browser: mgr, browserUse: g, cfg: config.Config{DataDir: dir}}
	tok := g.Token(conv.ID)
	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/browser-use", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		s.handleBrowserUse(rr, req)
		return rr.Code
	}

	sess, err := mgr.CreateSession(ctx, conv.ID, "about:blank", 1280, 800)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close()
	if _, err := sess.StartRecording("itest"); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}

	if code := post(`{"tool":"navigate","url":"` + origin.URL + `"}`); code != 200 {
		t.Fatalf("navigate: %d", code)
	}
	// Agent click lands a tool event AND a real dom/click through the tap
	// (CDP-dispatched input hits the page's capture listeners).
	if code := post(`{"tool":"click","x":60,"y":35}`); code != 200 {
		t.Fatalf("click: %d", code)
	}
	if code := post(`{"tool":"eval","expression":"boom()&&'ok'"}`); code != 200 {
		t.Fatalf("eval: %d", code)
	}
	// A failing tool call is recorded too — emulate with a bogus preset 400s.
	if code := post(`{"tool":"emulate","preset":"bogus"}`); code != 400 {
		t.Fatalf("bad emulate should 400, got %d", code)
	}
	// The read-only recording tools answer from the files — an agent polling
	// the timeline must not amplify it, so they append no tool event.
	if code := post(`{"tool":"browser_get_recording_status"}`); code != 200 {
		t.Fatalf("recording status read: %d", code)
	}
	if code := post(`{"tool":"browser_get_timeline"}`); code != 200 {
		t.Fatalf("timeline read: %d", code)
	}

	rec := sess.Recording()
	if rec == nil {
		t.Fatal("recording should still be live")
	}
	deadline := time.Now().Add(6 * time.Second)
	var evs []browser.RecEvent
	for {
		evs, _, _ = rec.Events(0, 0, nil)
		var tools, domClicks, navs, netErrs int
		for _, ev := range evs {
			switch {
			case ev.Type == "tool":
				tools++
			case ev.Type == "click" && ev.Origin == "dom":
				domClicks++
			case ev.Type == "navigate":
				navs++
			case ev.Type == "network_error":
				netErrs++
			}
		}
		if tools >= 4 && domClicks >= 1 && navs >= 1 && netErrs >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events missing: tools=%d domClicks=%d navs=%d netErrs=%d\nall=%v",
				tools, domClicks, navs, netErrs, evs)
		}
		time.Sleep(150 * time.Millisecond)
	}
	// The tool events carry args + outcome; the failed emulate records the
	// 400 code, the successful ones ok:true.
	var okCount, failCount int
	for _, ev := range evs {
		if ev.Type != "tool" || ev.Origin != "agent" {
			continue
		}
		var d struct {
			Tool  string          `json:"tool"`
			Op    string          `json:"op"`
			Args  json.RawMessage `json:"args"`
			OK    bool            `json:"ok"`
			Code  any             `json:"code"`
			Error string          `json:"error"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			t.Fatalf("tool event: %v %s", err, ev.Data)
		}
		if len(d.Args) == 0 {
			t.Fatalf("tool args missing: %s", ev.Data)
		}
		if d.OK {
			okCount++
		} else {
			failCount++
			if d.Error == "" {
				t.Fatalf("failed tool event lacks error: %s", ev.Data)
			}
		}
	}
	if okCount != 3 || failCount != 1 {
		t.Fatalf("tool outcomes ok=%d fail=%d", okCount, failCount)
	}
	// Wait a beat past the last poll so a (wrongly) appended read-tool event
	// would have landed, then confirm none of the read ops are on the log.
	time.Sleep(400 * time.Millisecond)
	toolEvs, _, _ := rec.Events(0, 0, []string{"tool"})
	for _, ev := range toolEvs {
		var d struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			continue
		}
		switch d.Op {
		case "recording_status", "recording_summary", "timeline", "annotation", "artifact":
			t.Fatalf("read tool %q must not land a tool event", d.Op)
		}
	}

	// The sidecar has request metadata for the page load and the 404 fetch.
	var netLines []json.RawMessage
	for time.Now().Before(deadline) {
		netLines, _, _ = rec.Network(0, 0)
		if len(netLines) >= 3 {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if len(netLines) < 3 {
		t.Fatalf("network sidecar lines = %d, want >= 3", len(netLines))
	}
	var sawMissing bool
	for _, line := range netLines {
		if strings.Contains(string(line), "/missing") {
			sawMissing = true
		}
	}
	if !sawMissing {
		t.Fatal("404 request absent from network.jsonl")
	}

	// The session-scoped network endpoint pages the same lines; the live
	// recording also answers through the /api/recordings/{recId} path.
	sessReq := httptest.NewRequest(http.MethodGet,
		"/api/browser/sessions/"+sess.ID()+"/recording/network", nil)
	sessReq.SetPathValue("id", sess.ID())
	rr := httptest.NewRecorder()
	s.handleRecordingNetwork(rr, sessReq)
	if rr.Code != 200 {
		t.Fatalf("session network endpoint: %d %s", rr.Code, rr.Body)
	}
	var netPage struct {
		Events []json.RawMessage `json:"events"`
		Next   int64             `json:"next"`
		Done   bool              `json:"done"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &netPage); err != nil || len(netPage.Events) == 0 {
		t.Fatalf("network page: %v %s", err, rr.Body)
	}
	recID := rec.Meta().ID
	idReq := httptest.NewRequest(http.MethodGet, "/api/recordings/"+recID+"/network?limit=2", nil)
	idReq.SetPathValue("recId", recID)
	rr2 := httptest.NewRecorder()
	s.handleRecordingNetworkByID(rr2, idReq)
	if rr2.Code != 200 {
		t.Fatalf("recording network endpoint: %d %s", rr2.Code, rr2.Body)
	}
	var netPage2 struct {
		Events []json.RawMessage `json:"events"`
		Next   int64             `json:"next"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &netPage2); err != nil || len(netPage2.Events) > 2 {
		t.Fatalf("limited network page: %v %s", err, rr2.Body)
	}

	// The download link writes through Browser.setDownloadBehavior into the
	// recording's downloads dir and logs browser/download events.
	dlExpr := `document.getElementById('dl').click()&&'clicked'`
	if code := post(`{"tool":"eval","expression":"` + dlExpr + `"}`); code != 200 {
		t.Fatalf("download click: %d", code)
	}
	dlDeadline := time.Now().Add(8 * time.Second)
	var filePath string
	for time.Now().Before(dlDeadline) {
		evs, _, _ := rec.Events(0, 0, []string{"download"})
		for _, ev := range evs {
			var d struct {
				State string `json:"state"`
				File  string `json:"file"`
			}
			if err := json.Unmarshal(ev.Data, &d); err == nil && d.State == "completed" {
				filePath = d.File
			}
		}
		if filePath != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if filePath == "" {
		t.Fatal("no completed download event recorded")
	}
	entries, err := os.ReadDir(rec.DownloadsDir())
	if err != nil || len(entries) == 0 {
		t.Fatalf("downloads dir empty: %v", err)
	}
	if strings.Contains(filePath, "/") && !strings.HasPrefix(filePath, "downloads/") {
		t.Fatalf("file field = %q, want downloads/<guid>", filePath)
	}
	if _, err := sess.StopRecording(); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
}
