package browser

// Capture-source tests run without a browser: batches and CDP event structs
// feed the parsers/handlers directly, and every assertion lands on the
// recorder's own read path.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func testCapture(t *testing.T, sess *Session) (*Recorder, *recCapture) {
	t.Helper()
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	r := sess.Recording()
	cap := &recCapture{
		rec: r, rootFrame: "F1",
		pending: map[network.RequestID]*recNetReq{},
		guids:   map[string]bool{},
	}
	return r, cap
}

// The binding path and the drain path both end at appendDOMBatch: an event
// array is appended in order with origin dom and the page's ts; malformed
// entries drop silently.
func TestAppendDOMBatch(t *testing.T) {
	m, _ := testRecManager(t)
	sess := testRecSession(m, "cv1")
	r, _ := testCapture(t, sess)
	defer func() { _, _ = sess.StopRecording() }()

	r.appendDOMBatch([]byte(`[{"t":"click","ts":1000,"x":4,"y":5,"descriptor":{"tag":"button"}},
		{"t":"input","ts":1100,"value":"hi","descriptor":{"tag":"input"}},
		{"no-type":true},
		"garbage",
		{"t":"key","key":"Enter"}]`))
	// The drain path delivers a JSON string containing the same array.
	r.appendDOMBatch([]byte(`"[{\"t\":\"scroll\",\"ts\":1200,\"x\":0,\"y\":600}]"`))
	r.appendDOMBatch([]byte(`not json`))

	evs, _, _ := r.Events(0, 0, nil)
	var clicks, inputs, keys, scrolls int
	var clickTS, scrollTS int64
	for _, ev := range evs {
		if ev.Origin != "dom" && ev.Type != "state" {
			t.Fatalf("origin = %q, want dom", ev.Origin)
		}
		switch ev.Type {
		case "click":
			clicks++
			clickTS = ev.TS
		case "input":
			inputs++
		case "key":
			keys++
		case "scroll":
			scrolls++
			scrollTS = ev.TS
		}
	}
	if clicks != 1 || inputs != 1 || keys != 1 || scrolls != 1 {
		t.Fatalf("parsed events click=%d input=%d key=%d scroll=%d", clicks, inputs, keys, scrolls)
	}
	if clickTS != 1000 {
		t.Fatalf("click ts = %d, want the page's 1000", clickTS)
	}
	if scrollTS != 1200 {
		t.Fatalf("nested drain ts = %d, want 1200", scrollTS)
	}
}

// The raw batch keeps its page order — seq is assigned on write, not on the
// page clock.
func TestAppendDOMBatchPageOrder(t *testing.T) {
	m, _ := testRecManager(t)
	sess := testRecSession(m, "cv1")
	r, _ := testCapture(t, sess)
	defer func() { _, _ = sess.StopRecording() }()

	r.appendDOMBatch([]byte(`[{"t":"click","ts":2},{"t":"click","ts":1}]`))
	evs, _, _ := r.Events(0, 0, []string{"click"})
	if len(evs) != 2 {
		t.Fatalf("clicks = %d", len(evs))
	}
	var a, b struct {
		TS int64 `json:"ts"`
	}
	_ = json.Unmarshal(evs[0].Data, &a)
	_ = json.Unmarshal(evs[1].Data, &b)
	if a.TS != 2 || b.TS != 1 {
		t.Fatalf("order not preserved: %d then %d", a.TS, b.TS)
	}
}

// Every network line lands in the sidecar; only status>=400 and real load
// failures also reach events.jsonl as network_error.
func TestNetworkCaptureFiltering(t *testing.T) {
	m, _ := testRecManager(t)
	sess := testRecSession(m, "cv1")
	r, cap := testCapture(t, sess)
	defer func() { _, _ = sess.StopRecording() }()

	sendReq := func(id network.RequestID, url string) {
		cap.onRequest(network.EventRequestWillBeSent{
			RequestID:   id,
			DocumentURL: "https://ex.test/",
			Request:     &network.Request{Method: "GET", URL: url},
			Timestamp:   cdp.MonotonicTime(100), WallTime: cdp.TimeSinceEpoch(1700),
			Type: "Document", FrameID: "F1",
		})
	}
	// A 200 with a finished load: two sidecar lines, no timeline event.
	sendReq("r1", "https://ex.test/")
	cap.onResponse(network.EventResponseReceived{RequestID: "r1", Timestamp: 110,
		Response: &network.Response{URL: "https://ex.test/", Status: 200, MimeType: "text/html"}})
	cap.onFinished(network.EventLoadingFinished{RequestID: "r1", Timestamp: 150, EncodedDataLength: 1024})
	// A 404: timeline gets a network_error.
	sendReq("r2", "https://ex.test/missing")
	cap.onResponse(network.EventResponseReceived{RequestID: "r2", Timestamp: 120,
		Response: &network.Response{URL: "https://ex.test/missing", Status: 404, MimeType: "text/html"}})
	cap.onFinished(network.EventLoadingFinished{RequestID: "r2", Timestamp: 160})
	// A failed load: timeline gets a network_error with errorText.
	sendReq("r3", "https://ex.test/api")
	cap.onFailed(network.EventLoadingFailed{RequestID: "r3", Timestamp: 140,
		Type: "XHR", ErrorText: "net::ERR_FAILED"})
	// A canceled load: sidecar line only — an aborted load is not an error.
	sendReq("r4", "https://ex.test/aborted")
	cap.onFailed(network.EventLoadingFailed{RequestID: "r4", Timestamp: 145, Canceled: true, ErrorText: "net::ERR_ABORTED"})

	lines, next, _ := r.Network(0, 0)
	// r1: request+response+finished; r2: request+response+finished;
	// r3: request+failed; r4: request+failed = 10 sidecar lines.
	if len(lines) != 10 {
		t.Fatalf("network lines = %d, want 10", len(lines))
	}
	var first struct {
		Seq   int64  `json:"seq"`
		Stage string `json:"stage"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(lines[0], &first); err != nil || first.Seq != 1 || first.Stage != "request" {
		t.Fatalf("first line = %s", lines[0])
	}
	if next == 0 {
		t.Fatal("network cursor must advance")
	}
	// Pagination works over the sidecar's own seq space.
	page2, next2, _ := r.Network(next, 0)
	if len(page2) != 0 || next2 != next {
		t.Fatalf("past-end page = %d lines, next=%d", len(page2), next2)
	}
	after4, _, _ := r.Network(4, 0)
	if len(after4) != 6 {
		t.Fatalf("lines after seq 4 = %d, want 6", len(after4))
	}

	evs, _, _ := r.Events(0, 0, []string{"network_error"})
	if len(evs) != 2 {
		t.Fatalf("network_error events = %d, want 2 (404 + failed load)", len(evs))
	}
	var e struct {
		Status int    `json:"status"`
		Error  string `json:"error"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(evs[0].Data, &e); err != nil || e.Status != 404 || e.URL != "https://ex.test/missing" {
		t.Fatalf("404 event = %s", evs[0].Data)
	}
	if err := json.Unmarshal(evs[1].Data, &e); err != nil || e.Error != "net::ERR_FAILED" || e.URL != "https://ex.test/api" {
		t.Fatalf("failed event = %s", evs[1].Data)
	}
	for _, ev := range evs {
		if ev.Origin != "browser" {
			t.Fatalf("network_error origin = %q", ev.Origin)
		}
	}
	// Bodies/headers never enter the sidecar — assert the lines carry no
	// body/cookie fields.
	for _, line := range lines {
		var fields map[string]any
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatalf("line not JSON: %s", line)
		}
		for k := range fields {
			switch k {
			case "body", "postData", "headers", "cookie", "authorization":
				t.Fatalf("forbidden field %q in %s", k, line)
			}
		}
	}
}

// Downloads attribute by root frame: willBegin lands a started event,
// progress/completed follow the guid, other frames' downloads are ignored.
func TestDownloadEventMapping(t *testing.T) {
	m, _ := testRecManager(t)
	sess := testRecSession(m, "cv1")
	r, cap := testCapture(t, sess)
	defer func() { _, _ = sess.StopRecording() }()

	cap.onDownloadBegin(browser.EventDownloadWillBegin{
		FrameID: "F1", GUID: "g-1", URL: "https://ex.test/f.zip", SuggestedFilename: "f.zip"})
	cap.onDownloadProgress(browser.EventDownloadProgress{
		GUID: "g-1", TotalBytes: 100, ReceivedBytes: 40, State: browser.DownloadProgressStateInProgress})
	cap.onDownloadProgress(browser.EventDownloadProgress{
		GUID: "g-1", TotalBytes: 100, ReceivedBytes: 100, State: browser.DownloadProgressStateCompleted,
		FilePath: "/tmp/dl/g-1"})
	// Another frame's download and an unknown guid's progress: both ignored.
	cap.onDownloadBegin(browser.EventDownloadWillBegin{
		FrameID: "F2", GUID: "g-2", URL: "https://ex.test/other", SuggestedFilename: "o.bin"})
	cap.onDownloadProgress(browser.EventDownloadProgress{
		GUID: "g-3", State: browser.DownloadProgressStateCompleted})

	evs, _, _ := r.Events(0, 0, []string{"download"})
	if len(evs) != 3 {
		t.Fatalf("download events = %d, want 3", len(evs))
	}
	var d struct {
		GUID  string `json:"guid"`
		State string `json:"state"`
		URL   string `json:"url"`
		Name  string `json:"suggestedFilename"`
		File  string `json:"file"`
		Recv  int64  `json:"receivedBytes"`
	}
	if err := json.Unmarshal(evs[0].Data, &d); err != nil ||
		d.GUID != "g-1" || d.State != "started" || d.Name != "f.zip" || d.URL != "https://ex.test/f.zip" {
		t.Fatalf("started event = %s", evs[0].Data)
	}
	if err := json.Unmarshal(evs[1].Data, &d); err != nil || d.State != "progress" || d.Recv != 40 {
		t.Fatalf("progress event = %s", evs[1].Data)
	}
	if err := json.Unmarshal(evs[2].Data, &d); err != nil || d.State != "completed" || d.File != "downloads/g-1" {
		t.Fatalf("completed event = %s", evs[2].Data)
	}
	for _, ev := range evs {
		if ev.Origin != "browser" {
			t.Fatalf("download origin = %q", ev.Origin)
		}
	}
	// A canceled download is removed from attribution and logged once.
	cap.onDownloadBegin(browser.EventDownloadWillBegin{FrameID: "F1", GUID: "g-4", URL: "u", SuggestedFilename: "n"})
	cap.onDownloadProgress(browser.EventDownloadProgress{GUID: "g-4", State: browser.DownloadProgressStateCanceled})
	cap.onDownloadProgress(browser.EventDownloadProgress{GUID: "g-4", State: browser.DownloadProgressStateCanceled})
	evs2, _, _ := r.Events(0, 0, []string{"download"})
	if len(evs2) != 5 { // 3 above + started + one canceled
		t.Fatalf("post-cancel download events = %d, want 5", len(evs2))
	}
}

// Viewport notes carry the session's current device profile, origin-tagged
// by the caller (agent tools "browser", pane "system").
func TestNoteViewport(t *testing.T) {
	m, _ := testRecManager(t)
	sess := testRecSession(m, "cv1")
	sess.NoteViewport("system") // no recorder: must be a no-op
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	defer func() { _, _ = sess.StopRecording() }()
	sess.device = DeviceOpts{Width: 1280, Height: 800, DPR: 2, Mobile: true}
	sess.NoteViewport("browser")
	sess.NoteViewport("system")
	evs, _, _ := sess.Recording().Events(0, 0, []string{"viewport"})
	if len(evs) != 2 {
		t.Fatalf("viewport events = %d", len(evs))
	}
	var v struct {
		Width  int64   `json:"width"`
		Height int64   `json:"height"`
		DPR    float64 `json:"dpr"`
		Mobile bool    `json:"mobile"`
	}
	if err := json.Unmarshal(evs[0].Data, &v); err != nil ||
		v.Width != 1280 || v.Height != 800 || v.DPR != 2 || !v.Mobile {
		t.Fatalf("viewport = %s", evs[0].Data)
	}
	if evs[0].Origin != "browser" || evs[1].Origin != "system" {
		t.Fatalf("origins = %q/%q", evs[0].Origin, evs[1].Origin)
	}
}

// Lease transitions land as system state markers.
func TestLeaseStateMarkers(t *testing.T) {
	m, _ := testRecManager(t)
	sess := testRecSession(m, "cv1")
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	defer func() { _, _ = sess.StopRecording() }()
	sess.SetAgentLock(true)
	sess.SetAgentLock(false)
	evs, _, _ := sess.Recording().Events(0, 0, []string{"state"})
	var locks int
	for _, ev := range evs {
		var d struct {
			State string `json:"state"`
			On    bool   `json:"on"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			continue
		}
		if d.State == "agent_lock" {
			locks++
		}
	}
	if locks != 2 {
		t.Fatalf("agent_lock markers = %d, want 2", locks)
	}
}

// A stored recording's network.jsonl paginates exactly like events.jsonl.
func TestRecordingNetworkReadBack(t *testing.T) {
	m, root := testRecManager(t)
	sess := testRecSession(m, "cv1")
	r, cap := testCapture(t, sess)
	cap.onRequest(network.EventRequestWillBeSent{
		RequestID: "r1", Request: &network.Request{Method: "GET", URL: "https://ex.test/a"},
		Timestamp: 1, WallTime: 1700})
	cap.onRequest(network.EventRequestWillBeSent{
		RequestID: "r2", Request: &network.Request{Method: "POST", URL: "https://ex.test/b"},
		Timestamp: 2, WallTime: 1700})
	meta, err := sess.StopRecording()
	if err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
	rec, err := m.Recording(meta.ID)
	if err != nil {
		t.Fatalf("Manager.Recording: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, meta.ID, "network.jsonl")); err != nil {
		t.Fatalf("network.jsonl missing: %v", err)
	}
	lines, next, done := rec.Network(0, 0)
	if len(lines) != 2 || !done {
		t.Fatalf("stored network = %d lines done=%v", len(lines), done)
	}
	var l2 struct {
		Seq    int64  `json:"seq"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(lines[1], &l2); err != nil || l2.Seq != 2 || l2.Method != "POST" {
		t.Fatalf("line 2 = %s", lines[1])
	}
	if next != 2 {
		t.Fatalf("next = %d", next)
	}
	tail, _, done2 := rec.Network(1, 2)
	if len(tail) != 1 || !done2 {
		t.Fatalf("paged network = %d done=%v", len(tail), done2)
	}
	// A recording with no network.jsonl reads as an empty finished page.
	rec2 := &Recording{dir: t.TempDir(), meta: RecMeta{Status: "completed"}}
	l, n, d := rec2.Network(0, 0)
	if len(l) != 0 || n != 0 || !d {
		t.Fatalf("missing sidecar = %v/%d/%v", l, n, d)
	}
	_ = r
}

// The context-wide download behavior is owned by the last armer on a shared
// browser: an older capture's release is a no-op, the current holder's
// release re-arms the previous holder's dir, and the final release restores
// the CDP default.
func TestDownloadArmOwnership(t *testing.T) {
	m, _ := testRecManager(t)
	sessA := testRecSession(m, "cvA")
	sessB := testRecSession(m, "cvB")
	rA, capA := testCapture(t, sessA)
	_, capB := testCapture(t, sessB)
	defer func() { _, _ = sessA.StopRecording(); _, _ = sessB.StopRecording() }()
	key := &chromedp.Browser{}
	arm := func(cap *recCapture) {
		recDlMu.Lock()
		cap.dlKey = key
		recDlArms[key] = append(recDlArms[key], cap)
		recDlMu.Unlock()
	}
	release := func(cap *recCapture) (string, bool) {
		recDlMu.Lock()
		defer recDlMu.Unlock()
		return cap.releaseDownloadArm()
	}
	arm(capA)
	arm(capB)
	// The holder B releases first while A still records: the previous
	// armer's dir is re-armed so A's live capture keeps its downloads.
	if dir, apply := release(capB); !apply || dir != rA.DownloadsDir() {
		t.Fatalf("holder release = %q,%v, want %q,true", dir, apply, rA.DownloadsDir())
	}
	// A alone holds now: its release restores the default and empties the list.
	if dir, apply := release(capA); !apply || dir != "" {
		t.Fatalf("last release = %q,%v, want default restore", dir, apply)
	}
	if len(recDlArms) != 0 {
		t.Fatal("arm list must empty out")
	}
	// The reported bug order: the older armer tears down while the newer
	// still holds — A's release must not touch the context at all.
	arm(capA)
	arm(capB)
	if dir, apply := release(capA); apply || dir != "" {
		t.Fatalf("older armer release = %q,%v, want no-op", dir, apply)
	}
	if dir, apply := release(capB); !apply || dir != "" {
		t.Fatalf("final holder release = %q,%v, want default restore", dir, apply)
	}
	// A capture that never armed is a no-op even on its own browser.
	capC := &recCapture{dlKey: key}
	if dir, apply := release(capC); apply || dir != "" {
		t.Fatalf("never-armed release = %q,%v, want no-op", dir, apply)
	}
}

// The in-page flush restore drops the oldest events on overflow — pop()
// would drop the newest, opposite of push()'s shift().
func TestRecordingTapRestoresOldestDrop(t *testing.T) {
	if !strings.Contains(recordingTapJS, "buf.splice(0, buf.length - CAP)") {
		t.Fatal("flush restore must drop the oldest via splice(0, …)")
	}
	if strings.Contains(recordingTapJS, "buf.pop()") {
		t.Fatal("restore must not pop the newest events")
	}
}

// The downloads/ artifact prefix reaches captured downloads; traversal stays
// rejected.
func TestArtifactPathDownloadsPrefix(t *testing.T) {
	dir := t.TempDir()
	rec := &Recording{dir: dir}
	dl := filepath.Join(dir, "artifacts", "downloads")
	if err := os.MkdirAll(dl, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dl, "g-1"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := rec.ArtifactPath("downloads/g-1")
	if err != nil {
		t.Fatalf("ArtifactPath downloads: %v", err)
	}
	got, _ := filepath.EvalSymlinks(p)
	want, _ := filepath.EvalSymlinks(filepath.Join(dl, "g-1"))
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	for _, bad := range []string{"downloads/../x", "downloads/", "downloads/a/b", "downloads/.."} {
		if _, err := rec.ArtifactPath(bad); !errors.Is(err, ErrRecordingNotFound) {
			t.Fatalf("ArtifactPath(%q) err = %v", bad, err)
		}
	}
}
