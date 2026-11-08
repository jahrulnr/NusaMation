package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

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
	if err := readJSON(w, r, &in, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid browser_use request.")
		return
	}
	if !knownBrowserUseTool(in.Tool) {
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown browser_use tool.")
		return
	}
	if msg := in.invalid(); msg != "" {
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
	switch in.Tool {
	case "navigate":
		target, _ := normalizeBrowserURL(in.URL)
		if _, err := sess.Navigate(r.Context(), target); err != nil {
			s.browserError(w, err, false)
			return
		}
	case "snapshot":
	case "emulate":
		if _, err := sess.ApplyDevicePatch(in.Preset, in.devicePatch()); err != nil {
			if errors.Is(err, browser.ErrUnknownPreset) {
				writeError(w, http.StatusBadRequest, "bad_request", "Unknown preset. Try desktop, mobile, or tablet.")
				return
			}
			s.browserError(w, err, false)
			return
		}
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
	case "click":
		x, y, _ := in.point()
		x, y = in.resolvePoint(sess.View(false), x, y)
		if err := sess.Click(x, y, in.Button, in.ClickCount); err != nil {
			s.browserError(w, err, false)
			return
		}
		pointer = &struct {
			x, y float64
			kind string
		}{x, y, "click"}
	case "type":
		if err := sess.InsertText(in.Text); err != nil {
			s.browserError(w, err, false)
			return
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
		data := map[string]any{"sessionId": view.ID, "url": view.URL, "tool": in.Tool}
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
	if in.Tool == "snapshot" || in.Tool == "navigate" {
		if jpeg, err := sess.JPEG(0); err == nil && len(jpeg) > 0 {
			out["image"] = base64.StdEncoding.EncodeToString(jpeg)
			out["mimeType"] = "image/jpeg"
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type browserUseRequest struct {
	Tool       string   `json:"tool"`
	URL        string   `json:"url"`
	X          *float64 `json:"x"`
	Y          *float64 `json:"y"`
	Button     string   `json:"button"`
	ClickCount int64    `json:"clickCount"`
	Text       string   `json:"text"`
	Key        string   `json:"key"`
	DeltaX     float64  `json:"deltaX"`
	DeltaY     float64  `json:"deltaY"`
	Preset     string   `json:"preset"`
	Width      *int64   `json:"width"`
	Height     *int64   `json:"height"`
	DPR        *float64 `json:"dpr"`
	Mobile     *bool    `json:"mobile"`
	UA         *string  `json:"ua"`
	Touch      *bool    `json:"touch"`
}

func knownBrowserUseTool(name string) bool {
	switch name {
	case "navigate", "snapshot", "move", "click", "type", "press", "scroll", "emulate":
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

func (in browserUseRequest) invalid() string {
	switch in.Tool {
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
	case "move", "click":
		if _, _, ok := in.point(); !ok {
			return "x and y are required."
		}
	case "type":
		if in.Text == "" {
			return "text is required."
		}
	case "press":
		if strings.TrimSpace(in.Key) == "" {
			return "key is required."
		}
	}
	return ""
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
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	m, err := s.loadConvMeta(id)
	if err != nil {
		return
	}
	if m.Browser == nil {
		m.Browser = &convMetaBrowser{}
	}
	open := true
	m.Browser.Open = &open
	if pageURL != "" && pageURL != "about:blank" {
		m.Browser.LastURL = pageURL
	}
	_ = s.saveConvMeta(id, m)
}
