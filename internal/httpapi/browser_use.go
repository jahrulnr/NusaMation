package httpapi

import (
	"encoding/base64"
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
	case "move":
		x, y, _ := in.point()
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
		data := map[string]any{"sessionId": view.ID, "url": view.URL}
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
}

func knownBrowserUseTool(name string) bool {
	switch name {
	case "navigate", "snapshot", "move", "click", "type", "press", "scroll":
		return true
	default:
		return false
	}
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
