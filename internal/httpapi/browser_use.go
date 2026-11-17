package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/browser"
	"github.com/jahrulnr/nusamation/internal/events"
)

// handleBrowserUse is the loopback callback for the browser_use MCP process.
// The bearer token selects the conversation; the body cannot retarget it.
func (s *Server) handleBrowserUse(w http.ResponseWriter, r *http.Request) {
	convID, ok := s.browserUseConversation(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Unknown browser_use token.")
		return
	}
	var in browserUseRequest
	if err := readJSONMode(w, r, &in, 1<<20, false); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid browser_use request.")
		return
	}
	tool := normalizeBrowserUseTool(in.Tool)
	if !knownBrowserUseTool(tool) {
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown browser_use tool.")
		return
	}
	if msg := in.invalid(tool); msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	if s.browser == nil {
		writeError(w, http.StatusServiceUnavailable, "browser_unavailable", "The embedded browser is not enabled.")
		return
	}
	// Recording read tools answer from the recordings dir — they never launch
	// or resurrect a session; the live session only supplies the active
	// recording and the secret mask when it exists. They get no timeline
	// tool event: an agent polling the recording must not write into it.
	if recordingReadTool(tool) {
		sess := s.browser.ConversationSession(convID)
		mw := newSecretMaskWriter(w, sess)
		w = mw
		defer mw.flush()
		s.handleRecordingRead(w, convID, sess, tool, in)
		return
	}
	sess, err := s.browserForConversation(r, convID)
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	// Everything written past this point may carry registered secrets; buffer
	// and mask the JSON before it reaches the agent.
	mw := newSecretMaskWriter(w, sess)
	w = mw
	defer mw.flush()
	// Every dispatched tool lands on the recording timeline as an agent/tool
	// intent event — success and failure alike (registered before the switch
	// so early error returns are covered). Args marshal once, only while
	// recording; the writer re-masks every string, so raw args are safe to
	// pass. mw.buf still holds the buffered error body when this runs.
	defer appendBrowserToolEvent(sess, in.Tool, tool, in, mw)
	var pointer *struct {
		x, y float64
		kind string
	}
	var evalResult json.RawMessage
	var snapshotText, detail string
	switch tool {
	case "navigate":
		target, implicit, _ := normalizeBrowserURL(in.URL)
		if _, err := s.navigateWithSchemeFallback(r.Context(), sess, target, implicit); err != nil {
			s.browserError(w, err, false)
			return
		}
	case "navigate_back", "navigate_forward":
		action := strings.TrimPrefix(tool, "navigate_")
		if _, err := sess.History(r.Context(), action); err != nil {
			s.browserError(w, err, false)
			return
		}
	case "snapshot":
	case "tree":
		text, err := sess.Snapshot(r.Context())
		if err != nil {
			s.browserError(w, err, false)
			return
		}
		snapshotText = text
	case "console":
		raw, err := sess.Eval(r.Context(), `window.__nusaConsole || []`)
		if err != nil {
			s.browserError(w, err, false)
			return
		}
		evalResult = raw
	case "eval":
		expr := strings.TrimSpace(in.Expression)
		if expr == "" {
			expr = "(" + strings.TrimSpace(in.Function) + ")()"
		}
		val, err := sess.Eval(r.Context(), expr)
		if err != nil {
			s.browserToolError(w, err)
			return
		}
		evalResult = val
	case "wait":
		if err := browserWait(r, sess, in); err != nil {
			writeError(w, http.StatusRequestTimeout, "wait_timeout", err.Error())
			return
		}
	case "vault_fill":
		filled, err := sess.AutofillForUsername(r.Context(), in.Username)
		if err != nil {
			s.browserError(w, err, false)
			return
		}
		if filled {
			detail = "Saved login filled."
		} else {
			detail = "No saved login was filled for this page and profile."
		}
	case "profile":
		if err := s.browser.MigrateProfile(r.Context(), sess, in.Profile); err != nil {
			s.browserError(w, err, false)
			return
		}
	case "lock":
		sess.SetAgentLock(*in.Mode)
		if *in.Mode {
			detail = "input locked — pane viewers are muted until a human takes over"
		} else {
			detail = "input unlocked"
		}
	case "route":
		state, err := sess.AddRoute(browser.RouteRule{
			Pattern:      in.Pattern,
			Regex:        in.Regex,
			Method:       in.Method,
			ResourceType: in.ResourceType,
			Action:       in.RouteAction,
			Status:       in.RouteStatus,
			Headers:      in.Headers,
			Body:         in.Body,
			BodyBase64:   in.BodyBase64,
			DelayMS:      in.DelayMS,
			TargetURL:    in.TargetURL,
			ErrorReason:  in.ErrorReason,
			Times:        in.Times,
		})
		if err != nil {
			if errors.Is(err, browser.ErrRouteInvalid) {
				writeError(w, http.StatusBadRequest, "invalid_route", err.Error())
				return
			}
			s.browserError(w, err, false)
			return
		}
		raw, _ := json.Marshal(map[string]any{"rule": state})
		evalResult = raw
	case "route_list":
		raw, _ := json.Marshal(map[string]any{
			"rules":          sess.Routes(),
			"webSocketRules": sess.WSRoutes(),
			"initScripts":    sess.InitScripts(),
		})
		evalResult = raw
	case "route_clear":
		cleared := sess.ClearRoutes(in.ID)
		raw, _ := json.Marshal(map[string]any{"cleared": cleared, "rules": sess.Routes()})
		evalResult = raw
	case "network_log":
		entries, next, err := browserNetworkLog(sess.NetLog(), in)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_pattern", err.Error())
			return
		}
		raw, _ := json.Marshal(map[string]any{"entries": entries, "next": next})
		evalResult = raw
	case "add_init_script":
		script, err := sess.AddInitScript(r.Context(), in.Source, in.ID, in.RunImmediately)
		if err != nil {
			if errors.Is(err, browser.ErrRouteInvalid) {
				writeError(w, http.StatusBadRequest, "invalid_init_script", err.Error())
				return
			}
			s.browserError(w, err, false)
			return
		}
		raw, _ := json.Marshal(map[string]any{"script": script})
		evalResult = raw
	case "remove_init_script":
		removed := sess.RemoveInitScript(in.ID)
		raw, _ := json.Marshal(map[string]any{
			"removed": removed,
			"scripts": sess.InitScripts(),
		})
		evalResult = raw
	case "route_ws":
		reload := in.Reload == nil || *in.Reload
		state, err := sess.AddWSRoute(r.Context(), browser.WSRouteRule{
			Pattern: in.Pattern,
			Target:  in.Target,
		}, reload)
		if err != nil {
			if errors.Is(err, browser.ErrRouteInvalid) {
				writeError(w, http.StatusBadRequest, "invalid_route", err.Error())
				return
			}
			s.browserError(w, err, false)
			return
		}
		raw, _ := json.Marshal(map[string]any{
			"route":          state,
			"reload":         reload,
			"webSocketRules": sess.WSRoutes(),
		})
		evalResult = raw
	case "route_ws_clear":
		reload := in.Reload == nil || *in.Reload
		cleared, err := sess.ClearWSRoutes(r.Context(), reload)
		if err != nil {
			s.browserError(w, err, false)
			return
		}
		raw, _ := json.Marshal(map[string]any{"cleared": cleared, "reload": reload})
		evalResult = raw
	case "wait_response":
		entry, err := browserWaitResponse(r.Context(), sess, in)
		if err != nil {
			var timeout *waitResponseTimeout
			switch {
			case errors.As(err, &timeout):
				writeError(w, http.StatusRequestTimeout, "wait_timeout", err.Error())
			case errors.Is(err, browser.ErrRouteInvalid):
				writeError(w, http.StatusBadRequest, "invalid_pattern", err.Error())
			default:
				s.browserError(w, err, false)
			}
			return
		}
		raw, _ := json.Marshal(entry)
		evalResult = raw
	case "emulate":
		if _, err := sess.ApplyDevicePatch(in.Preset, in.devicePatch()); err != nil {
			if errors.Is(err, browser.ErrUnknownPreset) {
				writeError(w, http.StatusBadRequest, "bad_request", "Unknown preset. Try desktop, mobile, or tablet.")
				return
			}
			s.browserError(w, err, false)
			return
		}
		sess.NoteViewport("browser")
	case "select":
		out, err := sess.SelectRef(r.Context(), in.Ref, in.Values)
		if err != nil {
			s.browserToolError(w, err)
			return
		}
		detail = out
	case "fill":
		out, err := s.fillBrowserFields(r, convID, sess, in.Fields)
		if err != nil {
			switch {
			case errors.Is(err, errSecretTarget):
				writeError(w, http.StatusBadRequest, "invalid_secret_target", "Secret references can only fill text fields.")
			case errors.Is(err, errUnknownSecret), errors.Is(err, errInvalidSecret), errors.Is(err, errSecretUnavailable):
				secretError(w, err)
			default:
				s.browserToolError(w, err)
			}
			return
		}
		detail = out
	case "move":
		x, y, _ := in.point()
		x, y = in.resolvePoint(sess.View(false), x, y)
		if err := sess.MouseMove(x, y); err != nil {
			s.browserError(w, err, false)
			return
		}
		pointer = &struct {
			x, y float64
			kind string
		}{x, y, "move"}
	case "hover":
		x, y, err := sess.RefPoint(r.Context(), in.Ref)
		if err != nil {
			s.browserToolError(w, err)
			return
		}
		if err := sess.MouseMove(x, y); err != nil {
			s.browserError(w, err, false)
			return
		}
		pointer = &struct {
			x, y float64
			kind string
		}{x, y, "move"}
	case "click":
		var x, y float64
		if in.Ref != "" {
			var err error
			x, y, err = sess.RefPoint(r.Context(), in.Ref)
			if err != nil {
				s.browserToolError(w, err)
				return
			}
		} else {
			x, y, _ = in.point()
			x, y = in.resolvePoint(sess.View(false), x, y)
		}
		count := in.ClickCount
		if in.DoubleClick && count <= 1 {
			count = 2
		}
		if err := sess.Click(x, y, in.Button, count); err != nil {
			s.browserError(w, err, false)
			return
		}
		pointer = &struct {
			x, y float64
			kind string
		}{x, y, "click"}
	case "type":
		resolved, bindings, rerr := s.resolveSecretInput(r, convID, in.Text)
		sess.RegisterSecrets(bindings)
		if rerr != nil {
			secretError(w, rerr)
			return
		}
		if len(bindings) > 0 {
			if err := sess.InsertSecretText(r.Context(), in.Ref, resolved, false); err != nil {
				s.browserToolError(w, err)
				return
			}
		} else {
			if in.Ref != "" {
				focused, err := sess.FocusRef(r.Context(), in.Ref)
				if err != nil {
					s.browserToolError(w, err)
					return
				}
				if !focused {
					x, y, perr := sess.RefPoint(r.Context(), in.Ref)
					if perr != nil {
						s.browserToolError(w, perr)
						return
					}
					if cerr := sess.Click(x, y, "left", 1); cerr != nil {
						s.browserError(w, cerr, false)
						return
					}
					pointer = &struct {
						x, y float64
						kind string
					}{x, y, "click"}
				}
			}
			if err := sess.InsertText(resolved); err != nil {
				s.browserError(w, err, false)
				return
			}
		}
		if in.Submit {
			if err := sess.Key("Enter", 0); err != nil {
				s.browserError(w, err, false)
				return
			}
		}
	case "press":
		if err := sess.Key(in.Key, 0); err != nil {
			s.browserError(w, err, false)
			return
		}
	case "scroll":
		x, y, ok := in.point()
		if !ok {
			view := sess.View(false)
			x, y = float64(view.Width)/2, float64(view.Height)/2
		} else {
			x, y = in.resolvePoint(sess.View(false), x, y)
		}
		if err := sess.Wheel(x, y, in.DeltaX, in.DeltaY); err != nil {
			s.browserError(w, err, false)
			return
		}
		pointer = &struct {
			x, y float64
			kind string
		}{x, y, "scroll"}
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown browser_use tool.")
		return
	}
	if pointer != nil {
		sess.ShowPointer(pointer.x, pointer.y, pointer.kind)
	}
	view := sess.View(true)
	s.noteBrowserDriven(convID, view.URL)
	if s.hub != nil {
		data := map[string]any{"sessionId": view.ID, "url": view.URL, "tool": in.Tool, "op": tool}
		if pointer != nil {
			data["x"] = pointer.x
			data["y"] = pointer.y
			data["kind"] = pointer.kind
		}
		s.hub.Broadcast(events.New("browser.drive", convID, data))
	}
	out := map[string]any{
		"sessionId": view.ID,
		"url":       view.URL,
		"title":     view.Title,
		"width":     view.Width,
		"height":    view.Height,
	}
	if tool == "snapshot" || tool == "navigate" {
		if jpeg, err := sess.JPEG(0); err == nil && len(jpeg) > 0 {
			out["image"] = base64.StdEncoding.EncodeToString(jpeg)
			out["mimeType"] = "image/jpeg"
		}
	}
	if evalResult != nil {
		out["result"] = evalResult
	}
	if snapshotText != "" {
		out["snapshot"] = snapshotText
	}
	if detail != "" {
		out["detail"] = detail
	}
	if obs := sess.Observations(); len(obs) > 0 {
		out["observations"] = obs
	}
	writeJSON(w, http.StatusOK, out)
}

type browserUseRequest struct {
	Tool         string               `json:"tool"`
	URL          string               `json:"url"`
	X            *float64             `json:"x"`
	Y            *float64             `json:"y"`
	Button       string               `json:"button"`
	ClickCount   int64                `json:"clickCount"`
	DoubleClick  bool                 `json:"doubleClick"`
	Text         string               `json:"text"`
	Key          string               `json:"key"`
	DeltaX       float64              `json:"deltaX"`
	DeltaY       float64              `json:"deltaY"`
	Preset       string               `json:"preset"`
	Profile      string               `json:"profile"`
	Username     *string              `json:"username"`
	Expression   string               `json:"expression"`
	Function     string               `json:"function"`
	Ref          string               `json:"ref"`
	Element      string               `json:"element"` // schema parity with Playwright; descriptive only
	Values       []string             `json:"values"`
	Fields       []browser.FieldValue `json:"fields"`
	AfterSeq     int64                `json:"after_seq"`
	Limit        int                  `json:"limit"`
	Types        []string             `json:"types"`
	AnnotationID string               `json:"annotation_id"`
	Name         string               `json:"name"`
	Time         float64              `json:"time"`
	TextGone     string               `json:"textGone"`
	Submit       bool                 `json:"submit"`
	Mode         *bool                `json:"mode"`
	Width        *int64               `json:"width"`
	Height       *int64               `json:"height"`
	DPR          *float64             `json:"dpr"`
	Mobile       *bool                `json:"mobile"`
	UA           *string              `json:"ua"`
	Touch        *bool                `json:"touch"`

	// Route and network tools. Pattern is the URL matcher for route,
	// network_log, and wait_response; RouteAction is the rule's disposition.
	Pattern        string            `json:"pattern"`
	Method         string            `json:"method"`
	Regex          bool              `json:"regex"`
	ResourceType   string            `json:"resourceType"`
	RouteAction    string            `json:"action"`
	RouteStatus    int64             `json:"status"`
	Headers        map[string]string `json:"headers"`
	Body           string            `json:"body"`
	BodyBase64     bool              `json:"bodyBase64"`
	DelayMS        int               `json:"delayMs"`
	TargetURL      string            `json:"targetUrl"`
	Target         string            `json:"target"`
	Source         string            `json:"source"`
	RunImmediately bool              `json:"runImmediately"`
	Reload         *bool             `json:"reload"`
	ErrorReason    string            `json:"errorReason"`
	Times          int               `json:"times"`
	ID             string            `json:"id"`
	Errors         bool              `json:"errors"`
	After          int64             `json:"after"`
	Timeout        float64           `json:"timeout"`
}

// normalizeBrowserUseTool maps Playwright-flavoured tool names onto the
// internal operations; the short legacy names pass through unchanged.
func normalizeBrowserUseTool(name string) string {
	switch name {
	case "browser_navigate":
		return "navigate"
	case "browser_navigate_back":
		return "navigate_back"
	case "browser_navigate_forward":
		return "navigate_forward"
	case "browser_snapshot":
		return "tree"
	case "browser_take_screenshot":
		return "snapshot"
	case "browser_click":
		return "click"
	case "browser_mouse_click_xy":
		return "click"
	case "browser_mouse_move_xy":
		return "move"
	case "browser_hover":
		return "hover"
	case "browser_type":
		return "type"
	case "browser_press_key":
		return "press"
	case "browser_wait_for":
		return "wait"
	case "browser_vault_fill":
		return "vault_fill"
	case "browser_profile":
		return "profile"
	case "browser_evaluate":
		return "eval"
	case "browser_console_messages":
		return "console"
	case "browser_select_option":
		return "select"
	case "browser_fill_form":
		return "fill"
	case "browser_resize":
		return "emulate"
	case "browser_scroll":
		return "scroll"
	case "browser_emulate":
		return "emulate"
	case "browser_lock":
		return "lock"
	case "browser_route":
		return "route"
	case "browser_route_list":
		return "route_list"
	case "browser_route_clear":
		return "route_clear"
	case "browser_network_log":
		return "network_log"
	case "browser_wait_for_response":
		return "wait_response"
	case "browser_add_init_script":
		return "add_init_script"
	case "browser_remove_init_script":
		return "remove_init_script"
	case "browser_route_ws":
		return "route_ws"
	case "browser_route_ws_clear":
		return "route_ws_clear"
	case "browser_get_recording_status":
		return "recording_status"
	case "browser_get_recording_summary":
		return "recording_summary"
	case "browser_get_timeline":
		return "timeline"
	case "browser_get_annotation":
		return "annotation"
	case "browser_get_artifact":
		return "artifact"
	default:
		return name
	}
}

func knownBrowserUseTool(name string) bool {
	switch name {
	case "navigate", "navigate_back", "navigate_forward", "snapshot", "tree",
		"console", "wait", "move", "hover", "click", "type", "press", "scroll",
		"emulate", "eval", "select", "fill", "lock", "profile", "vault_fill",
		"route", "route_list", "route_clear", "network_log", "wait_response",
		"add_init_script", "remove_init_script", "route_ws", "route_ws_clear",
		"recording_status", "recording_summary", "timeline", "annotation", "artifact":
		return true
	default:
		return false
	}
}

// recordingReadTool reports whether the op reads recording files — those
// tools resolve the recording server-side and never need a live session.
func recordingReadTool(name string) bool {
	switch name {
	case "recording_status", "recording_summary", "timeline", "annotation", "artifact":
		return true
	default:
		return false
	}
}

// devicePatch is the request's optional device fields; nil keeps the base.
func (in browserUseRequest) devicePatch() browser.DevicePatch {
	return browser.DevicePatch{
		Width: in.Width, Height: in.Height, DPR: in.DPR,
		Mobile: in.Mobile, UserAgent: in.UA, Touch: in.Touch,
	}
}

// resolvePoint maps 0–1 fractions onto the viewport; anything larger stays an
// absolute CSS-px coordinate so older callers keep working.
func (in browserUseRequest) resolvePoint(v browser.View, x, y float64) (float64, float64) {
	if x >= 0 && x <= 1 && y >= 0 && y <= 1 {
		return x * float64(v.Width), y * float64(v.Height)
	}
	return x, y
}

func (in browserUseRequest) invalid(tool string) string {
	switch tool {
	case "navigate":
		if strings.TrimSpace(in.URL) == "" {
			return "A URL is required."
		}
		if _, _, ok := normalizeBrowserURL(in.URL); !ok {
			return "Invalid URL."
		}
	case "emulate":
		if in.Preset == "" && in.Width == nil && in.Height == nil && in.DPR == nil && in.Mobile == nil && in.UA == nil && in.Touch == nil {
			return "preset or device fields are required."
		}
	case "move":
		if _, _, ok := in.point(); !ok {
			return "x and y are required."
		}
	case "click":
		if in.Ref == "" {
			if _, _, ok := in.point(); !ok {
				return "ref or x/y are required."
			}
		}
	case "hover", "select":
		if in.Ref == "" {
			return "ref is required — get one from browser_snapshot."
		}
		if tool == "select" && len(in.Values) == 0 {
			return "values are required."
		}
	case "fill":
		if len(in.Fields) == 0 {
			return "fields are required."
		}
	case "type":
		if in.Text == "" {
			return "text is required."
		}
	case "press":
		if strings.TrimSpace(in.Key) == "" {
			return "key is required."
		}
	case "wait":
		set := 0
		if in.Time > 0 {
			set++
		}
		if in.Text != "" {
			set++
		}
		if in.TextGone != "" {
			set++
		}
		if set != 1 {
			return "exactly one of time, text, or textGone is required."
		}
	case "eval":
		if strings.TrimSpace(in.Expression) == "" && strings.TrimSpace(in.Function) == "" {
			return "expression or function is required."
		}
	case "lock":
		if in.Mode == nil {
			return "mode is required."
		}
	case "route":
		if strings.TrimSpace(in.Pattern) == "" {
			return "pattern is required."
		}
		if strings.TrimSpace(in.RouteAction) == "" {
			return "action is required — fulfill, abort, delay, or continue."
		}
		if in.Times < 0 {
			return "times must be 0 or positive."
		}
	case "add_init_script":
		if strings.TrimSpace(in.Source) == "" {
			return "source is required."
		}
	case "remove_init_script":
		if strings.TrimSpace(in.ID) == "" {
			return "id is required."
		}
	case "route_ws":
		if strings.TrimSpace(in.Pattern) == "" {
			return "pattern is required."
		}
		if strings.TrimSpace(in.Target) == "" {
			return "target is required — an absolute ws:// or wss:// URL."
		}
	case "network_log":
		if in.Limit < 0 || in.Limit > netLogCapLimit {
			return "limit must be 0-300."
		}
		if in.After < 0 {
			return "after must be 0 or positive."
		}
	case "wait_response":
		if in.Timeout < 0 || in.Timeout > 30 {
			return "timeout must be 0-30 seconds."
		}
		if in.After < 0 {
			return "after must be 0 or positive."
		}
	case "annotation":
		if strings.TrimSpace(in.AnnotationID) == "" {
			return "annotation_id is required."
		}
	case "artifact":
		if strings.TrimSpace(in.Name) == "" {
			return "name is required."
		}
	}
	return ""
}

// browserToolError maps tool errors: ref staleness is a 400 client error
// with a re-snapshot hint; session failures (busy, dead tab) go through
// browserError's status table.
func (s *Server) browserToolError(w http.ResponseWriter, err error) {
	if errors.Is(err, browser.ErrRefNotFound) {
		writeError(w, http.StatusBadRequest, "ref_not_found", err.Error())
		return
	}
	s.browserError(w, err, false)
}

// browserWait implements wait: a fixed pause, or polling for text in
// body.innerText until it appears/disappears (10s cap inside WaitForText).
func browserWait(r *http.Request, sess *browser.Session, in browserUseRequest) error {
	if in.Time > 0 {
		d := min(time.Duration(in.Time*float64(time.Second)), 30*time.Second)
		select {
		case <-r.Context().Done():
			return r.Context().Err()
		case <-time.After(d):
			return nil
		}
	}
	text, gone := in.Text, false
	if in.TextGone != "" {
		text, gone = in.TextGone, true
	}
	return sess.WaitForText(r.Context(), text, gone)
}

const (
	// netLogDefaultLimit is one browser_network_log page when the agent does
	// not ask for a size; netLogCapLimit is the whole session ring.
	netLogDefaultLimit = 50
	netLogCapLimit     = 300
	// waitResponsePoll is how often the wait tool re-reads the net ring. The
	// ring is appended under a mutex, so polling is cheap and needs no extra
	// plumbing from the network watchers.
	waitResponsePoll = 50 * time.Millisecond
)

// waitResponseTimeout reports a wait that saw no matching response. It is a
// distinct type because the HTTP bridge answers 408, not 500.
type waitResponseTimeout struct{ msg string }

func (e *waitResponseTimeout) Error() string { return e.msg }

// urlMatcher compiles an optional URL pattern; no pattern matches everything.
func urlMatcher(pattern string, regex bool) (func(string) bool, error) {
	if strings.TrimSpace(pattern) == "" {
		return func(string) bool { return true }, nil
	}
	re, err := browser.GlobURL(pattern, regex)
	if err != nil {
		return nil, err
	}
	return re.MatchString, nil
}

// netEntryMatches applies the shared network_log/wait_response filters.
func netEntryMatches(e browser.NetEntry, in browserUseRequest, match func(string) bool) bool {
	if !match(e.URL) {
		return false
	}
	if in.Method != "" && !strings.EqualFold(in.Method, e.Method) {
		return false
	}
	if in.RouteStatus != 0 && e.Status != in.RouteStatus {
		return false
	}
	if in.Errors && e.Error == "" && e.Status < 400 {
		return false
	}
	return true
}

// browserNetworkLog returns the newest matching entries and the cursor for the
// next page. Entries are oldest-first; a page larger than limit keeps the
// newest end and reports the newest sequence as next.
func browserNetworkLog(entries []browser.NetEntry, in browserUseRequest) ([]browser.NetEntry, int64, error) {
	match, err := urlMatcher(in.Pattern, in.Regex)
	if err != nil {
		return nil, 0, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = netLogDefaultLimit
	}
	limit = min(limit, netLogCapLimit)
	out := filterNetEntries(entries, in, match)
	next := in.After
	if len(out) > 0 {
		next = out[len(out)-1].Seq
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, next, nil
}

// filterNetEntries applies the network filters to a snapshot, oldest first.
func filterNetEntries(entries []browser.NetEntry, in browserUseRequest, match func(string) bool) []browser.NetEntry {
	var out []browser.NetEntry
	for _, e := range entries {
		if e.Seq <= in.After || !netEntryMatches(e, in, match) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// scanNetEntries returns the newest matching entry above the cursor, the new
// cursor (newest sequence seen, matched or not), and how many entries were
// observed. It is the polling step of browserWaitResponse.
func scanNetEntries(entries []browser.NetEntry, cursor int64, in browserUseRequest, match func(string) bool) (browser.NetEntry, int64, int, bool) {
	var found browser.NetEntry
	newest, observed := cursor, 0
	ok := false
	for _, e := range entries {
		if e.Seq <= cursor {
			continue
		}
		newest = e.Seq
		observed++
		if !netEntryMatches(e, in, match) {
			continue
		}
		found, ok = e, true
	}
	return found, newest, observed, ok
}

// browserWaitResponse returns the newest matching response already seen, or
// waits for one to arrive. Scanning history first makes the tool useful right
// after a click, when the response has usually landed before the call.
func browserWaitResponse(ctx context.Context, sess *browser.Session, in browserUseRequest) (browser.NetEntry, error) {
	match, err := urlMatcher(in.Pattern, in.Regex)
	if err != nil {
		return browser.NetEntry{}, err
	}
	timeout := 10 * time.Second
	if in.Timeout > 0 {
		timeout = min(time.Duration(in.Timeout*float64(time.Second)), 30*time.Second)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(waitResponsePoll)
	defer ticker.Stop()
	cursor := in.After
	observed := 0
	for {
		found, newest, seen, ok := scanNetEntries(sess.NetLog(), cursor, in, match)
		cursor = newest
		observed += seen
		if ok {
			return found, nil
		}
		select {
		case <-ctx.Done():
			return browser.NetEntry{}, ctx.Err()
		case <-ticker.C:
		case <-deadline.C:
			label := in.Pattern
			if label == "" {
				label = "**"
			}
			return browser.NetEntry{}, &waitResponseTimeout{msg: fmt.Sprintf(
				"no response matched %q within %s (%d responses observed; pass after=<next> from browser_network_log to skip old ones)",
				label, timeout, observed)}
		}
	}
}

func (in browserUseRequest) point() (float64, float64, bool) {
	if in.X == nil || in.Y == nil {
		return 0, 0, false
	}
	return *in.X, *in.Y, true
}

func (s *Server) browserUseConversation(r *http.Request) (string, bool) {
	if s.browserUse == nil {
		return "", false
	}
	h := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || token == "" || token == h {
		return "", false
	}
	convID, ok := s.browserUse.Conversation(token)
	if !ok {
		return "", false
	}
	// Tokens outlive process restarts; the conversation may not. Reject ids
	// whose row is gone (deleted conversations also hit Revoke, but the file
	// could lag or be edited by hand).
	if s.store != nil {
		if _, err := s.store.GetConversation(r.Context(), convID); err != nil {
			return "", false
		}
	}
	return convID, true
}

func (s *Server) browserForConversation(r *http.Request, conversationID string) (*browser.Session, error) {
	if sess := s.browser.ConversationSession(conversationID); sess != nil {
		return sess, nil
	}
	return s.browser.CreateSession(r.Context(), conversationID, "", 1280, 800)
}

func (s *Server) noteBrowserDriven(id, pageURL string) {
	if pageURL == "" || pageURL == "about:blank" {
		return
	}
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	m, err := s.loadConvMeta(id)
	if err != nil {
		return
	}
	if m.Browser == nil {
		m.Browser = &convMetaBrowser{}
	}
	// Browser.Open belongs to the client's pane open/close: a drive only
	// tracks where the agent is, and must not resurrect a pane the user
	// closed (an explicit open:false in the file).
	m.Browser.LastURL = pageURL
	_ = s.saveConvMeta(id, m)
}

/* ---------------- recording read tools ----------------
   The browser_get_* tools resolve the recording server-side: loopback token
   → conversation → newest relevant recording (the live session's active
   recorder wins, else the newest completed/interrupted meta). The agent
   never sends a recording id and never sees tokens or raw paths. */

const (
	// artifactTextCap bounds text artifacts; artifactReadCap bounds the
	// total read (images can be bigger than text but not unbounded).
	artifactTextCap = 256 << 10
	artifactReadCap = 8 << 20
	// recEventsScanLimit bounds one page of the annotation timeline scan.
	recEventsScanLimit = 1000
)

// recordingForConversation picks the recording a browser_get_* tool sees.
func (s *Server) recordingForConversation(convID string, sess *browser.Session) *browser.Recording {
	if s.browser == nil {
		return nil
	}
	if sess != nil {
		if r := sess.Recording(); r != nil {
			if rec, err := s.browser.Recording(r.Meta().ID); err == nil {
				return rec
			}
		}
	}
	// Recordings merges live sessions' recorders over their ~1s-stale disk
	// snapshots, newest first — the first entry is the newest relevant one.
	for _, meta := range s.browser.Recordings(convID) {
		if rec, err := s.browser.Recording(meta.ID); err == nil {
			return rec
		}
	}
	return nil
}

// handleRecordingRead serves the browser_get_* tools. Data rides the
// apiResult contract: result for JSON, detail for text, image+mimeType for
// binaries — the same shapes browser_console_messages/navigate already use.
func (s *Server) handleRecordingRead(w http.ResponseWriter, convID string, sess *browser.Session, tool string, in browserUseRequest) {
	rec := s.recordingForConversation(convID, sess)
	if rec == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"detail": "No browser recording exists for this conversation."})
		return
	}
	meta := rec.Meta()
	switch tool {
	case "recording_status":
		writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"recording": meta}})
	case "recording_summary":
		summary, err := rec.Summary()
		switch {
		case errors.Is(err, browser.ErrSummaryUnavailable):
			writeJSON(w, http.StatusOK, map[string]any{
				"detail": "Recording " + meta.ID + " is still " + meta.Status +
					" — no summary yet. browser_get_timeline reads the live event stream."})
		case err != nil:
			s.browserError(w, err, false)
		default:
			writeJSON(w, http.StatusOK, map[string]any{"detail": summary})
		}
	case "timeline":
		limit := in.Limit
		if limit <= 0 {
			limit = 200
		}
		events, next, done := rec.Events(in.AfterSeq, limit, in.Types)
		if events == nil {
			events = []browser.RecEvent{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{
			"recordingId": meta.ID, "events": events, "next": next, "done": done,
		}})
	case "annotation":
		s.writeRecordingAnnotation(w, rec, strings.TrimSpace(in.AnnotationID))
	case "artifact":
		s.writeRecordingArtifact(w, rec, strings.TrimSpace(in.Name))
	}
}

// writeRecordingAnnotation finds an annotation event by annotationId/pickId
// and resolves its pick: descriptor + upload ref ride the result object.
func (s *Server) writeRecordingAnnotation(w http.ResponseWriter, rec *browser.Recording, annotationID string) {
	var data map[string]any
	pickID := ""
	// Page the annotation stream until the id matches — annotations are rare
	// but the scan stays bounded either way.
	var after int64
	for pages := 0; pages < 20 && data == nil; pages++ {
		evs, next, done := rec.Events(after, recEventsScanLimit, []string{"annotation"})
		for _, ev := range evs {
			var d map[string]any
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			id, _ := d["annotationId"].(string)
			pk, _ := d["pickId"].(string)
			if id == annotationID || (pk != "" && pk == annotationID) {
				data = d
				if pk != "" {
					pickID = pk
				} else {
					pickID = id
				}
				break
			}
		}
		after = next
		// A short page means the scan reached EOF — done additionally marks
		// a terminal recording fully read (live ones keep done=false).
		if done || len(evs) < recEventsScanLimit {
			break
		}
	}
	if data == nil {
		writeError(w, http.StatusNotFound, "annotation_not_found",
			"No annotation with that id exists in the recording.")
		return
	}
	out := map[string]any{"annotation": data}
	if pickID != "" {
		if pick, ok := s.browser.Pick(pickID); ok {
			out["pick"] = map[string]any{
				"pickId": pick.ID, "marker": pick.Marker,
				"descriptor": pick.Descriptor, "upload": pick.Upload,
			}
		} else {
			// Picks are ephemeral UI state swept after a week — the event
			// data still describes the annotation without them.
			out["pickUnavailable"] = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": out})
}

// writeRecordingArtifact streams one recording artifact back over the
// apiResult contract: images as image content, text/json/jsonl/logs as
// bounded text, anything else an explicit unsupported error.
func (s *Server) writeRecordingArtifact(w http.ResponseWriter, rec *browser.Recording, name string) {
	path, err := rec.ArtifactPath(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "artifact_not_found", "Artifact not found in the recording.")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "artifact_not_found", "Artifact not found in the recording.")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, artifactReadCap+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "artifact_read_failed", "Could not read the artifact.")
		return
	}
	mime := http.DetectContentType(data[:min(512, len(data))])
	switch {
	case strings.HasPrefix(mime, "image/"):
		if len(data) > artifactReadCap {
			writeError(w, http.StatusRequestEntityTooLarge, "artifact_too_large",
				"Image artifact exceeds the tool's 8MB limit.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"image": base64.StdEncoding.EncodeToString(data), "mimeType": mime,
			"detail": "artifact " + name,
		})
	case strings.HasPrefix(mime, "text/"):
		text := string(data)
		if len(data) > artifactTextCap {
			text = string(data[:artifactTextCap]) + "\n… (truncated at 256KB)"
		}
		writeJSON(w, http.StatusOK, map[string]any{"detail": text})
	default:
		writeError(w, http.StatusUnsupportedMediaType, "artifact_unsupported",
			"Artifact is not an image or text file.")
	}
}
