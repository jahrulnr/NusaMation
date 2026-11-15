package browser

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jahrulnr/nusamation/internal/ids"
)

// Recording lifecycle: recording -> paused -> recording -> finalizing ->
// completed; interrupted on session crash/close or server restart. Every
// transition appends a {origin:"system", type:"state"} event so the timeline
// is self-describing.
const (
	recStatusRecording   = "recording"
	recStatusPaused      = "paused"
	recStatusFinalizing  = "finalizing"
	recStatusCompleted   = "completed"
	recStatusInterrupted = "interrupted"
)

var (
	ErrRecordingNotFound  = errors.New("recording not found")
	ErrNoActiveRecording  = errors.New("no active recording for this session")
	ErrRecordingActive    = errors.New("a recording is already active")
	ErrSummaryUnavailable = errors.New("recording summary is not available yet")
)

const (
	// recChanBacklog bounds the append queue: producers never block; overflow
	// drops the event and flags the recording truncated.
	recChanBacklog = 512
	// recFlushInterval is how often the writer goroutine flushes events.jsonl
	// and refreshes meta.json while a recording is live.
	recFlushInterval = time.Second

	recEventsDefaultLimit = 100
	recEventsMaxLimit     = 1000
)

const (
	// recFieldCap bounds any single string leaf in a recorded payload. The
	// log is line-oriented and one unbounded string (a data: URL, a console
	// dump, a giant tool arg) would make a jsonl line too big to page back.
	recFieldCap = 16 << 10
	// recMaxEventData bounds one marshaled event payload: per-field caps
	// cannot keep a hostile in-page batch entry below the read budget —
	// the tap is page-controlled JSON with an unbounded field count.
	recMaxEventData = 256 << 10
)

// recTruncate caps v at n bytes, backing off to a rune boundary so the
// recorded field stays valid UTF-8; the ellipsis marks the cut.
func recTruncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	cut := n
	for cut > 0 && !utf8.ValidString(v[:cut]) {
		cut--
	}
	return v[:cut] + "…"
}

// recTruncateStrings maps recTruncate over every string leaf — applied at
// the writer after secret masking so a registered secret still matches
// before the cap lands.
func recTruncateStrings(v any) any {
	switch t := v.(type) {
	case string:
		return recTruncate(t, recFieldCap)
	case []any:
		for i := range t {
			t[i] = recTruncateStrings(t[i])
		}
	case map[string]any:
		for k := range t {
			t[k] = recTruncateStrings(t[k])
		}
	}
	return v
}

// RecCounts tallies the kinds the summary reports on.
type RecCounts struct {
	Events      int64 `json:"events"`
	Annotations int64 `json:"annotations"`
	Errors      int64 `json:"errors"`
}

// RecMeta is the meta.json snapshot — rewritten atomically on every status
// change and refreshed by the writer goroutine about once a second.
type RecMeta struct {
	ID                string    `json:"id"`
	SessionID         string    `json:"sessionId"`
	ConversationID    string    `json:"conversationId"`
	Profile           string    `json:"profile"`
	Status            string    `json:"status"`
	Env               string    `json:"env"`
	CreatedAt         int64     `json:"createdAt"`
	StartedAt         int64     `json:"startedAt"`
	EndedAt           int64     `json:"endedAt,omitempty"`
	LastSeq           int64     `json:"lastSeq"`
	Counts            RecCounts `json:"counts"`
	InterruptedReason string    `json:"interruptedReason,omitempty"`
	Truncated         bool      `json:"truncated,omitempty"`
}

// RecEvent is one events.jsonl line. Seq is assigned by the writer goroutine,
// so event order is append order, not wire order.
type RecEvent struct {
	Seq    int64           `json:"seq"`
	TS     int64           `json:"ts"` // unix ms, assigned at append time
	Origin string          `json:"origin"`
	Type   string          `json:"type"`
	Data   json.RawMessage `json:"data"`
}

// recMsg is one unit of writer-goroutine work: an event to append, a
// network.jsonl sidecar line, a flush barrier (Events/Network readers), or
// a quit request (stop/interrupt).
type recMsg struct {
	ev    *RecEvent
	net   map[string]any
	flush chan error
	quit  chan error
}

// Recorder owns one recording's files: meta.json, events.jsonl, artifacts/,
// and summary.txt on finalize. The single writer goroutine is the only jsonl
// writer — it owns seq assignment, masking, flushing and event counts, so
// Append never blocks a producer and never makes CDP calls.
type Recorder struct {
	id   string
	dir  string
	log  *slog.Logger
	mask func(string) string

	mu   sync.Mutex
	meta RecMeta

	ch   chan recMsg
	done chan struct{} // closed when the writer goroutine exits
	fin  chan struct{} // closed once a terminal status is fully persisted
}

// newRecorder creates <root>/<id>/ and starts the writer goroutine with an
// initial recording-status meta already on disk.
func newRecorder(root, sessionID, conversationID, profile, env string, mask func(string) string, log *slog.Logger) (*Recorder, error) {
	if mask == nil {
		mask = func(s string) string { return s }
	}
	if log == nil {
		log = slog.Default()
	}
	id := ids.New("rec")
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o700); err != nil {
		return nil, err
	}
	now := nowMillis()
	r := &Recorder{
		id: id, dir: dir, log: log, mask: mask,
		ch:   make(chan recMsg, recChanBacklog),
		done: make(chan struct{}),
		fin:  make(chan struct{}),
	}
	r.meta = RecMeta{
		ID: id, SessionID: sessionID, ConversationID: conversationID,
		Profile: profile, Env: env, Status: recStatusRecording,
		CreatedAt: now, StartedAt: now,
	}
	if err := r.writeMeta(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	go r.run()
	r.appendState(recStatusRecording, nil)
	return r, nil
}

// writeMeta persists meta.json atomically; failures are logged, not fatal —
// the jsonl log is the source of truth for the timeline. The lock is held
// through the write so a stale flush can't overwrite a newer status.
func (r *Recorder) writeMeta() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeMetaLocked()
}

// enqueue is the only writer-goroutine entry point; a full queue drops the
// event and marks the recording truncated rather than stalling a producer.
func (r *Recorder) enqueue(ev *RecEvent) {
	select {
	case r.ch <- recMsg{ev: ev}:
	default:
		r.mu.Lock()
		r.meta.Truncated = true
		r.mu.Unlock()
	}
}

// enqueueState queues a lifecycle marker. Ordinary events drop when the
// queue is full, but transition markers must never be lost — the send waits
// until the writer takes it, and only the writer's exit abandons it. The
// state callers (start/pause/resume/stop/interrupt/tab swap) never sit on
// the input-dispatch hot path, and the writer drains in microseconds, so
// the bounded wait is safe.
func (r *Recorder) enqueueState(ev *RecEvent) {
	select {
	case r.ch <- recMsg{ev: ev}:
	case <-r.done:
	}
}

// appendState records a lifecycle marker outside the paused gate — transitions
// must always land in the log.
func (r *Recorder) appendState(state string, extra map[string]any) {
	data := map[string]any{"state": state}
	maps.Copy(data, extra)
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	r.enqueueState(&RecEvent{TS: nowMillis(), Origin: "system", Type: "state", Data: raw})
}

// Append queues one event for the writer goroutine. While the recording is
// paused (or already finalizing/closed) the event is dropped — the timeline
// records what happened while recording, not what was attempted; the paused
// and resumed state events still bound the gap. Append never blocks and never
// makes CDP calls, so it is safe on the input-dispatch path.
func (r *Recorder) Append(origin, typ string, data any) {
	r.mu.Lock()
	ok := r.meta.Status == recStatusRecording
	r.mu.Unlock()
	if !ok {
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	r.enqueue(&RecEvent{TS: nowMillis(), Origin: origin, Type: typ, Data: raw})
}

// AppendRaw queues an event whose payload is already JSON — the in-page tap
// path, where events carry the page's own timestamp. ts <= 0 falls back to
// the append time; the paused/closed gate matches Append.
func (r *Recorder) AppendRaw(origin, typ string, ts int64, raw json.RawMessage) {
	r.mu.Lock()
	ok := r.meta.Status == recStatusRecording
	r.mu.Unlock()
	if !ok || len(raw) == 0 || typ == "" {
		return
	}
	if ts <= 0 {
		ts = nowMillis()
	}
	r.enqueue(&RecEvent{TS: ts, Origin: origin, Type: typ, Data: raw})
}

// AppendNet queues one network.jsonl line; the writer assigns the sidecar's
// own seq. Metadata only — callers must never pass bodies, cookies, or
// header values. Same never-block, drop-when-paused rules as Append.
func (r *Recorder) AppendNet(fields map[string]any) {
	r.mu.Lock()
	ok := r.meta.Status == recStatusRecording
	r.mu.Unlock()
	if !ok || fields == nil {
		return
	}
	select {
	case r.ch <- recMsg{net: fields}:
	default:
		r.mu.Lock()
		r.meta.Truncated = true
		r.mu.Unlock()
	}
}

// Done closes when the writer goroutine exits — the signal capture sources
// use to stop feeding (stop, interrupt, crash all end here).
func (r *Recorder) Done() <-chan struct{} { return r.done }

// DownloadsDir is where captured downloads land while recording.
func (r *Recorder) DownloadsDir() string { return filepath.Join(r.dir, "artifacts", "downloads") }

// Meta returns the recorder's current meta snapshot.
func (r *Recorder) Meta() RecMeta {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.meta
}

// Summary returns summary.txt — only a finalized recording has one, so a live
// recorder reports ErrSummaryUnavailable.
func (r *Recorder) Summary() (string, error) {
	data, err := os.ReadFile(filepath.Join(r.dir, "summary.txt"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrSummaryUnavailable
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Events reads back events with seq > after, optionally filtered by type.
// A flush barrier rides the append queue first so the read sees every event
// queued before this call. next is the seq to pass as after on the next poll;
// done reports end-of-log on a finalized recording.
func (r *Recorder) Events(after int64, limit int, types []string) ([]RecEvent, int64, bool) {
	fc := make(chan error, 1)
	select {
	case r.ch <- recMsg{flush: fc}:
		// FIFO ordering guarantees every earlier append is flushed first.
		select {
		case <-fc:
		case <-r.done:
		}
	case <-r.done:
	}
	evs, next, eof := readRecEvents(filepath.Join(r.dir, "events.jsonl"), after, limit, types)
	status := r.Meta().Status
	return evs, next, eof && recTerminal(status)
}

// Network reads network.jsonl lines with seq > after; the sidecar has its
// own seq space, assigned by the writer goroutine. Same flush barrier and
// pagination contract as Events.
func (r *Recorder) Network(after int64, limit int) ([]json.RawMessage, int64, bool) {
	fc := make(chan error, 1)
	select {
	case r.ch <- recMsg{flush: fc}:
		select {
		case <-fc:
		case <-r.done:
		}
	case <-r.done:
	}
	lines, next, eof := readRecNetwork(filepath.Join(r.dir, "network.jsonl"), after, limit)
	return lines, next, eof && recTerminal(r.Meta().Status)
}

// pause freezes appends; the recording can still be resumed or stopped.
func (r *Recorder) pause() error {
	r.mu.Lock()
	if r.meta.Status != recStatusRecording {
		status := r.meta.Status
		r.mu.Unlock()
		return fmt.Errorf("%w: recording is %s", ErrInvalidAction, status)
	}
	r.meta.Status = recStatusPaused
	err := r.writeMetaLocked()
	r.mu.Unlock()
	r.appendState(recStatusPaused, nil)
	return err
}

// resume reopens appends after a pause.
func (r *Recorder) resume() error {
	r.mu.Lock()
	if r.meta.Status != recStatusPaused {
		status := r.meta.Status
		r.mu.Unlock()
		return fmt.Errorf("%w: recording is %s", ErrInvalidAction, status)
	}
	r.meta.Status = recStatusRecording
	err := r.writeMetaLocked()
	r.mu.Unlock()
	r.appendState(recStatusRecording, map[string]any{"resumed": true})
	return err
}

// stop drains the queue, closes the log, writes summary.txt and marks the
// recording completed. A stop racing an interrupt returns whatever status the
// interrupt settled on — the recording is finalized either way.
func (r *Recorder) stop() (*RecMeta, error) {
	r.mu.Lock()
	switch r.meta.Status {
	case recStatusCompleted, recStatusInterrupted:
		m := r.meta
		r.mu.Unlock()
		return &m, nil
	case recStatusFinalizing:
		fin := r.fin
		r.mu.Unlock()
		<-fin
		m := r.Meta()
		return &m, nil
	}
	r.meta.Status = recStatusFinalizing
	_ = r.writeMetaLocked()
	r.mu.Unlock()
	r.appendState(recStatusFinalizing, nil)
	// The completed marker lands before the close so the log ends on it.
	r.appendState(recStatusCompleted, nil)
	r.shutdown()
	r.mu.Lock()
	r.meta.Status = recStatusCompleted
	r.meta.EndedAt = nowMillis()
	err := r.writeMetaLocked()
	m := r.meta
	r.mu.Unlock()
	if err := buildRecSummary(r.dir, m, r.mask); err != nil {
		r.log.Warn("recording summary not written", "recording", r.id, "error", err)
	}
	close(r.fin)
	return &m, err
}

// interrupt finalizes the files under the interrupted status (session crash,
// close, or a stale live meta found at boot — callers record the reason).
// A stop already in flight wins the race; interrupt then just waits it out.
func (r *Recorder) interrupt(reason string) {
	r.mu.Lock()
	switch r.meta.Status {
	case recStatusRecording, recStatusPaused:
	default:
		if r.meta.Status == recStatusFinalizing {
			fin := r.fin
			r.mu.Unlock()
			<-fin
			return
		}
		r.mu.Unlock()
		return
	}
	r.meta.Status = recStatusInterrupted
	r.meta.InterruptedReason = reason
	r.meta.EndedAt = nowMillis()
	_ = r.writeMetaLocked()
	r.mu.Unlock()
	r.appendState(recStatusInterrupted, map[string]any{"reason": reason})
	r.shutdown()
	// Rewrite meta so lastSeq/counts cover the interrupt marker itself.
	_ = r.writeMeta()
	if err := buildRecSummary(r.dir, r.Meta(), r.mask); err != nil {
		r.log.Warn("recording summary not written", "recording", r.id, "error", err)
	}
	close(r.fin)
}

// shutdown asks the writer to flush + close and waits for it to exit; safe to
// call after the writer already exited.
func (r *Recorder) shutdown() {
	qc := make(chan error, 1)
	select {
	case r.ch <- recMsg{quit: qc}:
		select {
		case <-qc:
		case <-r.done:
		}
	case <-r.done:
	}
}

// abort discards a recorder that was built but never published (a racing
// session close claimed the slot first).
func (r *Recorder) abort() {
	r.shutdown()
	if err := os.RemoveAll(r.dir); err != nil {
		r.log.Warn("unpublished recording dir not removed", "recording", r.id, "error", err)
	}
}

// claimConversation rebinds a recording created while the session was
// unclaimed so conversation-scoped lookups see it after the draft
// materializes.
func (r *Recorder) claimConversation(conversationID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.meta.ConversationID == "" {
		r.meta.ConversationID = conversationID
		_ = r.writeMetaLocked()
	}
}

// writeMetaLocked is writeMeta for callers already holding r.mu.
func (r *Recorder) writeMetaLocked() error {
	data, err := json.Marshal(r.meta)
	if err != nil {
		return err
	}
	if err := writeRecMeta(r.dir, data); err != nil {
		r.log.Warn("recording meta not persisted", "recording", r.id, "error", err)
		return err
	}
	return nil
}

// run is the single writer goroutine: it assigns seq, masks recorded values,
// appends to events.jsonl, and flushes + refreshes meta.json on a ~1s tick.
func (r *Recorder) run() {
	defer close(r.done)
	f, err := os.OpenFile(filepath.Join(r.dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		// Keep draining so flush/quit waiters never hang; events are lost and
		// the recording is flagged truncated.
		r.log.Warn("recording event log not opened", "recording", r.id, "error", err)
		r.mu.Lock()
		r.meta.Truncated = true
		r.mu.Unlock()
	}
	var w *bufio.Writer
	if f != nil {
		w = bufio.NewWriterSize(f, 64<<10)
	}
	// network.jsonl is the sidecar for request metadata — opened lazily so
	// recordings with no traffic never grow an empty file. It has its own
	// seq space; the writer is its only writer, like events.jsonl.
	var nf *os.File
	var nw *bufio.Writer
	var netSeq int64
	ioErrReported := false
	flush := func() {
		for _, bw := range []*bufio.Writer{w, nw} {
			if bw == nil {
				continue
			}
			if err := bw.Flush(); err != nil && !ioErrReported {
				ioErrReported = true
				r.log.Warn("recording flush failed", "recording", r.id, "error", err)
			}
		}
	}
	ticker := time.NewTicker(recFlushInterval)
	defer ticker.Stop()
	dirty := false
	for {
		select {
		case msg := <-r.ch:
			if msg.net != nil {
				if nf == nil {
					var err error
					nf, err = os.OpenFile(filepath.Join(r.dir, "network.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
					if err != nil {
						r.log.Warn("recording network log not opened", "recording", r.id, "error", err)
						r.mu.Lock()
						r.meta.Truncated = true
						r.mu.Unlock()
						nf = nil
					} else {
						nw = bufio.NewWriterSize(nf, 64<<10)
					}
				}
				if nw != nil {
					masked := recTruncateStrings(maskStrings(msg.net, r.mask))
					if m, ok := masked.(map[string]any); ok {
						m["seq"] = netSeq + 1
						line, err := json.Marshal(m)
						switch {
						case err != nil:
							// Unmarshalable fields drop silently, as before.
						case len(line) > recMaxEventData:
							// Over the line budget: drop + flag, same as a
							// full-queue event; the seq stays contiguous.
							r.mu.Lock()
							r.meta.Truncated = true
							r.mu.Unlock()
						default:
							netSeq++
							if _, err := nw.Write(line); err != nil && !ioErrReported {
								ioErrReported = true
								r.log.Warn("recording network write failed", "recording", r.id, "error", err)
							}
							_ = nw.WriteByte('\n')
						}
					}
					dirty = true
				}
				continue
			}
			if msg.ev != nil {
				ev := msg.ev
				// A payload past the read line budget can never be paged
				// back — drop it outright instead of running the mask pass
				// over an absurd-size document.
				oversized := len(ev.Data) > recLineCap
				if !oversized {
					ev.Data = maskRecData(ev.Data, ev.Type, r.mask)
				}
				r.mu.Lock()
				r.meta.LastSeq++
				ev.Seq = r.meta.LastSeq
				r.meta.Counts.Events++
				switch ev.Type {
				case "annotation":
					r.meta.Counts.Annotations++
				case "network_error":
					r.meta.Counts.Errors++
				case "console":
					var d struct {
						Level string `json:"level"`
					}
					if !oversized && json.Unmarshal(ev.Data, &d) == nil && d.Level == "error" {
						r.meta.Counts.Errors++
					}
				}
				if oversized {
					r.meta.Truncated = true
				}
				r.mu.Unlock()
				if w != nil && !oversized {
					line, err := json.Marshal(ev)
					switch {
					case err != nil:
						// Unmarshalable payload drops silently, as before.
					case len(line) > recMaxEventData:
						// Field caps bound each leaf, not the field count —
						// a still-oversized line drops like a full queue did.
						r.mu.Lock()
						r.meta.Truncated = true
						r.mu.Unlock()
					default:
						if _, err := w.Write(line); err != nil && !ioErrReported {
							ioErrReported = true
							r.log.Warn("recording write failed", "recording", r.id, "error", err)
						}
						_ = w.WriteByte('\n')
					}
				}
				dirty = true
				continue
			}
			if msg.flush != nil {
				flush()
				msg.flush <- nil
				continue
			}
			if msg.quit != nil {
				flush()
				if f != nil {
					_ = f.Close()
				}
				if nf != nil {
					_ = nf.Close()
				}
				msg.quit <- nil
				return
			}
		case <-ticker.C:
			flush()
			if dirty {
				_ = r.writeMeta()
				dirty = false
			}
		}
	}
}

// maskRecData applies the session's exact-match secret masking to every string
// leaf in the payload and blanks input values captured on a password field —
// the only two redaction rules Phase A keeps from the contract (no heuristics).
func maskRecData(raw json.RawMessage, typ string, mask func(string) string) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	v = recTruncateStrings(maskStrings(v, mask))
	if typ == "input" {
		if m, ok := v.(map[string]any); ok && isPasswordInput(m) {
			m["value"] = "[password]"
		}
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

func isPasswordInput(m map[string]any) bool {
	if t, _ := m["inputType"].(string); t == "password" {
		return true
	}
	if d, ok := m["descriptor"].(map[string]any); ok {
		if t, _ := d["type"].(string); t == "password" {
			return true
		}
	}
	return false
}

func maskStrings(v any, mask func(string) string) any {
	switch t := v.(type) {
	case string:
		return mask(t)
	case []any:
		for i := range t {
			t[i] = maskStrings(t[i], mask)
		}
	case map[string]any:
		for k := range t {
			t[k] = maskStrings(t[k], mask)
		}
	}
	return v
}

func recTerminal(status string) bool {
	return status == recStatusCompleted || status == recStatusInterrupted
}

// ---------- Session methods ----------

// Recording returns the session's live recorder, nil when idle.
func (s *Session) Recording() *Recorder {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recorder
}

// StartRecording opens a fresh recording under the manager's recordings dir.
func (s *Session) StartRecording(env string) (*RecMeta, error) {
	if s.m.recordingsDir == "" {
		return nil, fmt.Errorf("%w: recording storage is not configured", ErrUnavailable)
	}
	s.mu.Lock()
	err := s.aliveLocked()
	if err == nil && s.recorder != nil {
		err = ErrRecordingActive
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	r, err := newRecorder(s.m.recordingsDir, s.id, s.ConversationID(), s.Profile(), env, s.MaskSecrets, s.m.log)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if err := s.aliveLocked(); err != nil || s.recorder != nil {
		s.mu.Unlock()
		r.abort()
		if err != nil {
			return nil, err
		}
		return nil, ErrRecordingActive
	}
	s.recorder = r
	s.mu.Unlock()
	// Arm the capture sources on the current tab (binding watcher, drain
	// tick, Network domain, downloads) — all best-effort, all torn down via
	// r.Done().
	s.startRecordingCapture(r)
	meta := r.Meta()
	return &meta, nil
}

// NoteViewport records the session's current device metrics as a viewport
// event — origin "browser" for agent-driven changes, "system" for
// pane-driven ones (WS resize/emulate). No-op while not recording.
func (s *Session) NoteViewport(origin string) {
	r := s.Recording()
	if r == nil {
		return
	}
	s.mu.Lock()
	d := s.device
	s.mu.Unlock()
	r.Append(origin, "viewport", map[string]any{
		"width": d.Width, "height": d.Height, "dpr": d.DPR, "mobile": d.Mobile,
	})
}

// noteRecState records a session-scoped state marker on the live recording —
// input-lease changes are session events, not recording lifecycle
// transitions, so they flow through the gated Append like every other event.
func (s *Session) noteRecState(state string, extra map[string]any) {
	r := s.Recording()
	if r == nil {
		return
	}
	data := map[string]any{"state": state}
	maps.Copy(data, extra)
	r.Append("system", "state", data)
}

func (s *Session) PauseRecording() error {
	r := s.Recording()
	if r == nil {
		return ErrNoActiveRecording
	}
	return r.pause()
}

func (s *Session) ResumeRecording() error {
	r := s.Recording()
	if r == nil {
		return ErrNoActiveRecording
	}
	return r.resume()
}

// StopRecording finalizes the recording (summary.txt included) and detaches it
// from the session; the files stay readable through Manager.Recording.
func (s *Session) StopRecording() (*RecMeta, error) {
	r := s.Recording()
	if r == nil {
		return nil, ErrNoActiveRecording
	}
	meta, err := r.stop()
	if meta != nil {
		s.mu.Lock()
		if s.recorder == r {
			s.recorder = nil
		}
		s.mu.Unlock()
	}
	return meta, err
}
