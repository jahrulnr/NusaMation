package browser

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/fsutil"
)

// Recording file layout under <data>/browser-recordings/<id>/:
//
//	meta.json        RecMeta — atomic writes on every status change (~1s while live)
//	events.jsonl     one RecEvent per line, seq monotonic
//	artifacts/       rec-scoped screenshots/dom snapshots
//	summary.txt      built at finalize (stop or interrupt)
//
// Storage is file-only — no SQLite — so completed recordings stay readable
// after the browser session and even the server process are gone.

const recRetention = 7 * 24 * time.Hour

// validRecID mirrors validPickID: recording ids and artifact names come from
// request paths and must never reach the filesystem unvalidated.
func validRecID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`) && !strings.Contains(id, "..")
}

func writeRecMeta(dir string, data []byte) error {
	return fsutil.WriteFileAtomic(dir, "meta.json", data, 0o600)
}

func loadRecMeta(root, id string) (*RecMeta, error) {
	if !validRecID(id) {
		return nil, fmt.Errorf("invalid recording id %q", id)
	}
	data, err := os.ReadFile(filepath.Join(root, id, "meta.json"))
	if err != nil {
		return nil, err
	}
	var meta RecMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	if meta.ID != id {
		return nil, fmt.Errorf("recording id mismatch: dir %q holds %q", id, meta.ID)
	}
	return &meta, nil
}

// claimSessionRecordings rebinds finalized recordings owned by sessionID whose
// meta was written while the session had no conversation — a draft room can
// record and stop before the message that materializes it.
func claimSessionRecordings(root, sessionID, conversationID string, log *slog.Logger) {
	if root == "" {
		return
	}
	for _, meta := range listRecMetas(root) {
		if meta.SessionID != sessionID || meta.ConversationID != "" {
			continue
		}
		meta.ConversationID = conversationID
		data, err := json.Marshal(meta)
		if err == nil {
			err = writeRecMeta(filepath.Join(root, meta.ID), data)
		}
		if err != nil {
			log.Debug("claimed recording meta not rewritten", "recording", meta.ID, "error", err)
		}
	}
}

// listRecMetas scans the recordings dir; a bad meta in one dir does not stop
// the listing of the others.
func listRecMetas(root string) []RecMeta {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := []RecMeta{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if meta, err := loadRecMeta(root, e.Name()); err == nil {
			out = append(out, *meta)
		}
	}
	return out
}

// recLineCap is the per-line read budget — same 4MB the old bufio.Scanner
// enforced. A line past it is drained to its newline and skipped instead of
// ending the scan, so one oversized line can never wedge the seq cursor.
const recLineCap = 4 << 20

// recLineReader returns each jsonl line without its trailing newline; the
// final unterminated line counts as a line, like Scanner's last token.
type recLineReader struct {
	br  *bufio.Reader
	buf []byte // accumulated fragments of the current line (≤ recLineCap)
	err error  // terminal error — io.EOF or a real read failure
}

func newRecLineReader(f *os.File) *recLineReader {
	return &recLineReader{br: bufio.NewReaderSize(f, 64<<10)}
}

// next returns the next line, or false at end of file / on a read error.
// Lines over recLineCap are drained and skipped rather than treated as EOF.
func (r *recLineReader) next() ([]byte, bool) {
	for {
		if r.err != nil {
			return nil, false
		}
		frag, err := r.br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			r.buf = append(r.buf, frag...)
			if len(r.buf) <= recLineCap {
				continue
			}
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.br.ReadSlice('\n')
			}
			r.err = err // io.EOF ends the scan; nil resumes it on the next call
			r.buf = nil
			continue
		}
		r.err = err
		if len(r.buf) > 0 {
			r.buf = append(r.buf, frag...)
			frag = r.buf
			r.buf = nil
		}
		if err != nil && len(frag) == 0 {
			return nil, false
		}
		if n := len(frag); n > 0 && frag[n-1] == '\n' {
			frag = frag[:n-1]
		}
		return frag, true
	}
}

// readRecEvents streams events.jsonl and returns the matching events, the seq
// cursor for the next poll, and whether the scan reached end of file. A
// missing or truncated tail line just ends the scan.
func readRecEvents(path string, after int64, limit int, types []string) (events []RecEvent, next int64, eof bool) {
	if limit <= 0 {
		limit = recEventsDefaultLimit
	}
	if limit > recEventsMaxLimit {
		limit = recEventsMaxLimit
	}
	var typeSet map[string]bool
	for _, t := range types {
		if t = strings.TrimSpace(t); t != "" {
			if typeSet == nil {
				typeSet = map[string]bool{}
			}
			typeSet[t] = true
		}
	}
	next = after
	f, err := os.Open(path)
	if err != nil {
		return nil, next, true
	}
	defer f.Close()
	lr := newRecLineReader(f)
	for {
		line, ok := lr.next()
		if !ok {
			break
		}
		var ev RecEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Seq <= after {
			continue
		}
		if typeSet != nil && !typeSet[ev.Type] {
			continue
		}
		events = append(events, ev)
		next = ev.Seq
		if len(events) >= limit {
			return events, next, false
		}
	}
	return events, next, true
}

// readRecNetwork streams network.jsonl with the same pagination contract as
// readRecEvents: every line carries the sidecar's own seq, and the cursor is
// that seq. A missing file ends immediately with eof.
func readRecNetwork(path string, after int64, limit int) (lines []json.RawMessage, next int64, eof bool) {
	if limit <= 0 {
		limit = recEventsDefaultLimit
	}
	if limit > recEventsMaxLimit {
		limit = recEventsMaxLimit
	}
	next = after
	f, err := os.Open(path)
	if err != nil {
		return nil, next, true
	}
	defer f.Close()
	lr := newRecLineReader(f)
	for {
		line, ok := lr.next()
		if !ok {
			break
		}
		var head struct {
			Seq int64 `json:"seq"`
		}
		if json.Unmarshal(line, &head) != nil || head.Seq <= after {
			continue
		}
		lines = append(lines, json.RawMessage(append([]byte(nil), line...)))
		next = head.Seq
		if len(lines) >= limit {
			return lines, next, false
		}
	}
	return lines, next, true
}

// Recording is a read handle over one recording's files, live or finalized.
// A live recorder answers Events through its flush barrier; a finalized one
// reads the files directly — both paths share readRecEvents.
type Recording struct {
	dir  string
	live *Recorder
	meta RecMeta // disk snapshot, only used when live == nil
}

// Meta returns fresh meta for a live recording, the disk snapshot otherwise.
func (r *Recording) Meta() RecMeta {
	if r.live != nil {
		return r.live.Meta()
	}
	return r.meta
}

// Dir is the recording's directory under the recordings root.
func (r *Recording) Dir() string { return r.dir }

func (r *Recording) Events(after int64, limit int, types []string) ([]RecEvent, int64, bool) {
	if r.live != nil {
		return r.live.Events(after, limit, types)
	}
	evs, next, eof := readRecEvents(filepath.Join(r.dir, "events.jsonl"), after, limit, types)
	return evs, next, eof && recTerminal(r.meta.Status)
}

// Network reads the sidecar request log — same pagination contract as
// Events over its own seq space.
func (r *Recording) Network(after int64, limit int) ([]json.RawMessage, int64, bool) {
	if r.live != nil {
		return r.live.Network(after, limit)
	}
	lines, next, eof := readRecNetwork(filepath.Join(r.dir, "network.jsonl"), after, limit)
	return lines, next, eof && recTerminal(r.meta.Status)
}

// Summary returns summary.txt; only finalized recordings have one.
func (r *Recording) Summary() (string, error) {
	data, err := os.ReadFile(filepath.Join(r.dir, "summary.txt"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrSummaryUnavailable
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ArtifactPath resolves an artifact file inside the recording dir; the name
// is validated like a pick id so no traversal is possible. A "downloads/"
// prefix reaches captured downloads (each named by its download guid).
func (r *Recording) ArtifactPath(name string) (string, error) {
	sub := ""
	if rest, ok := strings.CutPrefix(name, "downloads/"); ok {
		sub, name = "downloads", rest
	}
	if !validRecID(name) {
		return "", ErrRecordingNotFound
	}
	p := filepath.Join(r.dir, "artifacts", sub, name)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return "", ErrRecordingNotFound
	}
	return p, nil
}

// Recording resolves a recording id to a read handle: the live recorder wins
// (its buffered tail would otherwise lag ~1s), then meta.json on disk — which
// keeps completed recordings readable after the session died.
func (m *Manager) Recording(id string) (*Recording, error) {
	if !validRecID(id) || m.recordingsDir == "" {
		return nil, ErrRecordingNotFound
	}
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		if r := s.Recording(); r != nil && r.id == id {
			return &Recording{dir: r.dir, live: r}, nil
		}
	}
	meta, err := loadRecMeta(m.recordingsDir, id)
	if err != nil {
		return nil, ErrRecordingNotFound
	}
	return &Recording{dir: filepath.Join(m.recordingsDir, id), meta: *meta}, nil
}

// Recordings lists metas for one conversation (empty matches all), live
// recorders merged over their ~1s-stale disk snapshots, newest first.
func (m *Manager) Recordings(conversationID string) []RecMeta {
	out := []RecMeta{}
	index := map[string]int{}
	match := func(meta RecMeta) bool {
		return conversationID == "" || meta.ConversationID == conversationID
	}
	if m.recordingsDir != "" {
		for _, meta := range listRecMetas(m.recordingsDir) {
			if match(meta) {
				index[meta.ID] = len(out)
				out = append(out, meta)
			}
		}
	}
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		if r := s.Recording(); r != nil {
			meta := r.Meta()
			if !match(meta) {
				continue
			}
			if i, ok := index[meta.ID]; ok {
				out[i] = meta
			} else {
				index[meta.ID] = len(out)
				out = append(out, meta)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// activeRecording reports whether id is a live session's recording, so the
// retention sweep never deletes a dir whose writer goroutine still owns it.
func (m *Manager) activeRecording(id string) bool {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		if r := s.Recording(); r != nil && r.id == id {
			return true
		}
	}
	return false
}

// recoverRecordings marks live-status metas interrupted at boot: browser
// sessions never survive a server restart, so a recording still claiming
// recording/paused/finalizing is stale by definition.
func recoverRecordings(root string, now int64) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := loadRecMeta(root, e.Name())
		if err != nil {
			continue
		}
		switch meta.Status {
		case recStatusRecording, recStatusPaused, recStatusFinalizing:
		default:
			continue
		}
		meta.Status = recStatusInterrupted
		if meta.InterruptedReason == "" {
			meta.InterruptedReason = "server restarted"
		}
		if meta.EndedAt == 0 {
			meta.EndedAt = now
		}
		data, err := json.Marshal(meta)
		if err != nil {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if err := writeRecMeta(dir, data); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Finalize like Recorder.interrupt does: an interrupted recording
		// still answers /summary.
		if err := buildRecSummary(dir, *meta, nil); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// sweepRecordings removes recording dirs older than the cutoff (endedAt when
// set, else createdAt, else dir mtime). Live-status metas and ids the manager
// still reports active are never removed — sweeping an owned dir would orphan
// its writer goroutine.
func sweepRecordings(root string, cutoff int64, isActive func(string) bool) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if isActive != nil && isActive(id) {
			continue
		}
		stale := false
		if meta, err := loadRecMeta(root, id); err == nil {
			switch meta.Status {
			case recStatusRecording, recStatusPaused, recStatusFinalizing:
				continue
			}
			at := meta.EndedAt
			if at == 0 {
				at = meta.CreatedAt
			}
			if at > 0 {
				// A readable meta decides on its own timestamps — the dir
				// mtime must never override a fresh recording.
				stale = at < cutoff
			} else if fi, err := e.Info(); err == nil {
				stale = fi.ModTime().UnixMilli() < cutoff
			}
		} else if fi, err := e.Info(); err == nil {
			// No usable meta: the dir mtime is the only staleness signal.
			stale = fi.ModTime().UnixMilli() < cutoff
		}
		if stale {
			if err := os.RemoveAll(filepath.Join(root, id)); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// ---------- summary ----------

// summaryLineCap bounds each section so the summary stays a quick scan
// (~60 lines) even on a long recording.
const (
	summaryScanCap      = 20000 // jsonl lines read for one summary build
	summaryAnnotations  = 20
	summarySteps        = 30
	summaryObservations = 15
)

// buildRecSummary writes summary.txt: header, annotations, a step outline
// from the interaction events, and observations (console/network errors,
// interrupted marker). Zero-event recordings still get header + "(none)"
// sections.
func buildRecSummary(dir string, meta RecMeta, mask func(string) string) error {
	if mask == nil {
		mask = func(s string) string { return s }
	}
	evs, _, _ := readRecEvents(filepath.Join(dir, "events.jsonl"), 0, summaryScanCap, nil)
	var b strings.Builder
	fmt.Fprintf(&b, "NusaMation browser recording %s\n", meta.ID)
	status := meta.Status
	if meta.InterruptedReason != "" {
		status += " (" + meta.InterruptedReason + ")"
	}
	fmt.Fprintf(&b, "Status: %s\n", status)
	env := meta.Env
	if env == "" {
		env = "-"
	}
	fmt.Fprintf(&b, "Env: %s | Profile: %s | Session: %s | Conversation: %s\n",
		mask(env), meta.Profile, meta.SessionID, meta.ConversationID)
	start := time.UnixMilli(meta.StartedAt)
	end := meta.EndedAt
	if end == 0 {
		end = meta.StartedAt
	}
	dur := time.Duration(end-meta.StartedAt) * time.Millisecond
	fmt.Fprintf(&b, "Started: %s | Ended: %s | Duration: %s\n",
		start.Format(time.RFC3339), time.UnixMilli(end).Format(time.RFC3339), dur.Round(time.Second))
	lastURL := "-"
	for _, ev := range slices.Backward(evs) {
		if ev.Type == "navigate" {
			var d struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(ev.Data, &d) == nil && d.URL != "" {
				lastURL = d.URL
			}
			break
		}
	}
	fmt.Fprintf(&b, "URL: %s\n", mask(lastURL))
	fmt.Fprintf(&b, "Events: %d | Annotations: %d | Errors: %d\n",
		meta.Counts.Events, meta.Counts.Annotations, meta.Counts.Errors)
	if meta.Truncated {
		b.WriteString("Note: the event buffer overflowed; some events were dropped.\n")
	}

	// Annotations: {annotationId, kind, pickId?} per the contract.
	b.WriteString("\n## Annotations\n")
	shown, total := 0, 0
	for _, ev := range evs {
		if ev.Type != "annotation" {
			continue
		}
		total++
		if shown >= summaryAnnotations {
			continue
		}
		shown++
		var d struct {
			AnnotationID string `json:"annotationId"`
			Kind         string `json:"kind"`
			PickID       string `json:"pickId"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		label := d.AnnotationID
		if label == "" {
			label = d.PickID
		}
		fmt.Fprintf(&b, "- #%d %s %s\n", ev.Seq, d.Kind, label)
	}
	if total == 0 {
		b.WriteString("(none)\n")
	} else if total > shown {
		fmt.Fprintf(&b, "... and %d more\n", total-shown)
	}

	// Steps: the compact outline of dom + agent interactions.
	b.WriteString("\n## Steps\n")
	shown, total = 0, 0
	for _, ev := range evs {
		line := recStepLine(ev, mask)
		if line == "" {
			continue
		}
		total++
		if shown >= summarySteps {
			continue
		}
		shown++
		fmt.Fprintf(&b, "- #%d %s\n", ev.Seq, line)
	}
	if total == 0 {
		b.WriteString("(none)\n")
	} else if total > shown {
		fmt.Fprintf(&b, "... and %d more\n", total-shown)
	}

	b.WriteString("\n## Observations\n")
	shown, total = 0, 0
	for _, ev := range evs {
		line := recObservationLine(ev, mask)
		if line == "" {
			continue
		}
		total++
		if shown >= summaryObservations {
			continue
		}
		shown++
		fmt.Fprintf(&b, "- #%d %s\n", ev.Seq, line)
	}
	if total == 0 {
		b.WriteString("(none)\n")
	} else if total > shown {
		fmt.Fprintf(&b, "... and %d more\n", total-shown)
	}
	return fsutil.WriteFileAtomic(dir, "summary.txt", []byte(b.String()), 0o600)
}

// recStepLine renders one interaction event as a compact outline line; empty
// means the event is not a step (state/console/network live elsewhere).
func recStepLine(ev RecEvent, mask func(string) string) string {
	switch ev.Type {
	case "navigate":
		var d struct {
			URL string `json:"url"`
			SPA bool   `json:"spa"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		if d.SPA {
			return "spa " + mask(d.URL)
		}
		return "navigate " + mask(d.URL)
	case "click", "dblclick":
		return ev.Type + " " + recDescribe(ev.Data, mask)
	case "input", "change":
		var d struct {
			Value json.RawMessage `json:"value"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		desc := recDescribe(ev.Data, mask)
		val := "?"
		var s string
		if json.Unmarshal(d.Value, &s) == nil {
			val = quoteValue(mask(s), 60)
		}
		return ev.Type + " " + desc + " = " + val
	case "submit":
		return "submit " + recDescribe(ev.Data, mask)
	case "scroll":
		return "scroll " + recDescribe(ev.Data, mask)
	case "key":
		var d struct {
			Key string `json:"key"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		return "key " + d.Key
	case "tool":
		var d struct {
			Tool string `json:"tool"`
			OK   bool   `json:"ok"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		line := "tool " + d.Tool
		if !d.OK {
			line += " (failed)"
		}
		return line
	}
	return ""
}

// recDescribe renders a lite element descriptor ("name" <selector>) or falls
// back to the event's coordinates.
func recDescribe(raw json.RawMessage, mask func(string) string) string {
	var d struct {
		Descriptor struct {
			Name     string `json:"name"`
			Selector string `json:"selector"`
		} `json:"descriptor"`
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	_ = json.Unmarshal(raw, &d)
	name := mask(d.Descriptor.Name)
	if len(name) > 60 {
		name = name[:60] + "…"
	}
	switch {
	case name != "" && d.Descriptor.Selector != "":
		return fmt.Sprintf("%q <%s>", name, d.Descriptor.Selector)
	case name != "":
		return fmt.Sprintf("%q", name)
	case d.Descriptor.Selector != "":
		return "<" + d.Descriptor.Selector + ">"
	default:
		return fmt.Sprintf("(%g, %g)", d.X, d.Y)
	}
}

// recObservationLine renders failures and the interrupt marker.
func recObservationLine(ev RecEvent, mask func(string) string) string {
	switch ev.Type {
	case "network_error":
		var d struct {
			Method string `json:"method"`
			URL    string `json:"url"`
			Status int    `json:"status"`
			Error  string `json:"error"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		line := "request " + d.Method + " " + mask(d.URL)
		if d.Status != 0 {
			line += fmt.Sprintf(" -> %d", d.Status)
		}
		if d.Error != "" {
			line += " (" + mask(d.Error) + ")"
		}
		return line
	case "console":
		var d struct {
			Level string `json:"level"`
			Text  string `json:"text"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		if d.Level != "error" {
			return ""
		}
		text := mask(d.Text)
		if len(text) > 120 {
			text = text[:120] + "…"
		}
		return "console.error: " + text
	case "state":
		var d struct {
			State  string `json:"state"`
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		if d.State == recStatusInterrupted {
			return "interrupted: " + mask(d.Reason)
		}
	}
	return ""
}

// quoteValue renders a short quoted value for the step outline.
func quoteValue(s string, max int) string {
	if len(s) > max {
		s = s[:max] + "…"
	}
	return fmt.Sprintf("%q", s)
}
