package browser

// Recorder + recording store tests run without a browser: the recorder is
// pure file I/O and never touches CDP, so Session hooks are exercised by
// driving the lifecycle calls directly (markCrashed, Close-level interrupt
// goes through the same r.interrupt path).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/ids"
)

func testRecManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m := &Manager{
		log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		recordingsDir: dir,
		sessions:      map[string]*Session{},
		picks:         map[string]*Pick{},
		browsers:      map[string]*browserProc{},
	}
	return m, dir
}

// testRecSession is a minimal live session — recording needs only identity
// fields and a working status.
func testRecSession(m *Manager, convID string) *Session {
	s := &Session{
		m:              m,
		id:             ids.New("bs"),
		conversationID: convID,
		profile:        "default",
		status:         "active",
		createdAt:      nowMillis(),
	}
	m.sessions[s.id] = s
	return s
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func readMetaFile(t *testing.T, root, id string) RecMeta {
	t.Helper()
	meta, err := loadRecMeta(root, id)
	if err != nil {
		t.Fatalf("loadRecMeta(%s): %v", id, err)
	}
	return *meta
}

// Appends are queued and flushed by the writer goroutine; Events uses the
// flush barrier so the read-back is deterministic.
func TestRecorderSeqMonotonicAndReadBack(t *testing.T) {
	m, _ := testRecManager(t)
	sess := testRecSession(m, "cv1")
	if _, err := sess.StartRecording("staging"); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	r := sess.Recording()
	if r == nil {
		t.Fatal("Recording() must return the live recorder")
	}
	for i := range 5 {
		r.Append("dom", "click", map[string]any{"x": i, "y": i})
	}
	evs, next, done := r.Events(0, 0, nil)
	if len(evs) != 6 {
		t.Fatalf("events = %d, want 6 (state + 5 clicks)", len(evs))
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d seq = %d, want %d", i, ev.Seq, i+1)
		}
	}
	if evs[0].Type != "state" || evs[0].Origin != "system" {
		t.Fatalf("first event must be the recording state marker, got %+v", evs[0])
	}
	if next != evs[len(evs)-1].Seq {
		t.Fatalf("next = %d, want last seq %d", next, evs[len(evs)-1].Seq)
	}
	if done {
		t.Fatal("done must be false while the recording is live")
	}
	// Pagination + type filter.
	page, pnext, _ := r.Events(2, 2, nil)
	if len(page) != 2 || page[0].Seq != 3 || pnext != 4 {
		t.Fatalf("pagination returned %+v next=%d", page, pnext)
	}
	clicks, _, _ := r.Events(0, 0, []string{"click"})
	if len(clicks) != 5 {
		t.Fatalf("type filter returned %d clicks, want 5", len(clicks))
	}
	if got := r.Meta().Counts.Events; got != 6 {
		t.Fatalf("counts.events = %d, want 6", got)
	}
	if _, err := sess.StopRecording(); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
}

// Paused recordings drop appended events; the state markers bound the gap.
func TestRecorderPauseResumeGates(t *testing.T) {
	m, _ := testRecManager(t)
	sess := testRecSession(m, "cv1")
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	r := sess.Recording()
	r.Append("dom", "click", map[string]any{"x": 1})
	if err := sess.PauseRecording(); err != nil {
		t.Fatalf("PauseRecording: %v", err)
	}
	if err := sess.PauseRecording(); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("second pause err = %v, want ErrInvalidAction", err)
	}
	r.Append("dom", "click", map[string]any{"x": 99}) // dropped while paused
	if err := sess.ResumeRecording(); err != nil {
		t.Fatalf("ResumeRecording: %v", err)
	}
	if err := sess.ResumeRecording(); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("second resume err = %v, want ErrInvalidAction", err)
	}
	r.Append("dom", "click", map[string]any{"x": 2})
	evs, _, _ := r.Events(0, 0, nil)
	var clicks, states int
	var sawX99 bool
	for _, ev := range evs {
		switch ev.Type {
		case "click":
			clicks++
			var d struct {
				X int `json:"x"`
			}
			if json.Unmarshal(ev.Data, &d) == nil && d.X == 99 {
				sawX99 = true
			}
		case "state":
			states++
		}
	}
	if clicks != 2 || sawX99 {
		t.Fatalf("paused append must be dropped: clicks=%d sawX99=%v", clicks, sawX99)
	}
	if states != 3 { // recording, paused, recording(resumed)
		t.Fatalf("state markers = %d, want 3", states)
	}
	if _, err := sess.StopRecording(); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
	// Appends after stop drop silently instead of writing to a closed log.
	r.Append("dom", "click", map[string]any{"x": 7})
	evs, _, done := r.Events(0, 0, nil)
	for _, ev := range evs {
		var d struct {
			X int `json:"x"`
		}
		if ev.Type == "click" && json.Unmarshal(ev.Data, &d) == nil && d.X == 7 {
			t.Fatal("append after stop must not land in the log")
		}
	}
	if !done {
		t.Fatal("done must be true on a completed recording")
	}
}

// Stop finalizes: meta.json, events.jsonl, artifacts/, summary.txt all exist.
func TestRecorderStopFinalizes(t *testing.T) {
	m, root := testRecManager(t)
	sess := testRecSession(m, "cv1")
	meta0, err := sess.StartRecording("staging")
	if err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	r := sess.Recording()
	r.Append("dom", "click", map[string]any{
		"descriptor": map[string]any{"name": "Save", "selector": "button.save"},
	})
	r.Append("agent", "annotation", map[string]any{"annotationId": "a1", "kind": "element", "pickId": "pk_1"})
	r.Append("browser", "network_error", map[string]any{"method": "GET", "url": "https://ex.test/api", "status": 500})
	r.Append("browser", "navigate", map[string]any{"url": "https://ex.test/next"})
	meta, err := sess.StopRecording()
	if err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
	if meta.Status != recStatusCompleted || meta.EndedAt == 0 {
		t.Fatalf("meta not completed: %+v", meta)
	}
	if meta.Counts.Events != meta.LastSeq {
		t.Fatalf("counts.events %d != lastSeq %d", meta.Counts.Events, meta.LastSeq)
	}
	if meta.Counts.Annotations != 1 || meta.Counts.Errors != 1 {
		t.Fatalf("counts = %+v, want 1 annotation + 1 error", meta.Counts)
	}
	dir := filepath.Join(root, meta.ID)
	for _, name := range []string{"meta.json", "events.jsonl", "summary.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "artifacts")); err != nil || !st.IsDir() {
		t.Fatalf("artifacts dir missing: %v", err)
	}
	diskMeta := readMetaFile(t, root, meta.ID)
	if diskMeta.Status != recStatusCompleted || diskMeta.Counts.Events != meta.Counts.Events {
		t.Fatalf("disk meta not finalized: %+v", diskMeta)
	}
	summary, err := os.ReadFile(filepath.Join(dir, "summary.txt"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	text := string(summary)
	for _, want := range []string{"recording " + meta.ID, "Env: staging", "https://ex.test/next",
		"Events: 7", "Annotations: 1", "Errors: 1", "## Steps", "## Observations", "click \"Save\""} {
		if !contains(text, want) {
			t.Fatalf("summary missing %q:\n%s", want, text)
		}
	}
	if sess.Recording() != nil {
		t.Fatal("Recording() must be nil after stop")
	}
	// The same files resolve through the manager after the recorder is gone.
	rec, err := m.Recording(meta.ID)
	if err != nil {
		t.Fatalf("Manager.Recording: %v", err)
	}
	gotDir, _ := filepath.EvalSymlinks(rec.Dir())
	wantDir, _ := filepath.EvalSymlinks(dir)
	if gotDir != wantDir {
		t.Fatalf("dir = %q, want %q", gotDir, wantDir)
	}
	if _, _, done := rec.Events(0, 0, nil); !done {
		t.Fatal("done must be true for a completed recording")
	}
	if s, err := rec.Summary(); err != nil || s != text {
		t.Fatalf("summary read-back = %v / %q", err, s)
	}
	if meta0.ID != meta.ID {
		t.Fatalf("start meta id %q != stop meta id %q", meta0.ID, meta.ID)
	}
}

// A crashed session interrupts the recording and still writes summary.txt.
func TestRecorderInterruptOnCrash(t *testing.T) {
	m, root := testRecManager(t)
	sess := testRecSession(m, "cv1")
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	r := sess.Recording()
	r.Append("dom", "click", map[string]any{"x": 1})
	recID := r.Meta().ID
	sess.markCrashed("test crash")
	waitFor(t, "interrupted meta", func() bool {
		meta, err := loadRecMeta(root, recID)
		return err == nil && meta.Status == recStatusInterrupted
	})
	meta := readMetaFile(t, root, recID)
	if meta.InterruptedReason != "test crash" || meta.EndedAt == 0 {
		t.Fatalf("interrupt meta = %+v", meta)
	}
	if sess.Recording() != nil {
		t.Fatal("Recording() must be nil after a crash")
	}
	if _, err := os.Stat(filepath.Join(root, recID, "summary.txt")); err != nil {
		t.Fatalf("interrupted recordings also get summary.txt: %v", err)
	}
	rec, err := m.Recording(recID)
	if err != nil {
		t.Fatalf("Manager.Recording: %v", err)
	}
	evs, _, done := rec.Events(0, 0, nil)
	if !done || len(evs) == 0 {
		t.Fatalf("events after interrupt = %d done=%v", len(evs), done)
	}
	last := evs[len(evs)-1]
	if last.Type != "state" {
		t.Fatalf("last event = %+v, want interrupted state marker", last)
	}
}

// A jsonl line past the read budget is skipped, not treated as EOF: the
// cursor keeps moving and the page reports eof only at real end of file.
func TestRecEventsSkipsOversizedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	var b strings.Builder
	b.WriteString(`{"seq":1,"ts":1,"origin":"dom","type":"click","data":{"x":1}}` + "\n")
	// >4MB of padding — over the line cap; the content is valid JSON, the
	// reader must still drop it rather than stall the cursor on it.
	b.WriteString(`{"seq":2,"ts":2,"origin":"dom","type":"console","data":{"pad":"` + strings.Repeat("x", 5<<20) + `"}}` + "\n")
	b.WriteString(`{"seq":3,"ts":3,"origin":"dom","type":"click","data":{"x":3}}` + "\n")
	b.WriteString(`{"seq":4,"ts":4,"origin":"dom","type":"click","data":{"x":4}}`) // unterminated tail
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	evs, next, eof := readRecEvents(path, 0, 0, nil)
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3 (oversized line skipped)", len(evs))
	}
	if evs[0].Seq != 1 || evs[1].Seq != 3 || evs[2].Seq != 4 {
		t.Fatalf("seqs = %d,%d,%d", evs[0].Seq, evs[1].Seq, evs[2].Seq)
	}
	if next != 4 || !eof {
		t.Fatalf("cursor = %d eof=%v, want 4/true", next, eof)
	}
	// A second page from before the poisoned line still crosses it.
	evs2, next2, eof2 := readRecEvents(path, 1, 0, nil)
	if len(evs2) != 2 || next2 != 4 || !eof2 {
		t.Fatalf("repage = %d events next=%d eof=%v", len(evs2), next2, eof2)
	}
}

// The same skip applies to the network sidecar's own seq space.
func TestRecNetworkSkipsOversizedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.jsonl")
	var b strings.Builder
	b.WriteString(`{"seq":1,"stage":"request","url":"https://ex.test/a"}` + "\n")
	b.WriteString(`{"seq":2,"stage":"response","url":"` + strings.Repeat("y", 5<<20) + `"}` + "\n")
	b.WriteString(`{"seq":3,"stage":"finished"}` + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, next, eof := readRecNetwork(path, 0, 0)
	if len(lines) != 2 || next != 3 || !eof {
		t.Fatalf("network read = %d lines next=%d eof=%v", len(lines), next, eof)
	}
	var l struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal(lines[1], &l); err != nil || l.Seq != 3 {
		t.Fatalf("second line = %s", lines[1])
	}
}

// Free-text fields are capped at the writer: a huge URL or value records at
// the field cap with the ellipsis marker, and an event whose marshaled
// payload exceeds the line budget drops instead of writing a poison line.
func TestRecorderTruncatesOversizedFields(t *testing.T) {
	m, _ := testRecManager(t)
	sess := testRecSession(m, "cv1")
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	r := sess.Recording()
	big := strings.Repeat("z", 1<<20) // 1MB
	r.Append("browser", "navigate", map[string]any{"url": "https://ex.test/" + big})
	r.Append("dom", "input", map[string]any{"value": big})
	// One event over the whole-payload budget drops entirely.
	huge := map[string]any{}
	for i := range 40 {
		huge[fmt.Sprintf("k%d", i)] = strings.Repeat("w", 16<<10)
	}
	r.AppendRaw("dom", "click", nowMillis(), json.RawMessage(mustJSON(t, huge)))
	evs, _, _ := r.Events(0, 0, nil)
	var navs, inputs, clicks int
	for _, ev := range evs {
		var d struct {
			URL   string `json:"url"`
			Value string `json:"value"`
		}
		switch ev.Type {
		case "navigate":
			navs++
			if err := json.Unmarshal(ev.Data, &d); err != nil || len(d.URL) > recFieldCap+8 {
				t.Fatalf("navigate url len = %d (err %v)", len(d.URL), err)
			}
			if !strings.HasSuffix(d.URL, "…") {
				t.Fatalf("truncated url missing marker: %q", d.URL[len(d.URL)-16:])
			}
		case "input":
			inputs++
			if err := json.Unmarshal(ev.Data, &d); err != nil || len(d.Value) > recFieldCap+8 {
				t.Fatalf("input value len = %d (err %v)", len(d.Value), err)
			}
		case "click":
			clicks++
		}
	}
	if navs != 1 || inputs != 1 {
		t.Fatalf("nav=%d input=%d", navs, inputs)
	}
	if clicks != 0 {
		t.Fatalf("oversized payload must drop, got %d clicks", clicks)
	}
	if !r.Meta().Truncated {
		t.Fatal("dropped payload must flag the recording truncated")
	}
	if _, err := sess.StopRecording(); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Boot recovery: live-status metas become interrupted; completed ones are
// untouched. NewManager runs the same pass.
func TestRecordingBootRecovery(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UnixMilli()
	for id, status := range map[string]string{"rec_a": "recording", "rec_b": "paused", "rec_c": "completed"} {
		dir := filepath.Join(root, id)
		data, _ := json.Marshal(RecMeta{ID: id, SessionID: "s", Status: status, CreatedAt: now - 1000, StartedAt: now - 1000})
		if err := writeRecMeta(dir, data); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if err := recoverRecordings(root, now); err != nil {
		t.Fatalf("recoverRecordings: %v", err)
	}
	for _, id := range []string{"rec_a", "rec_b"} {
		meta := readMetaFile(t, root, id)
		if meta.Status != recStatusInterrupted || meta.InterruptedReason == "" || meta.EndedAt == 0 {
			t.Fatalf("recovered meta = %+v", meta)
		}
	}
	if meta := readMetaFile(t, root, "rec_c"); meta.Status != recStatusCompleted {
		t.Fatalf("completed recording was touched: %+v", meta)
	}
	// Through NewManager: the same pass runs on boot.
	root2 := t.TempDir()
	dir := filepath.Join(root2, "rec_stale")
	data, _ := json.Marshal(RecMeta{ID: "rec_stale", Status: "recording", CreatedAt: now - 1000})
	if err := writeRecMeta(dir, data); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	m := NewManager(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), RecordingsDir: root2})
	defer m.Close()
	if meta := readMetaFile(t, root2, "rec_stale"); meta.Status != recStatusInterrupted {
		t.Fatalf("boot did not recover stale recording: %+v", meta)
	}
}

// Boot recovery finalizes like Recorder.interrupt does: the recovered
// recording gets a summary.txt so /summary answers for it.
func TestRecordingBootRecoveryBuildsSummary(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UnixMilli()
	dir := filepath.Join(root, "rec_live")
	data, _ := json.Marshal(RecMeta{
		ID: "rec_live", SessionID: "bs_1", ConversationID: "cv1", Profile: "default",
		Status: "recording", CreatedAt: now - 5000, StartedAt: now - 5000,
		Counts: RecCounts{Events: 2}, LastSeq: 2,
	})
	if err := writeRecMeta(dir, data); err != nil {
		t.Fatal(err)
	}
	evs := `{"seq":1,"ts":1,"origin":"system","type":"state","data":{"state":"recording"}}` + "\n" +
		`{"seq":2,"ts":2,"origin":"dom","type":"click","data":{"x":1}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(evs), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverRecordings(root, now); err != nil {
		t.Fatalf("recoverRecordings: %v", err)
	}
	summary, err := os.ReadFile(filepath.Join(dir, "summary.txt"))
	if err != nil {
		t.Fatalf("recovered recording has no summary.txt: %v", err)
	}
	text := string(summary)
	for _, want := range []string{"rec_live", "interrupted", "server restarted", "click"} {
		if !contains(text, want) {
			t.Fatalf("summary missing %q:\n%s", want, text)
		}
	}
}

// The retention sweep deletes dirs past the cutoff but never a live-status or
// still-active recording.
func TestRecordingSweep(t *testing.T) {
	m, root := testRecManager(t)
	sess := testRecSession(m, "cv1")
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	liveID := sess.Recording().Meta().ID
	old := time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
	// Backdate the live recording's on-disk meta to look sweepable — the
	// isActive guard must save it anyway.
	staleMeta := RecMeta{ID: liveID, SessionID: sess.id, Status: "completed", CreatedAt: old, EndedAt: old}
	data, _ := json.Marshal(staleMeta)
	if err := writeRecMeta(filepath.Join(root, liveID), data); err != nil {
		t.Fatalf("backdate live meta: %v", err)
	}
	seed := func(id string, meta *RecMeta) {
		dir := filepath.Join(root, id)
		if meta != nil {
			data, _ := json.Marshal(meta)
			if err := writeRecMeta(dir, data); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		} else {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
			past := time.Now().Add(-8 * 24 * time.Hour)
			if err := os.Chtimes(dir, past, past); err != nil {
				t.Fatalf("chtimes %s: %v", id, err)
			}
		}
	}
	seed("rec_old", &RecMeta{ID: "rec_old", Status: "completed", CreatedAt: old, EndedAt: old})
	seed("rec_fresh", &RecMeta{ID: "rec_fresh", Status: "completed", CreatedAt: nowMillis(), EndedAt: nowMillis()})
	seed("rec_live", &RecMeta{ID: "rec_live", Status: "recording", CreatedAt: old, EndedAt: old})
	seed("rec_nometa", nil)
	// A readable, fresh meta wins over a stale dir mtime — the fallback only
	// applies when the meta is unreadable or has no usable timestamp.
	seed("rec_freshmeta", &RecMeta{ID: "rec_freshmeta", Status: "completed", CreatedAt: nowMillis(), EndedAt: nowMillis()})
	past := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "rec_freshmeta"), past, past); err != nil {
		t.Fatalf("chtimes rec_freshmeta: %v", err)
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	if err := sweepRecordings(root, cutoff, m.activeRecording); err != nil {
		t.Fatalf("sweepRecordings: %v", err)
	}
	for _, id := range []string{"rec_old", "rec_nometa"} {
		if _, err := os.Stat(filepath.Join(root, id)); !os.IsNotExist(err) {
			t.Fatalf("%s should be swept", id)
		}
	}
	for _, id := range []string{"rec_fresh", "rec_live", "rec_freshmeta", liveID} {
		if _, err := os.Stat(filepath.Join(root, id)); err != nil {
			t.Fatalf("%s must survive the sweep: %v", id, err)
		}
	}
	if _, err := sess.StopRecording(); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
}

// Artifact names are validated like pick ids: no separators, no traversal.
func TestRecordingArtifactPathValidation(t *testing.T) {
	dir := t.TempDir()
	rec := &Recording{dir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "artifacts", "shot.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := rec.ArtifactPath("shot.png")
	if err != nil {
		t.Fatalf("ArtifactPath: %v", err)
	}
	got, _ := filepath.EvalSymlinks(p)
	want, _ := filepath.EvalSymlinks(filepath.Join(dir, "artifacts", "shot.png"))
	if got != want {
		t.Fatalf("artifact path = %q, want %q", got, want)
	}
	for _, bad := range []string{"", "..", "../x", "a/b", `a\b`, "../artifacts/x", "shot.png/../x"} {
		if _, err := rec.ArtifactPath(bad); !errors.Is(err, ErrRecordingNotFound) {
			t.Fatalf("ArtifactPath(%q) err = %v, want ErrRecordingNotFound", bad, err)
		}
	}
	if _, err := rec.ArtifactPath("missing.png"); !errors.Is(err, ErrRecordingNotFound) {
		t.Fatalf("missing artifact err = %v", err)
	}
}

// Recorded text values run through the session's exact-match secret masking;
// a password input's value is always "[password]" — even over a registered
// secret — and no other field type is touched.
func TestRecorderMasksSecrets(t *testing.T) {
	m, _ := testRecManager(t)
	sess := testRecSession(m, "cv1")
	sess.RegisterSecrets(map[string]string{"hunter2": "[[env:PASS]]"})
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	r := sess.Recording()
	r.Append("dom", "input", map[string]any{
		"descriptor": map[string]any{"name": "pwd"}, "value": "hunter2",
	})
	r.Append("dom", "input", map[string]any{
		"inputType": "password", "value": "hunter2",
	})
	r.Append("dom", "input", map[string]any{
		"descriptor": map[string]any{"type": "password"}, "value": "plain",
	})
	evs, _, _ := r.Events(0, 0, []string{"input"})
	if len(evs) != 3 {
		t.Fatalf("input events = %d, want 3", len(evs))
	}
	var v struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(evs[0].Data, &v); err != nil || v.Value != "[[env:PASS]]" {
		t.Fatalf("registered secret not masked: %s", evs[0].Data)
	}
	if err := json.Unmarshal(evs[1].Data, &v); err != nil || v.Value != "[password]" {
		t.Fatalf("password input value = %q", evs[1].Data)
	}
	if err := json.Unmarshal(evs[2].Data, &v); err != nil || v.Value != "[password]" {
		t.Fatalf("descriptor password value = %q", evs[2].Data)
	}
	if _, err := sess.StopRecording(); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
}

func TestRecordingLifecycleErrors(t *testing.T) {
	m, _ := testRecManager(t)
	sess := testRecSession(m, "cv1")
	if err := sess.PauseRecording(); !errors.Is(err, ErrNoActiveRecording) {
		t.Fatalf("pause without recording = %v", err)
	}
	if _, err := sess.StopRecording(); !errors.Is(err, ErrNoActiveRecording) {
		t.Fatalf("stop without recording = %v", err)
	}
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	if _, err := sess.StartRecording(""); !errors.Is(err, ErrRecordingActive) {
		t.Fatalf("double start = %v, want ErrRecordingActive", err)
	}
	if _, err := sess.StopRecording(); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
	// No recordings dir configured: recording is unavailable.
	m2 := &Manager{log: slog.New(slog.NewTextHandler(io.Discard, nil)), sessions: map[string]*Session{}, picks: map[string]*Pick{}, browsers: map[string]*browserProc{}}
	sess2 := testRecSession(m2, "cv1")
	if _, err := sess2.StartRecording(""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("start without recordingsDir = %v, want ErrUnavailable", err)
	}
}

// Manager.Recordings lists per conversation, live metas merged over disk.
func TestManagerRecordingsList(t *testing.T) {
	m, _ := testRecManager(t)
	s1 := testRecSession(m, "cv1")
	s2 := testRecSession(m, "cv2")
	if _, err := s1.StartRecording(""); err != nil {
		t.Fatalf("s1 start: %v", err)
	}
	if _, err := s2.StartRecording(""); err != nil {
		t.Fatalf("s2 start: %v", err)
	}
	list := m.Recordings("cv1")
	if len(list) != 1 || list[0].ConversationID != "cv1" || list[0].Status != "recording" {
		t.Fatalf("list = %+v", list)
	}
	if len(m.Recordings("")) != 2 {
		t.Fatal("empty conversation id lists all recordings")
	}
	// After stop the listing comes from disk and still shows the recording.
	if _, err := s1.StopRecording(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	list = m.Recordings("cv1")
	if len(list) != 1 || list[0].Status != "completed" {
		t.Fatalf("post-stop list = %+v", list)
	}
	if _, err := s2.StopRecording(); err != nil {
		t.Fatalf("s2 stop: %v", err)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
