package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/store"
)

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	initialized := auth.Initialized(r.Context(), s.store)
	resp := map[string]any{
		"initialized":   initialized,
		"authenticated": false,
		"version":       config.Version,
	}
	if ss, ok := s.session(r); ok {
		resp["authenticated"] = true
		resp["csrf"] = ss.CSRF
		resp["expiresAt"] = ss.ExpiresAt
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.limiter.allow(ip) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Wait a few minutes and try again.")
		return
	}
	var in struct {
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Enter your password.")
		return
	}
	if strings.TrimSpace(in.Password) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Enter your password.")
		return
	}
	if !auth.Initialized(r.Context(), s.store) {
		writeError(w, http.StatusServiceUnavailable, "not_initialized", "NusaMation has not finished first-run setup. Restart the app and check the terminal output.")
		return
	}
	if !auth.Verify(r.Context(), s.store, in.Password) {
		s.limiter.fail(ip)
		time.Sleep(250 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "invalid_password", "That password is not correct.")
		return
	}
	s.limiter.reset(ip)
	ttl := 24 * time.Hour
	if in.Remember {
		ttl = 30 * 24 * time.Hour
	}
	token, ss, err := auth.CreateSession(r.Context(), s.store, ip, r.UserAgent(), ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_failed", "Could not create a session.")
		return
	}
	s.setSessionCookie(w, r, token, ttl)
	_ = s.store.DeleteExpiredSessions(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "csrf": ss.CSRF, "expiresAt": ss.ExpiresAt,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		auth.DeleteSession(r.Context(), s.store, c.Value)
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Enter your current and new password.")
		return
	}
	if !auth.Verify(r.Context(), s.store, in.Current) {
		writeError(w, http.StatusForbidden, "invalid_password", "The current password is not correct.")
		return
	}
	if len(strings.TrimSpace(in.New)) < 4 {
		writeError(w, http.StatusBadRequest, "weak_password", "The new password must be at least 4 characters.")
		return
	}
	if err := auth.SetPassword(r.Context(), s.store, in.New); err != nil {
		writeError(w, http.StatusInternalServerError, "password_failed", err.Error())
		return
	}
	_ = s.store.DeleteAllSessions(r.Context())
	token, ss, err := auth.CreateSession(r.Context(), s.store, s.clientIP(r), r.UserAgent(), 30*24*time.Hour)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_failed", "Password changed, but a new session could not be created. Sign in again.")
		return
	}
	s.setSessionCookie(w, r, token, 30*24*time.Hour)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "csrf": ss.CSRF})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	ss := sessionFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"csrf":          ss.CSRF,
		"expiresAt":     ss.ExpiresAt,
		"version":       config.Version,
		"user":          map[string]any{"name": "Administrator", "role": "owner"},
	})
}

func sessionFrom(r *http.Request) store.Session {
	ss, _ := r.Context().Value(sessionKey).(store.Session)
	return ss
}
