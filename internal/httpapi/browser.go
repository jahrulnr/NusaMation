package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/jahrulnr/nusamation/internal/browser"
)

// browserError maps browser-layer errors onto the contract §11 envelope.
func (s *Server) browserError(w http.ResponseWriter, err error, annotate bool) {
	var navErr *browser.NavigateError
	switch {
	case errors.Is(err, browser.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Browser session not found.")
	case errors.Is(err, browser.ErrBusy):
		writeError(w, http.StatusConflict, "busy", "A navigation or annotation is still in flight; retry after it commits.")
	case errors.Is(err, browser.ErrPickMissed):
		writeError(w, http.StatusUnprocessableEntity, "pick_missed", "No element resolved at that point.")
	case errors.As(err, &navErr):
		writeError(w, http.StatusBadGateway, "navigate_failed", navErr.Reason)
	case errors.Is(err, browser.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "browser_unavailable", err.Error())
	case annotate:
		writeError(w, http.StatusInternalServerError, "annotate_failed", err.Error())
	default:
		writeError(w, http.StatusBadGateway, "navigate_failed", err.Error())
	}
}

func (s *Server) browserSession(w http.ResponseWriter, r *http.Request) *browser.Session {
	if s.browser == nil {
		writeError(w, http.StatusServiceUnavailable, "browser_unavailable", "The embedded browser is not enabled.")
		return nil
	}
	sess, err := s.browser.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Browser session not found.")
		return nil
	}
	return sess
}

func normalizeBrowserURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "about:blank", true
	}
	if !hasScheme(raw) {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch u.Scheme {
	case "http", "https":
		return raw, u.Host != ""
	case "about", "data":
		return raw, u.Opaque != ""
	}
	return "", false
}

// hasScheme reports whether s starts with a URI scheme (ALPHA *(ALPHA/DIGIT/
// "+"/"-"/".") ":") so about:/data:/custom schemes aren't https://-prefixed.
func hasScheme(s string) bool {
	i := strings.IndexByte(s, ':')
	if i < 1 {
		return false
	}
	for j := range i {
		c := s[j]
		alpha := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		rest := j > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.')
		if !alpha && !rest {
			return false
		}
	}
	return true
}

func (s *Server) handleBrowserSessionCreate(w http.ResponseWriter, r *http.Request) {
	if s.browser == nil {
		writeError(w, http.StatusServiceUnavailable, "browser_unavailable", "The embedded browser is not enabled.")
		return
	}
	var in struct {
		URL            string `json:"url"`
		ConversationID string `json:"conversationId"`
		Width          int    `json:"width"`
		Height         int    `json:"height"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid browser session payload.")
		return
	}
	in.ConversationID = strings.TrimSpace(in.ConversationID)
	if in.ConversationID != "" {
		if _, err := s.store.GetConversation(r.Context(), in.ConversationID); err != nil {
			writeError(w, http.StatusBadRequest, "conversation_not_found", "Conversation not found.")
			return
		}
	}
	target, ok := normalizeBrowserURL(in.URL)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid URL.")
		return
	}
	clamp := func(v, def int) int {
		if v == 0 {
			return def
		}
		if v < 200 {
			return 200
		}
		if v > 4096 {
			return 4096
		}
		return v
	}
	sess, err := s.browser.CreateSession(r.Context(), in.ConversationID, target,
		clamp(in.Width, 1280), clamp(in.Height, 800))
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": sess.View(true)})
}

func (s *Server) handleBrowserSessionList(w http.ResponseWriter, r *http.Request) {
	if s.browser == nil {
		writeError(w, http.StatusServiceUnavailable, "browser_unavailable", "The embedded browser is not enabled.")
		return
	}
	sessions := s.browser.List()
	views := make([]browser.View, 0, len(sessions))
	for _, sess := range sessions {
		views = append(views, sess.View(false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": views})
}

func (s *Server) handleBrowserSessionGet(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess.View(true)})
}

func (s *Server) handleBrowserSessionDelete(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	sess.Close()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBrowserNavigate(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	var in struct {
		URL string `json:"url"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid navigate payload.")
		return
	}
	if strings.TrimSpace(in.URL) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "A URL is required.")
		return
	}
	target, ok := normalizeBrowserURL(in.URL)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid URL.")
		return
	}
	res, err := sess.Navigate(r.Context(), target)
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleBrowserHistory(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	var in struct {
		Action string `json:"action"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid history payload.")
		return
	}
	switch in.Action {
	case "back", "forward", "reload":
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown history action.")
		return
	}
	res, err := sess.History(r.Context(), in.Action)
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleBrowserAnnotate(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	var in struct {
		X      *float64 `json:"x"`
		Y      *float64 `json:"y"`
		Marker int      `json:"marker"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid annotate payload.")
		return
	}
	if in.X == nil || in.Y == nil || *in.X < 0 || *in.Y < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Valid x and y coordinates are required.")
		return
	}
	pick, err := sess.Annotate(r.Context(), *in.X, *in.Y, in.Marker)
	if err != nil {
		s.browserError(w, err, true)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pickId": pick.ID, "descriptor": pick.Descriptor, "upload": pick.Upload,
		"marker": pick.Marker,
	})
}

func (s *Server) handleBrowserAnnotatePreview(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	var in struct {
		X *float64 `json:"x"`
		Y *float64 `json:"y"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid annotate payload.")
		return
	}
	if in.X == nil || in.Y == nil || *in.X < 0 || *in.Y < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Valid x and y coordinates are required.")
		return
	}
	pv, err := sess.PreviewAnnotate(*in.X, *in.Y)
	if err != nil {
		s.browserError(w, err, true)
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

func (s *Server) handleBrowserAnnotateClear(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	sess.ClearAnnotate()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
