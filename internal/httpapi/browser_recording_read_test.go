// Recording read tools (browser_get_*) resolve the recording server-side
// from its files — no Chrome needed — so these tests seed a recordings dir
// and drive handleBrowserUse with a loopback grant, exactly like the MCP
// child would.
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/browser"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/mcp/grants"
	"github.com/jahrulnr/nusamation/internal/store"
)

const recTestConv = "cv_test"

// seedRec writes a recording dir the Manager's read path can serve:
// meta.json + events.jsonl + artifacts/ (+ summary.txt when given).
func seedRec(t *testing.T, root, id, convID, status, summary string, events []string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "artifacts", "downloads"), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{
		"id": id, "sessionId": "bs_1", "conversationId": convID,
		"profile": "default", "status": status, "env": "headless",
		"createdAt": 1, "startedAt": 1, "endedAt": 2, "lastSeq": int64(len(events)),
		"counts": map[string]any{"events": int64(len(events)), "annotations": 1, "errors": 0},
	}
	if status == "recording" || status == "paused" {
		delete(meta, "endedAt")
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"),
		[]byte(strings.Join(events, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if summary != "" {
		if err := os.WriteFile(filepath.Join(dir, "summary.txt"), []byte(summary), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var recTestEvents = []string{
	`{"seq":1,"ts":1,"origin":"system","type":"state","data":{"state":"recording"}}`,
	`{"seq":2,"ts":2,"origin":"dom","type":"annotation","data":{"pickId":"pk_1","marker":1,"kind":"region","rect":{"x":1,"y":2,"width":30,"height":40}}}`,
	`{"seq":3,"ts":3,"origin":"dom","type":"click","data":{"x":50,"y":60}}`,
	`{"seq":4,"ts":4,"origin":"system","type":"state","data":{"state":"completed"}}`,
}

// recReadServer wires a Server the way app.New does for the loopback: grants
// for auth, a Manager whose recordings dir is seeded — store is nil, so the
// token's conversation is trusted like the unit tests above assume. The
// picks dir comes back so tests can drop pick files into it.
func recReadServer(t *testing.T) (*Server, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	recDir := filepath.Join(dir, "recordings")
	picksDir := filepath.Join(dir, "picks")
	g := grants.New("")
	mgr := browser.NewManager(browser.Options{DataDir: picksDir, RecordingsDir: recDir})
	t.Cleanup(mgr.Close)
	s := &Server{browserUse: g, browser: mgr}
	return s, g.Token(recTestConv), recDir, picksDir
}

func recPost(t *testing.T, s *Server, token, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/browser-use", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	s.handleBrowserUse(rr, req)
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("%d %s: %v", rr.Code, rr.Body.String(), err)
	}
	return rr, out
}

// Status resolves the newest recording for the token's conversation and
// returns its meta — never a path or token.
func TestBrowserUseRecordingStatus(t *testing.T) {
	s, token, recDir, _ := recReadServer(t)
	seedRec(t, recDir, "rec_1", recTestConv, "completed", "done", recTestEvents)
	seedRec(t, recDir, "rec_other", "cv_other", "completed", "", recTestEvents) // another conversation — must not win

	rr, out := recPost(t, s, token, `{"tool":"browser_get_recording_status"}`)
	if rr.Code != 200 {
		t.Fatalf("status: %d %s", rr.Code, rr.Body.String())
	}
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
	rec, _ := result["recording"].(map[string]any)
	if rec["id"] != "rec_1" || rec["status"] != "completed" {
		t.Fatalf("recording = %v", rec)
	}
	if rec["counts"].(map[string]any)["events"].(float64) != 4 {
		t.Fatalf("counts = %v", rec["counts"])
	}
	if strings.Contains(rr.Body.String(), recDir) || strings.Contains(rr.Body.String(), token) {
		t.Fatalf("response leaks path or token: %s", rr.Body.String())
	}
}

// A conversation that was never recorded gets a clear result, not an error.
func TestBrowserUseRecordingStatusNone(t *testing.T) {
	s, token, _, _ := recReadServer(t)
	rr, out := recPost(t, s, token, `{"tool":"browser_get_recording_status"}`)
	if rr.Code != 200 {
		t.Fatalf("status none: %d %s", rr.Code, rr.Body.String())
	}
	detail, _ := out["detail"].(string)
	if !strings.Contains(detail, "No browser recording") {
		t.Fatalf("detail = %v", out)
	}
	if out["error"] != nil {
		t.Fatalf("no-recording must not be an error: %v", out)
	}
}

// The live session's recording wins over older completed ones — here the
// live claim comes from a "recording"-status meta (seeded after the
// manager's boot recovery, so it stays live).
func TestBrowserUseRecordingStatusLiveWins(t *testing.T) {
	s, token, recDir, _ := recReadServer(t)
	seedRec(t, recDir, "rec_old", recTestConv, "completed", "done", recTestEvents)
	seedRec(t, recDir, "rec_live", recTestConv, "recording", "", recTestEvents)

	_, out := recPost(t, s, token, `{"tool":"browser_get_recording_status"}`)
	rec := out["result"].(map[string]any)["recording"].(map[string]any)
	if rec["id"] != "rec_live" || rec["status"] != "recording" {
		t.Fatalf("live recording should win: %v", rec)
	}
}

// Timeline pages the event stream with after_seq + types.
func TestBrowserUseTimeline(t *testing.T) {
	s, token, recDir, _ := recReadServer(t)
	seedRec(t, recDir, "rec_1", recTestConv, "completed", "done", recTestEvents)

	_, out := recPost(t, s, token, `{"tool":"browser_get_timeline"}`)
	result := out["result"].(map[string]any)
	evs, _ := result["events"].([]any)
	if len(evs) != 4 || result["recordingId"] != "rec_1" {
		t.Fatalf("timeline = %v", result)
	}
	if result["done"] != true || result["next"].(float64) != 4 {
		t.Fatalf("cursor = next %v done %v", result["next"], result["done"])
	}

	_, out = recPost(t, s, token, `{"tool":"browser_get_timeline","after_seq":2,"types":["click"]}`)
	result = out["result"].(map[string]any)
	evs, _ = result["events"].([]any)
	if len(evs) != 1 {
		t.Fatalf("filtered timeline = %v", result)
	}
	if evs[0].(map[string]any)["type"] != "click" || evs[0].(map[string]any)["seq"].(float64) != 3 {
		t.Fatalf("filtered event = %v", evs[0])
	}
}

// An annotation event resolves to its pick: descriptor + upload ref ride the
// result; an unknown id 404s.
func TestBrowserUseAnnotation(t *testing.T) {
	s, token, recDir, picksDir := recReadServer(t)
	seedRec(t, recDir, "rec_1", recTestConv, "completed", "done", recTestEvents)
	pickJSON := `{"id":"pk_1","sessionId":"bs_1","conversationId":"cv_test","marker":1,"kind":"region",
		"descriptor":{"kind":"region","rect":{"x":1,"y":2,"width":30,"height":40},
			"pageUrl":"https://ex.test","title":"Ex","viewport":{"width":1280,"height":800,"dpr":1}},
		"upload":{"id":"up_1","name":"region-pk_1.png","mime":"image/png","size":3},"createdAt":1}`
	if err := os.MkdirAll(picksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(picksDir, "pk_1.json"), []byte(pickJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	_, out := recPost(t, s, token, `{"tool":"browser_get_annotation","annotation_id":"pk_1"}`)
	result := out["result"].(map[string]any)
	ann := result["annotation"].(map[string]any)
	if ann["pickId"] != "pk_1" || ann["kind"] != "region" {
		t.Fatalf("annotation = %v", ann)
	}
	pick := result["pick"].(map[string]any)
	if pick["upload"].(map[string]any)["id"] != "up_1" || pick["descriptor"].(map[string]any)["kind"] != "region" {
		t.Fatalf("pick = %v", pick)
	}

	rr, out := recPost(t, s, token, `{"tool":"browser_get_annotation","annotation_id":"pk_nope"}`)
	if rr.Code != 404 || out["code"] != "annotation_not_found" {
		t.Fatalf("missing annotation: %d %v", rr.Code, out)
	}
	rr, _ = recPost(t, s, token, `{"tool":"browser_get_annotation"}`)
	if rr.Code != 400 {
		t.Fatalf("missing annotation_id should 400, got %d", rr.Code)
	}
}

// Artifacts: images return image content, text returns bounded detail,
// downloads/<guid> resolves inside the sandboxed subdir, traversal 404s.
func TestBrowserUseArtifact(t *testing.T) {
	s, token, recDir, _ := recReadServer(t)
	dir := seedRec(t, recDir, "rec_1", recTestConv, "completed", "done", recTestEvents)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	for name, data := range map[string][]byte{
		"shot.png":     png,
		"notes.txt":    []byte("agent took this shot"),
		"report.bin":   {0x00, 0x01, 0x02},
		"downloads/f1": []byte("downloaded bytes"),
	} {
		p := filepath.Join(dir, "artifacts", name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, out := recPost(t, s, token, `{"tool":"browser_get_artifact","name":"shot.png"}`)
	if out["mimeType"] != "image/png" || out["image"] == "" {
		t.Fatalf("image artifact = %v", out)
	}
	_, out = recPost(t, s, token, `{"tool":"browser_get_artifact","name":"notes.txt"}`)
	if out["detail"] != "agent took this shot" {
		t.Fatalf("text artifact = %v", out)
	}
	_, out = recPost(t, s, token, `{"tool":"browser_get_artifact","name":"downloads/f1"}`)
	if out["detail"] != "downloaded bytes" {
		t.Fatalf("downloads artifact = %v", out)
	}
	rr, _ := recPost(t, s, token, `{"tool":"browser_get_artifact","name":"report.bin"}`)
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("binary artifact should 415, got %d", rr.Code)
	}
	for _, bad := range []string{`../meta.json`, `a/b/c`, `downloads/../meta.json`, `..\\x`, `downloads/`} {
		rr, _ = recPost(t, s, token, `{"tool":"browser_get_artifact","name":`+jsonQuote(bad)+`}`)
		if rr.Code == 200 {
			t.Fatalf("traversal name %q served", bad)
		}
	}
	rr, _ = recPost(t, s, token, `{"tool":"browser_get_artifact"}`)
	if rr.Code != 400 {
		t.Fatalf("missing name should 400, got %d", rr.Code)
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Summary returns summary.txt; a live recording without one reports it is
// still recording rather than erroring.
func TestBrowserUseRecordingSummary(t *testing.T) {
	s, token, recDir, _ := recReadServer(t)
	seedRec(t, recDir, "rec_1", recTestConv, "completed", "Recording rec_1\nEvents: 4", recTestEvents)
	_, out := recPost(t, s, token, `{"tool":"browser_get_recording_summary"}`)
	if out["detail"] != "Recording rec_1\nEvents: 4" {
		t.Fatalf("summary = %v", out)
	}
}

func TestBrowserUseRecordingSummaryLive(t *testing.T) {
	s, token, recDir, _ := recReadServer(t)
	seedRec(t, recDir, "rec_live", recTestConv, "recording", "", recTestEvents)
	_, out := recPost(t, s, token, `{"tool":"browser_get_recording_summary"}`)
	detail, _ := out["detail"].(string)
	if !strings.Contains(detail, "still recording") || out["error"] != nil {
		t.Fatalf("live summary = %v", out)
	}
}

// POST /messages?recordingId= resolves the recording server-side — unknown
// ids and recordings owned by another conversation are 400s; a good id lands
// in metadata.recording with the summary text so the prompt block can be
// built even if the files are swept afterwards.
func TestHandlePromptRecordingID(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "rec", Slug: "rec", Command: "/bin/true", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "rec conv", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateConversation(ctx, ag.ID, "other conv", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	agentMgr := agent.NewManager(st, logger)
	defer agentMgr.StopAll()
	svc := chat.New(chat.Options{
		Store: st, Agents: agentMgr, Channels: channels.NewManager(events.Nop{}, logger),
		Events: events.Nop{}, Log: logger,
		UploadDir: filepath.Join(dir, "uploads"), Workspace: dir,
	})
	agentMgr.SetSink(svc)
	recDir := filepath.Join(dir, "recordings")
	mgr := browser.NewManager(browser.Options{RecordingsDir: recDir})
	defer mgr.Close()
	s := &Server{store: st, chat: svc, browser: mgr}
	seedRec(t, recDir, "rec_1", conv.ID, "completed", "Recording rec_1\nEvents: 4", recTestEvents)
	seedRec(t, recDir, "rec_2", other.ID, "completed", "other", recTestEvents)
	seedRec(t, recDir, "rec_live", conv.ID, "recording", "", recTestEvents) // no summary.txt

	post := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/messages",
			strings.NewReader(body))
		req.SetPathValue("id", conv.ID)
		rr := httptest.NewRecorder()
		s.handlePrompt(rr, req)
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s: %v", rr.Code, rr.Body.String(), err)
		}
		return rr, out
	}

	rr, _ := post(`{"text":"watch this","recordingId":"rec_1"}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("send with recordingId: %d %s", rr.Code, rr.Body.String())
	}
	msgs, err := st.ListMessagesAscending(ctx, conv.ID, 0, 10)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("no message stored: %v %v", msgs, err)
	}
	var meta struct {
		Recording store.MessageRecording `json:"recording"`
	}
	if err := json.Unmarshal(msgs[0].Metadata, &meta); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if meta.Recording.RecordingID != "rec_1" || meta.Recording.Status != "completed" ||
		!strings.Contains(meta.Recording.Summary, "Events: 4") {
		t.Fatalf("metadata.recording = %+v", meta.Recording)
	}

	// A live recording has no summary.txt — the meta-derived brief rides
	// instead so the prompt still carries context; and a recording alone
	// satisfies the prompt guard (no text needed).
	rr, _ = post(`{"recordingId":"rec_live"}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("recording-only send: %d %s", rr.Code, rr.Body.String())
	}
	msgs, err = st.ListMessagesAscending(ctx, conv.ID, 0, 10)
	if err != nil || len(msgs) < 2 {
		t.Fatalf("second message missing: %v", msgs)
	}
	var meta2 struct {
		Recording store.MessageRecording `json:"recording"`
	}
	if err := json.Unmarshal(msgs[1].Metadata, &meta2); err != nil {
		t.Fatalf("second metadata: %v", err)
	}
	if meta2.Recording.RecordingID != "rec_live" || meta2.Recording.Status != "recording" ||
		!strings.Contains(meta2.Recording.Summary, "no finalized summary") {
		t.Fatalf("live metadata.recording = %+v", meta2.Recording)
	}

	rr, out := post(`{"recordingId":"rec_nope"}`)
	if rr.Code != 400 || out["code"] != "recording_invalid" {
		t.Fatalf("unknown recording: %d %v", rr.Code, out)
	}
	rr, out = post(`{"recordingId":"rec_2"}`)
	if rr.Code != 400 || out["code"] != "recording_invalid" {
		t.Fatalf("other conversation's recording: %d %v", rr.Code, out)
	}
}
