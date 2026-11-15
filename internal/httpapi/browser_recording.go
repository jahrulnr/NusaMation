package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusamation/internal/browser"
)

// recordingQuery parses the shared events pagination params.
func recordingQuery(r *http.Request) (after int64, limit int, types []string) {
	q := r.URL.Query()
	after = int64(parseInt(q.Get("after"), 0))
	limit = parseInt(q.Get("limit"), 0)
	if t := q.Get("types"); t != "" {
		types = strings.Split(t, ",")
	}
	return after, limit, types
}

func (s *Server) writeRecordingEvents(w http.ResponseWriter, events []browser.RecEvent, next int64, done bool) {
	if events == nil {
		events = []browser.RecEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "next": next, "done": done})
}

// handleRecordingStart opens a recording on the session (contract §HTTP).
func (s *Server) handleRecordingStart(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	var in struct {
		Env string `json:"env"`
	}
	// env is optional — an empty body is a valid start request.
	if err := readJSON(w, r, &in, 64<<10); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid recording payload.")
		return
	}
	meta, err := sess.StartRecording(strings.TrimSpace(in.Env))
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"recording": meta})
}

func (s *Server) handleRecordingPause(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	if err := sess.PauseRecording(); err != nil {
		s.browserError(w, err, false)
		return
	}
	rec := sess.Recording()
	if rec == nil {
		s.browserError(w, browser.ErrNoActiveRecording, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recording": rec.Meta()})
}

func (s *Server) handleRecordingResume(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	if err := sess.ResumeRecording(); err != nil {
		s.browserError(w, err, false)
		return
	}
	rec := sess.Recording()
	if rec == nil {
		s.browserError(w, browser.ErrNoActiveRecording, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recording": rec.Meta()})
}

func (s *Server) handleRecordingStop(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	meta, err := sess.StopRecording()
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recording": meta})
}

// handleRecordingGet returns the session's active recording meta.
func (s *Server) handleRecordingGet(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	rec := sess.Recording()
	if rec == nil {
		s.browserError(w, browser.ErrNoActiveRecording, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recording": rec.Meta()})
}

// handleRecordingEvents reads the session's active recording's timeline.
func (s *Server) handleRecordingEvents(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	rec := sess.Recording()
	if rec == nil {
		s.browserError(w, browser.ErrNoActiveRecording, false)
		return
	}
	after, limit, types := recordingQuery(r)
	events, next, done := rec.Events(after, limit, types)
	s.writeRecordingEvents(w, events, next, done)
}

// handleRecordingSummary returns summary.txt for the session's recording —
// only finalized recordings have one.
func (s *Server) handleRecordingSummary(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	rec := sess.Recording()
	if rec == nil {
		s.browserError(w, browser.ErrNoActiveRecording, false)
		return
	}
	summary, err := rec.Summary()
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": summary})
}

// handleConversationRecordings lists recording metas for a conversation,
// newest first (live recordings merge over their on-disk snapshots).
func (s *Server) handleConversationRecordings(w http.ResponseWriter, r *http.Request) {
	if s.browser == nil {
		writeError(w, http.StatusServiceUnavailable, "browser_unavailable", "The embedded browser is not enabled.")
		return
	}
	convID := r.PathValue("id")
	if s.store != nil {
		if _, err := s.store.GetConversation(r.Context(), convID); err != nil {
			writeError(w, http.StatusBadRequest, "conversation_not_found", "Conversation not found.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"recordings": s.browser.Recordings(convID)})
}

// recordingByID resolves a recording id — works for live and for finalized
// recordings whose session is long gone.
func (s *Server) recordingByID(w http.ResponseWriter, r *http.Request) *browser.Recording {
	if s.browser == nil {
		writeError(w, http.StatusServiceUnavailable, "browser_unavailable", "The embedded browser is not enabled.")
		return nil
	}
	rec, err := s.browser.Recording(r.PathValue("recId"))
	if err != nil {
		s.browserError(w, err, false)
		return nil
	}
	return rec
}

func (s *Server) handleRecordingEventsByID(w http.ResponseWriter, r *http.Request) {
	rec := s.recordingByID(w, r)
	if rec == nil {
		return
	}
	after, limit, types := recordingQuery(r)
	events, next, done := rec.Events(after, limit, types)
	s.writeRecordingEvents(w, events, next, done)
}

func (s *Server) handleRecordingSummaryByID(w http.ResponseWriter, r *http.Request) {
	rec := s.recordingByID(w, r)
	if rec == nil {
		return
	}
	summary, err := rec.Summary()
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": summary})
}

// handleRecordingArtifact serves a file from the recording's artifacts dir;
// the name is validated like a pick id so traversal is impossible.
func (s *Server) handleRecordingArtifact(w http.ResponseWriter, r *http.Request) {
	rec := s.recordingByID(w, r)
	if rec == nil {
		return
	}
	name := r.PathValue("name")
	path, err := rec.ArtifactPath(name)
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	if strings.HasPrefix(name, "downloads/") {
		// Captured downloads are page-controlled content — never serve them
		// inline on the control-center origin.
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
	}
	http.ServeFile(w, r, path)
}

// writeRecordingNetwork emits the sidecar page — same cursor contract as
// writeRecordingEvents over network.jsonl's own seq space.
func (s *Server) writeRecordingNetwork(w http.ResponseWriter, lines []json.RawMessage, next int64, done bool) {
	if lines == nil {
		lines = []json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": lines, "next": next, "done": done})
}

// handleRecordingNetwork reads the session's live network.jsonl.
func (s *Server) handleRecordingNetwork(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	rec := sess.Recording()
	if rec == nil {
		s.browserError(w, browser.ErrNoActiveRecording, false)
		return
	}
	after, limit, _ := recordingQuery(r)
	lines, next, done := rec.Network(after, limit)
	s.writeRecordingNetwork(w, lines, next, done)
}

// handleRecordingNetworkByID reads a stored recording's network.jsonl.
func (s *Server) handleRecordingNetworkByID(w http.ResponseWriter, r *http.Request) {
	rec := s.recordingByID(w, r)
	if rec == nil {
		return
	}
	after, limit, _ := recordingQuery(r)
	lines, next, done := rec.Network(after, limit)
	s.writeRecordingNetwork(w, lines, next, done)
}

// recToolArgsCap bounds the recorded args blob: the writer caps each string
// leaf inside it, but a request carrying many fields could still outgrow a
// timeline line, so an oversized args is noted instead of written.
const recToolArgsCap = 64 << 10

// appendBrowserToolEvent records the finished browser_use dispatch on the
// session's live recording — an "agent" intent event for every tool, success
// or failure. mw's buffered status/body carries the outcome; the writer's
// string masking re-covers anything a registered secret touched. Deferred
// calls read mw after the handler has written, so this runs before
// mw.flush() and the buffer is still intact. The read-only recording tools
// never land here — polling the timeline must not write into it.
func appendBrowserToolEvent(sess *browser.Session, tool, op string, in browserUseRequest, mw *secretMaskWriter) {
	if sess == nil || recordingReadTool(op) {
		return // read tools can also run with no live session for the conversation
	}
	rec := sess.Recording()
	if rec == nil {
		return
	}
	data := map[string]any{"tool": tool, "op": op}
	if args, err := json.Marshal(in); err == nil {
		if len(args) <= recToolArgsCap {
			data["args"] = json.RawMessage(args)
		} else {
			data["argsTruncated"] = true
		}
	}
	if mw.status >= 400 {
		data["ok"] = false
		data["code"] = mw.status
		var body struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.Unmarshal(mw.buf.Bytes(), &body); err == nil {
			if body.Error != "" {
				data["error"] = body.Error
			}
			if body.Code != "" {
				data["code"] = body.Code
			}
		}
	} else {
		data["ok"] = true
	}
	rec.Append("agent", "tool", data)
}
