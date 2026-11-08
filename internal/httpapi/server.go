package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/browser"
	"github.com/jahrulnr/nusamation/internal/browseruse"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/notify"
	"github.com/jahrulnr/nusamation/internal/realtime"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/update"
	webui "github.com/jahrulnr/nusamation/web"
)

const sessionCookie = "nm_session"

type Server struct {
	cfg        config.Config
	store      *store.Store
	chat       *chat.Service
	agents     *agent.Manager
	browser    *browser.Manager
	browserUse *browseruse.Grants
	channels   *channels.Manager
	hub        *realtime.Hub
	notifier   *notify.Broadcaster
	updater    *update.Manager
	log        *slog.Logger
	metaMu     sync.Mutex
	started    time.Time
	limiter    *loginLimiter
	static     fs.FS
	index      []byte
}

type Options struct {
	Config     config.Config
	Store      *store.Store
	Chat       *chat.Service
	Agents     *agent.Manager
	Browser    *browser.Manager
	BrowserUse *browseruse.Grants
	Channels   *channels.Manager
	Hub        *realtime.Hub
	Notifier   *notify.Broadcaster
	Updater    *update.Manager
	Log        *slog.Logger
}

func New(opts Options) (*Server, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	static := webui.FS()
	index, err := fs.ReadFile(static, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read embedded index.html: %w", err)
	}
	s := &Server{
		cfg: opts.Config, store: opts.Store, chat: opts.Chat, agents: opts.Agents,
		browser: opts.Browser, browserUse: opts.BrowserUse, channels: opts.Channels, hub: opts.Hub,
		notifier: opts.Notifier, updater: opts.Updater, log: log.With("component", "http"),
		started: time.Now(), limiter: newLoginLimiter(8, 5*time.Minute), static: static, index: index,
	}
	if opts.Config.PublicURL != "" && s.hub != nil {
		s.hub.AllowOrigin(opts.Config.PublicURL)
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.protected(s.handleLogout))
	mux.HandleFunc("POST /api/auth/password", s.protected(s.handlePassword))
	mux.HandleFunc("GET /api/auth/me", s.protected(s.handleMe))

	mux.HandleFunc("GET /api/automations", s.protected(s.handleAutomations))
	mux.HandleFunc("POST /api/automations", s.protected(s.handleAutomations))
	mux.HandleFunc("GET /api/automations/{id}", s.protected(s.handleAutomation))
	mux.HandleFunc("PUT /api/automations/{id}", s.protected(s.handleAutomation))
	mux.HandleFunc("DELETE /api/automations/{id}", s.protected(s.handleAutomation))
	mux.HandleFunc("PATCH /api/automations/{id}/enabled", s.protected(s.handleAutomationEnabled))
	mux.HandleFunc("GET /api/bootstrap", s.protected(s.handleBootstrap))
	mux.HandleFunc("GET /api/events", s.protected(s.handleEvents))

	mux.HandleFunc("GET /api/conversations", s.protected(s.handleListConversations))
	mux.HandleFunc("POST /api/conversations", s.protected(s.handleCreateConversation))
	mux.HandleFunc("GET /api/conversations/{id}", s.protected(s.handleGetConversation))
	mux.HandleFunc("PATCH /api/conversations/{id}", s.protected(s.handleUpdateConversation))
	mux.HandleFunc("DELETE /api/conversations/{id}", s.protected(s.handleDeleteConversation))
	mux.HandleFunc("GET /api/conversations/{id}/messages", s.protected(s.handleMessages))
	mux.HandleFunc("POST /api/conversations/{id}/messages", s.protected(s.handlePrompt))
	mux.HandleFunc("GET /api/conversations/{id}/meta", s.protected(s.handleGetConversationMeta))
	mux.HandleFunc("PUT /api/conversations/{id}/meta", s.protected(s.handlePutConversationMeta))
	mux.HandleFunc("POST /api/conversations/{id}/cancel", s.protected(s.handleCancel))
	mux.HandleFunc("POST /api/conversations/{id}/turns/{turnId}/cancel", s.protected(s.handleCancelQueuedTurn))
	mux.HandleFunc("POST /api/conversations/{id}/retry", s.protected(s.handleRetry))
	mux.HandleFunc("POST /api/conversations/{id}/prepare", s.protected(s.handlePrepareConversation))
	mux.HandleFunc("POST /api/conversations/{id}/elicitations/{elicitationId}", s.protected(s.handleElicitationResponse))
	mux.HandleFunc("POST /api/conversations/{id}/config", s.protected(s.handleSetConfig))
	mux.HandleFunc("POST /api/conversations/{id}/agent", s.protected(s.handleSwitchAgent))
	mux.HandleFunc("POST /api/conversations/{id}/routing", s.protected(s.handleRouting))
	mux.HandleFunc("POST /api/conversations/{id}/approvals/{approvalId}", s.protected(s.handleResolveApproval))
	mux.HandleFunc("GET /api/conversations/{id}/export", s.protected(s.handleExport))
	mux.HandleFunc("DELETE /api/conversations", s.protected(s.handleDeleteAllConversations))

	mux.HandleFunc("GET /api/projects", s.protected(s.handleListProjects))
	mux.HandleFunc("POST /api/projects", s.protected(s.handleCreateProject))
	mux.HandleFunc("PATCH /api/projects/{id}", s.protected(s.handleUpdateProject))
	mux.HandleFunc("DELETE /api/projects/{id}", s.protected(s.handleDeleteProject))
	mux.HandleFunc("POST /api/projects/{id}/restore", s.protected(s.handleRestoreProject))
	mux.HandleFunc("GET /api/fs/list", s.protected(s.handleListDirs))

	mux.HandleFunc("GET /api/approvals", s.protected(s.handleListApprovals))
	mux.HandleFunc("POST /api/approvals/{id}", s.protected(s.handleResolveApprovalByID))
	mux.HandleFunc("GET /api/approval-rules", s.protected(s.handleListApprovalRules))
	mux.HandleFunc("DELETE /api/approval-rules/{id}", s.protected(s.handleDeleteApprovalRule))

	mux.HandleFunc("GET /api/agents", s.protected(s.handleListAgents))
	mux.HandleFunc("POST /api/agents", s.protected(s.handleCreateAgent))
	mux.HandleFunc("GET /api/agents/{id}", s.protected(s.handleGetAgent))
	mux.HandleFunc("PATCH /api/agents/{id}", s.protected(s.handleUpdateAgent))
	mux.HandleFunc("DELETE /api/agents/{id}", s.protected(s.handleDeleteAgent))
	mux.HandleFunc("POST /api/agents/{id}/probe", s.protected(s.handleProbeAgent))
	mux.HandleFunc("POST /api/agents/{id}/authenticate", s.protected(s.handleAuthenticateAgent))
	mux.HandleFunc("GET /api/agents/{id}/auth-terminal", s.protected(s.handleAuthTerminal))
	mux.HandleFunc("POST /api/agents/{id}/logout", s.protected(s.handleLogoutAgent))
	mux.HandleFunc("GET /api/agents/{id}/logs", s.protected(s.handleAgentLogs))

	mux.HandleFunc("GET /api/mcp", s.protected(s.handleListMCP))
	mux.HandleFunc("POST /api/mcp", s.protected(s.handleCreateMCP))
	mux.HandleFunc("PATCH /api/mcp/{id}", s.protected(s.handleUpdateMCP))
	mux.HandleFunc("DELETE /api/mcp/{id}", s.protected(s.handleDeleteMCP))

	mux.HandleFunc("GET /api/channels", s.protected(s.handleChannels))
	mux.HandleFunc("POST /api/channels/{name}/connect", s.protected(s.handleChannelConnect))
	mux.HandleFunc("POST /api/channels/whatsapp/send", s.protected(s.handleWhatsAppSend))
	mux.HandleFunc("GET /api/channels/whatsapp/user", s.protected(s.handleWhatsAppUser))
	mux.HandleFunc("POST /api/channels/{name}/disconnect", s.protected(s.handleChannelDisconnect))
	mux.HandleFunc("POST /api/channels/{name}/logout", s.protected(s.handleChannelLogout))
	mux.HandleFunc("GET /api/channels/{name}/qr", s.protected(s.handleChannelQR))
	mux.HandleFunc("GET /api/channels/{name}/settings", s.protected(s.handleChannelSettings))
	mux.HandleFunc("PUT /api/channels/{name}/settings", s.protected(s.handleUpdateChannelSettings))

	mux.HandleFunc("POST /api/uploads", s.protected(s.handleUpload))
	mux.HandleFunc("GET /api/uploads/{id}", s.protected(s.handleGetUpload))

	mux.HandleFunc("POST /api/browser/sessions", s.protected(s.handleBrowserSessionCreate))
	mux.HandleFunc("GET /api/browser/sessions", s.protected(s.handleBrowserSessionList))
	mux.HandleFunc("GET /api/browser/sessions/{id}", s.protected(s.handleBrowserSessionGet))
	mux.HandleFunc("DELETE /api/browser/sessions/{id}", s.protected(s.handleBrowserSessionDelete))
	mux.HandleFunc("POST /api/browser/sessions/{id}/navigate", s.protected(s.handleBrowserNavigate))
	mux.HandleFunc("POST /api/browser/sessions/{id}/history", s.protected(s.handleBrowserHistory))
	mux.HandleFunc("POST /api/browser/sessions/{id}/annotate", s.protected(s.handleBrowserAnnotate))
	mux.HandleFunc("POST /api/browser/sessions/{id}/annotate/preview", s.protected(s.handleBrowserAnnotatePreview))
	mux.HandleFunc("POST /api/browser/sessions/{id}/annotate/clear", s.protected(s.handleBrowserAnnotateClear))
	mux.HandleFunc("GET /api/browser/sessions/{id}/stream", s.protected(s.handleBrowserStream))
	mux.HandleFunc("POST /api/browser-use", s.handleBrowserUse)

	mux.HandleFunc("GET /api/update", s.protected(s.handleUpdateStatus))
	mux.HandleFunc("POST /api/update/check", s.protected(s.handleUpdateCheck))
	mux.HandleFunc("POST /api/update/apply", s.protected(s.handleUpdateApply))
	mux.HandleFunc("POST /api/notifications/test", s.protected(s.handleTestNotification))

	mux.HandleFunc("GET /api/settings", s.protected(s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", s.protected(s.handleUpdateSettings))
	mux.HandleFunc("POST /api/settings/agent-defaults/preview", s.protected(s.handlePreviewAgentDefaults))
	mux.HandleFunc("PUT /api/settings/agent-defaults", s.protected(s.handleSaveAgentDefaults))
	mux.HandleFunc("GET /api/stats", s.protected(s.handleStats))

	static := http.FileServer(http.FS(s.static))
	mux.Handle("GET /static/", http.StripPrefix("/static/", noDirListing(static)))
	mux.HandleFunc("GET /favicon.png", s.serveFile("mark.png", "image/png"))
	mux.HandleFunc("/", s.handleSPA)

	return s.recoverer(s.securityHeaders(s.requestLog(mux)))
}

func noDirListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "not_found", "API route not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(s.index)
}

func (s *Server) serveFile(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(s.static, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(data)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "name": config.AppName, "version": config.Version,
		"uptimeSeconds": int64(time.Since(s.started).Seconds()),
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "realtime hub is not available")
		return
	}
	s.hub.Serve(w, r)
}

// ---------- middleware ----------

type ctxKey int

const sessionKey ctxKey = 1

func (s *Server) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ss, ok := s.session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !s.sameOrigin(r) {
				writeError(w, http.StatusForbidden, "bad_origin", "Request origin is not allowed.")
				return
			}
			token := r.Header.Get("X-CSRF-Token")
			if token == "" {
				token = r.FormValue("csrf")
			}
			if !security.ConstantTimeEqual(token, ss.CSRF) {
				writeError(w, http.StatusForbidden, "csrf", "Your session token expired. Reload the page and try again.")
				return
			}
		}
		ctx := context.WithValue(r.Context(), sessionKey, ss)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) session(r *http.Request) (store.Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return store.Session{}, false
	}
	ss, err := auth.LookupSession(r.Context(), s.store, c.Value)
	if err != nil {
		return store.Session{}, false
	}
	if time.Since(time.UnixMilli(ss.LastSeenAt)) > time.Minute {
		_ = s.store.TouchSession(r.Context(), ss.IDHash)
	}
	return ss, true
}

func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if s.cfg.PublicURL != "" {
		if p, err := url.Parse(s.cfg.PublicURL); err == nil && strings.EqualFold(u.Host, p.Host) {
			return true
		}
	}
	return false
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob: https:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; font-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/events") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.log.Debug("http", "method", r.Method, "path", r.URL.Path,
				"status", sw.status, "bytes", sw.bytes, "duration", time.Since(start))
		}
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in http handler", "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": message, "code": code})
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any, max int64) error {
	if max <= 0 {
		max = 2 << 20
	}
	body := http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	secure := r.TLS != nil || strings.HasPrefix(s.cfg.PublicURL, "https://")
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode,
	})
}

func parseInt(q string, def int) int {
	if q == "" {
		return def
	}
	n, err := strconv.Atoi(q)
	if err != nil {
		return def
	}
	return n
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// ---------- login rate limiting ----------

type loginLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	attempts map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, attempts: map[string][]time.Time{}}
}

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	list := l.attempts[key]
	kept := list[:0]
	for _, t := range list {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.attempts[key] = kept
	return len(kept) < l.max
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	l.attempts[key] = append(l.attempts[key], time.Now())
	l.mu.Unlock()
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}
