package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
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
	sess, err := s.browserForConversation(r, convID)
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	var pointer *struct {
		x, y float64
		kind string
	}
	var evalResult json.RawMessage
	var snapshotText, detail string
	switch tool {
	case "navigate":
		target, _ := normalizeBrowserURL(in.URL)
		if _, err := sess.Navigate(r.Context(), target); err != nil {
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
	case "emulate":
		if _, err := sess.ApplyDevicePatch(in.Preset, in.devicePatch()); err != nil {
			if errors.Is(err, browser.ErrUnknownPreset) {
				writeError(w, http.StatusBadRequest, "bad_request", "Unknown preset. Try desktop, mobile, or tablet.")
				return
			}
			s.browserError(w, err, false)
			return
		}
	case "select":
		out, err := sess.SelectRef(r.Context(), in.Ref, in.Values)
		if err != nil {
			s.browserToolError(w, err)
			return
		}
		detail = out
	case "fill":
		out, err := sess.FillFields(r.Context(), in.Fields)
		if err != nil {
			s.browserToolError(w, err)
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
		if err := sess.InsertText(in.Text); err != nil {
			s.browserError(w, err, false)
			return
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
	writeJSON(w, http.StatusOK, out)
}

type browserUseRequest struct {
	Tool        string               `json:"tool"`
	URL         string               `json:"url"`
	X           *float64             `json:"x"`
	Y           *float64             `json:"y"`
	Button      string               `json:"button"`
	ClickCount  int64                `json:"clickCount"`
	DoubleClick bool                 `json:"doubleClick"`
	Text        string               `json:"text"`
	Key         string               `json:"key"`
	DeltaX      float64              `json:"deltaX"`
	DeltaY      float64              `json:"deltaY"`
	Preset      string               `json:"preset"`
	Expression  string               `json:"expression"`
	Function    string               `json:"function"`
	Ref         string               `json:"ref"`
	Element     string               `json:"element"` // schema parity with Playwright; descriptive only
	Values      []string             `json:"values"`
	Fields      []browser.FieldValue `json:"fields"`
	Time        float64              `json:"time"`
	TextGone    string               `json:"textGone"`
	Submit      bool                 `json:"submit"`
	Width       *int64               `json:"width"`
	Height      *int64               `json:"height"`
	DPR         *float64             `json:"dpr"`
	Mobile      *bool                `json:"mobile"`
	UA          *string              `json:"ua"`
	Touch       *bool                `json:"touch"`
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
	default:
		return name
	}
}

func knownBrowserUseTool(name string) bool {
	switch name {
	case "navigate", "navigate_back", "navigate_forward", "snapshot", "tree",
		"console", "wait", "move", "hover", "click", "type", "press", "scroll",
		"emulate", "eval", "select", "fill":
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
		if _, ok := normalizeBrowserURL(in.URL); !ok {
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
	return s.browser.CreateSession(r.Context(), conversationID, "about:blank", 1280, 800)
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
