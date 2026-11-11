package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
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
	case errors.Is(err, browser.ErrNotActionable):
		writeError(w, http.StatusConflict, "not_actionable", err.Error())
	case errors.Is(err, browser.ErrInvalidAction):
		writeError(w, http.StatusBadRequest, "invalid_action", err.Error())
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

// normalizeBrowserURL resolves an address-bar string into a navigable URL.
// implicit=true means the scheme was assumed (https) and an http retry is
// allowed when the connection fails; anything that does not look like a URL
// becomes a Google search, matching how browser omniboxes behave.
func normalizeBrowserURL(raw string) (target string, implicit bool, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "about:blank", false, true
	}
	if !hasScheme(raw) {
		if looksLikeSearch(raw) {
			return "https://www.google.com/search?q=" + url.QueryEscape(raw), false, true
		}
		raw = "https://" + raw
		implicit = true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false, false
	}
	switch u.Scheme {
	case "http", "https":
		return raw, implicit, u.Host != ""
	case "about", "data":
		return raw, false, u.Opaque != ""
	case "file":
		return raw, false, u.Path != ""
	}
	return "https://www.google.com/search?q=" + url.QueryEscape(raw), false, true
}

// looksLikeSearch reports whether scheme-less input is a search term rather
// than a host: localhost/IPs/hosts-with-a-dot navigate, the rest searches.
func looksLikeSearch(raw string) bool {
	if strings.ContainsAny(raw, " \t") {
		return true
	}
	host := raw
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	hasPort := false
	if i := strings.LastIndex(host, ":"); i >= 0 {
		hasPort = host[i+1:] != "" && strings.TrimLeft(host[i+1:], "0123456789") == ""
		host = host[:i] // port suffix
	}
	if hasPort || strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil {
		return false
	}
	return !strings.Contains(host, ".")
}

var fallbackNavReasons = []string{
	"ERR_CONNECTION_REFUSED", "ERR_SSL_", "ERR_CONNECTION_RESET",
	"ERR_CONNECTION_ABORTED", "ERR_CONNECTION_CLOSED", "ERR_EMPTY_RESPONSE",
	"ERR_ADDRESS_UNREACHABLE", "ERR_TIMED_OUT",
}

// navigateWithSchemeFallback retries an implicit https:// target over http://
// when the first attempt dies on a transport error — the classic "site only
// serves plain http" case a typed scheme would have avoided.
func (s *Server) navigateWithSchemeFallback(ctx context.Context, sess *browser.Session, target string, implicit bool) (*browser.NavResult, error) {
	res, err := sess.Navigate(ctx, target)
	var navErr *browser.NavigateError
	if !implicit || !strings.HasPrefix(target, "https://") || !errors.As(err, &navErr) {
		return res, err
	}
	retryable := false
	for _, p := range fallbackNavReasons {
		if strings.Contains(navErr.Reason, p) {
			retryable = true
			break
		}
	}
	if !retryable {
		return res, err
	}
	return sess.Navigate(ctx, "http://"+strings.TrimPrefix(target, "https://"))
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
	// "host:port" looks scheme-shaped to a parser (localhost:8787, nas:5000)
	// but is really a scheme-less address — a real scheme's rest is never a
	// bare port, so a digits-only tail un-declares the scheme.
	digits := false
	for k := i + 1; k < len(s); k++ {
		c := s[k]
		if c == '/' || c == '?' || c == '#' {
			break
		}
		if c < '0' || c > '9' {
			return true
		}
		digits = true
	}
	return !digits
}

func (s *Server) handleBrowserSessionCreate(w http.ResponseWriter, r *http.Request) {
	if s.browser == nil {
		writeError(w, http.StatusServiceUnavailable, "browser_unavailable", "The embedded browser is not enabled.")
		return
	}
	var in struct {
		URL            string `json:"url"`
		ConversationID string `json:"conversationId"`
		Profile        string `json:"profile"`
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
	target, _, ok := normalizeBrowserURL(in.URL)
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
	sess, err := s.browser.CreateSessionProfile(r.Context(), in.ConversationID, target,
		clamp(in.Width, 1280), clamp(in.Height, 800), in.Profile)
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
	target, implicit, ok := normalizeBrowserURL(in.URL)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid URL.")
		return
	}
	res, err := s.navigateWithSchemeFallback(r.Context(), sess, target, implicit)
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

// handleBrowserHistoryQuery serves address-bar suggestions for a profile:
// ?q= filters by url/title, empty q returns the most recent visits.
func (s *Server) handleBrowserHistoryQuery(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}})
		return
	}
	profile := browser.NormalizeProfile(r.URL.Query().Get("profile"))
	limit := 8
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	entries, err := s.store.SearchBrowserHistory(r.Context(), profile, r.URL.Query().Get("q"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "history_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) handleBrowserProfiles(w http.ResponseWriter, r *http.Request) {
	if s.browser == nil {
		writeError(w, http.StatusServiceUnavailable, "browser_unavailable", "The embedded browser is not enabled.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": s.browser.Profiles()})
}

// handleBrowserProfile migrates a session to another browser profile. The tab
// is recreated in that profile's Chromium and reloads the current URL.
func (s *Server) handleBrowserProfile(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	var in struct {
		Profile string `json:"profile"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid profile payload.")
		return
	}
	if strings.TrimSpace(in.Profile) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "A profile name is required.")
		return
	}
	if err := s.browser.MigrateProfile(r.Context(), sess, in.Profile); err != nil {
		s.browserError(w, err, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess.View(true)})
}

// handleBrowserConsole drains the page's console-tap buffer for the pane's
// developer tools drawer.
func (s *Server) handleBrowserConsole(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	raw, err := sess.Eval(r.Context(), `window.__nusaConsole || []`)
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"entries":` + string(raw) + `}`))
}

// handleBrowserEval runs an expression on the pane page for the developer
// tools drawer's console prompt. Same surface the browser_evaluate MCP tool
// exposes to agents.
func (s *Server) handleBrowserEval(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	var in struct {
		Expression string `json:"expression"`
	}
	if err := readJSON(w, r, &in, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid eval payload.")
		return
	}
	if strings.TrimSpace(in.Expression) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "An expression is required.")
		return
	}
	val, err := sess.Eval(r.Context(), in.Expression)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true,"result":` + string(val) + `}`))
}

// handleBrowserLoginOffer resolves the current submitted-login offer.
func (s *Server) handleBrowserLoginOffer(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	var in struct {
		ID       string  `json:"id"`
		Save     bool    `json:"save"`
		Username *string `json:"username"`
		Password *string `json:"password"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil || in.ID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "An offer id is required.")
		return
	}
	if err := sess.DecideLoginOfferEdited(r.Context(), in.ID, in.Save, in.Username, in.Password); err != nil {
		s.browserError(w, err, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBrowserLoginOfferDetails(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	username, password, err := sess.LoginOfferDetails(r.URL.Query().Get("id"))
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"username": username, "password": password})
}

func (s *Server) handleBrowserAutofillChoices(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	origin, choices, err := sess.SavedLogins(r.Context())
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"origin": origin, "credentials": choices})
}

// handleBrowserAutofill fills only the account explicitly selected by the user.
func (s *Server) handleBrowserAutofill(w http.ResponseWriter, r *http.Request) {
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	var in struct {
		ID     string `json:"id"`
		Origin string `json:"origin"`
	}
	if err := readJSON(w, r, &in, 4096); err != nil || in.ID == "" || in.Origin == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "A credential id and origin are required.")
		return
	}
	filled, err := sess.FillSavedLogin(r.Context(), in.ID, in.Origin)
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"filled": filled})
}

// handleBrowserCredentials lists a profile's saved logins, passwords masked.
func (s *Server) handleBrowserCredentials(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"credentials": []any{}})
		return
	}
	profile := browser.NormalizeProfile(r.URL.Query().Get("profile"))
	list, err := s.store.ListBrowserCredentials(r.Context(), profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credentials_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": list})
}

func (s *Server) handleBrowserCredentialDelete(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusNotFound, "not_found", "Credential not found.")
		return
	}
	if err := s.store.DeleteBrowserCredential(r.Context(), r.PathValue("credId")); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
