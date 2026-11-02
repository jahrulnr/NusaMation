package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

type AuthRequiredError struct {
	Methods []acp.AuthMethod
	Err     error
}

func (e *AuthRequiredError) Error() string {
	if e.Err != nil {
		return "agent authentication required: " + e.Err.Error()
	}
	return "agent authentication required"
}

type pendingNotif struct {
	method string
	raw    json.RawMessage
	at     time.Time
}

type Runtime struct {
	mgr   *Manager
	agent store.Agent
	log   *slog.Logger

	mu       sync.Mutex
	client   *acp.Client
	starting bool
	startCh  chan struct{}
	state    string
	lastErr  string
	stopping bool

	sessions map[string]*Session
	byACP    map[string]*Session
	pending  map[string][]pendingNotif
}

func (r *Runtime) Info() (string, int, time.Duration, []string) {
	r.mu.Lock()
	c := r.client
	state := r.state
	r.mu.Unlock()
	if c == nil {
		return state, 0, 0, nil
	}
	return state, c.PID(), c.Uptime(), c.Stderr()
}

func (r *Runtime) setState(state, lastErr string) {
	r.mu.Lock()
	if r.state == state && r.lastErr == lastErr {
		r.mu.Unlock()
		return
	}
	r.state = state
	r.lastErr = lastErr
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = r.mgr.store.UpdateAgentStatus(ctx, r.agent.ID, state, lastErr)
	cancel()
	r.mgr.Sink().AgentState(r.agent.ID, state, lastErr)
}

func (r *Runtime) ensureClient(ctx context.Context) (*acp.Client, error) {
	for {
		r.mu.Lock()
		if r.client != nil && r.client.Alive() {
			c := r.client
			r.mu.Unlock()
			return c, nil
		}
		if r.starting {
			ch := r.startCh
			r.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		r.starting = true
		r.startCh = make(chan struct{})
		r.mu.Unlock()

		r.setState("starting", "")
		procCfg := acp.ProcessConfig{
			Command: r.agent.Command,
			Args:    r.agent.Args,
			Env:     envMap(r.agent.Env),
			Dir:     r.agent.Cwd,
			Log:     r.log,
		}
		startCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		client, err := acp.Start(startCtx, acp.ClientConfig{
			ProcessConfig: procCfg,
			ClientInfo:    acp.Implementation{Name: "nusamation", Title: "NusaMation", Version: config.Version},
		}, &runtimeHandler{r: r})
		cancel()

		r.mu.Lock()
		r.starting = false
		if err != nil {
			r.startErrSetLocked(err)
			close(r.startCh)
			r.startCh = nil
			r.mu.Unlock()
			return nil, err
		}
		r.client = client
		r.stopping = false
		close(r.startCh)
		r.startCh = nil
		r.state = "ready"
		r.lastErr = ""
		r.mu.Unlock()
		_ = r.mgr.store.UpdateAgentStatus(context.Background(), r.agent.ID, "ready", "")
		r.mgr.Sink().AgentState(r.agent.ID, "ready", "")
		return client, nil
	}
}

func (r *Runtime) startErrSetLocked(err error) {
	r.state = "error"
	r.lastErr = trimErr(err)
}

func (r *Runtime) EnsureSession(ctx context.Context, req EnsureRequest) (*Session, error) {
	r.mu.Lock()
	if s, ok := r.sessions[req.ConversationID]; ok {
		r.mu.Unlock()
		s.touch()
		if s.isLoaded() {
			return s, nil
		}
		return r.loadOrCreate(ctx, s, req)
	}
	s := &Session{
		agentID:        r.agent.ID,
		conversationID: req.ConversationID,
		cwd:            req.Cwd,
		roots:          append([]string{req.Cwd}, req.AdditionalDirs...),
		createdAt:      time.Now(),
		lastUsed:       time.Now(),
	}
	r.sessions[req.ConversationID] = s
	r.mu.Unlock()
	return r.loadOrCreate(ctx, s, req)
}

func (r *Runtime) loadOrCreate(ctx context.Context, s *Session, req EnsureRequest) (*Session, error) {
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	if s.isLoaded() {
		return s, nil
	}
	client, err := r.ensureClient(ctx)
	if err != nil {
		return s, err
	}
	mcpList, _ := r.mgr.store.ListMCP(ctx, r.agent.ID)
	mcps := mcpConfigs(mcpList, client.Capabilities().AgentCapabilities)
	acpID := strings.TrimSpace(req.ACPSessionID)
	if acpID == "" {
		s.mu.Lock()
		acpID = s.acpID
		s.mu.Unlock()
	}
	recovered := false
	loadedExisting := false
	var state acp.SessionState
	if acpID != "" && !req.ForceNew {
		s.mu.Lock()
		s.acpID = acpID
		s.mu.Unlock()
		r.registerACP(s, acpID)
		if client.SupportsResume() {
			resumeCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
			state, err = client.ResumeSession(resumeCtx, acp.ResumeSessionRequest{
				SessionID: acpID, Cwd: req.Cwd, AdditionalDirectories: req.AdditionalDirs, MCPServers: mcps,
			})
			cancel()
			if err == nil {
				state.SessionID = acpID
				loadedExisting = true
			} else {
				s.replaying.Store(false)
				if acp.IsUnknownSession(err) {
					recovered = true
				} else if acp.IsAuthRequired(err) {
					r.setState("needs_auth", trimErr(err))
					return s, &AuthRequiredError{Methods: client.AuthMethods(), Err: err}
				} else {
					return s, err
				}
			}
		} else if client.SupportsLoad() {
			s.replaying.Store(true)
			loadCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
			state, err = client.LoadSession(loadCtx, acp.LoadSessionRequest{
				SessionID: acpID, Cwd: req.Cwd, AdditionalDirectories: req.AdditionalDirs, MCPServers: mcps,
			})
			cancel()
			s.replaying.Store(false)
			if err == nil {
				state.SessionID = acpID
				loadedExisting = true
			} else if acp.IsUnknownSession(err) {
				recovered = true
			} else if acp.IsAuthRequired(err) {
				r.setState("needs_auth", trimErr(err))
				return s, &AuthRequiredError{Methods: client.AuthMethods(), Err: err}
			} else {
				return s, err
			}
		} else {
			recovered = true
		}
	}
	if !loadedExisting || req.ForceNew {
		newCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
		state, err = client.NewSession(newCtx, acp.NewSessionRequest{
			Cwd: req.Cwd, AdditionalDirectories: req.AdditionalDirs, MCPServers: mcps,
		})
		cancel()
		if err != nil {
			if acp.IsAuthRequired(err) {
				r.setState("needs_auth", trimErr(err))
				return s, &AuthRequiredError{Methods: client.AuthMethods(), Err: err}
			}
			return s, err
		}
		if req.ACPSessionID != "" || acpID != "" {
			recovered = true
		}
	}
	s.mu.Lock()
	s.acpID = state.SessionID
	s.cwd = req.Cwd
	s.roots = append([]string{req.Cwd}, req.AdditionalDirs...)
	if len(state.ConfigOptions) > 0 {
		s.configOptions = append(json.RawMessage(nil), state.ConfigOptions...)
	} else if len(s.configOptions) == 0 && len(req.CachedConfig) > 0 {
		s.configOptions = append(json.RawMessage(nil), req.CachedConfig...)
	}
	s.loaded = true
	s.recovered = recovered
	s.lastUsed = time.Now()
	configs := append(json.RawMessage(nil), s.configOptions...)
	s.mu.Unlock()
	r.registerACP(s, state.SessionID)
	r.setState("ready", "")

	r.mgr.Sink().SessionCreated(s, configs, recovered)
	r.drainPending(state.SessionID, s)

	if err := r.applyDesiredConfig(ctx, client, s, req); err != nil {
		return s, err
	}
	return s, nil
}

func (r *Runtime) applyDesiredConfig(ctx context.Context, client *acp.Client, s *Session, req EnsureRequest) error {
	configs := s.Config()
	if req.DesiredModel != "" {
		id, current, choices := sessionConfigs(configs, "model")
		if id != "" && !anyEqual(current, req.DesiredModel) && contains(choices, req.DesiredModel) {
			if err := r.applyConfig(ctx, client, s, id, req.DesiredModel); err != nil {
				return fmt.Errorf("select model %q: %w", req.DesiredModel, err)
			}
			configs = s.Config()
		}
	}
	for id, value := range req.DesiredConfig {
		if id == "" || value == nil {
			continue
		}
		cur := findConfigCurrent(configs, id)
		if cur == nil || anyEqual(cur, value) {
			continue
		}
		if err := r.applyConfig(ctx, client, s, id, value); err != nil {
			r.log.Warn("failed to apply session config", "config", id, "error", err)
			continue
		}
		configs = s.Config()
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (r *Runtime) applyConfig(ctx context.Context, client *acp.Client, s *Session, id string, value any) error {
	cfgCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := client.SetConfig(cfgCtx, s.ACPID(), id, value)
	if err != nil {
		return err
	}
	if len(res.ConfigOptions) > 0 {
		s.mu.Lock()
		s.configOptions = append(json.RawMessage(nil), res.ConfigOptions...)
		configs := append(json.RawMessage(nil), s.configOptions...)
		s.mu.Unlock()
		r.mgr.Sink().SessionConfigUpdated(s, configs)
		_ = r.mgr.store.UpdateAgentConfigCache(context.Background(), r.agent.ID, configs)
	}
	return nil
}

func (r *Runtime) registerACP(s *Session, acpID string) {
	if acpID == "" {
		return
	}
	r.mu.Lock()
	r.byACP[acpID] = s
	r.mu.Unlock()
}

func (r *Runtime) drainPending(acpID string, s *Session) {
	r.mu.Lock()
	list := r.pending[acpID]
	delete(r.pending, acpID)
	r.mu.Unlock()
	for _, n := range list {
		r.dispatchNotification(s, n.method, n.raw)
	}
}

func (r *Runtime) findSession(acpID string) *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byACP[acpID]
}

func (r *Runtime) Prompt(ctx context.Context, s *Session, blocks []acp.ContentBlock) (acp.PromptResult, error) {
	client, err := r.ensureClient(ctx)
	if err != nil {
		return acp.PromptResult{}, err
	}
	s.touch()
	return client.Prompt(ctx, s.ACPID(), blocks)
}

func (r *Runtime) Cancel(ctx context.Context, s *Session) error {
	client, err := r.ensureClient(ctx)
	if err != nil {
		return err
	}
	return client.Cancel(s.ACPID())
}

func (r *Runtime) SetConfig(ctx context.Context, s *Session, configID string, value any) (json.RawMessage, error) {
	client, err := r.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	res, err := client.SetConfig(ctx, s.ACPID(), configID, value)
	if err != nil {
		return nil, err
	}
	configs := res.ConfigOptions
	if len(configs) == 0 {
		configs = s.Config()
	} else {
		s.mu.Lock()
		s.configOptions = append(json.RawMessage(nil), configs...)
		s.mu.Unlock()
	}
	r.mgr.Sink().SessionConfigUpdated(s, configs)
	_ = r.mgr.store.UpdateAgentConfigCache(context.Background(), r.agent.ID, configs)
	return configs, nil
}

func (r *Runtime) DropSession(ctx context.Context, s *Session) {
	client := func() *acp.Client {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.client
	}()
	if client != nil && client.SupportsClose() {
		closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = client.CloseSession(closeCtx, s.ACPID())
		cancel()
	}
	s.mu.Lock()
	s.loaded = false
	acpID := s.acpID
	s.acpID = ""
	s.mu.Unlock()
	r.mu.Lock()
	if r.sessions[s.conversationID] == s {
		delete(r.sessions, s.conversationID)
	}
	if r.byACP[acpID] == s {
		delete(r.byACP, acpID)
	}
	r.mu.Unlock()
}

func (r *Runtime) Stop(reason string) {
	r.mu.Lock()
	r.stopping = true
	c := r.client
	r.client = nil
	sessions := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	r.setState("stopped", reason)
	for _, s := range sessions {
		s.mu.Lock()
		s.loaded = false
		s.mu.Unlock()
	}
}

func (r *Runtime) Authenticate(ctx context.Context, methodID string) error {
	client, err := r.ensureClient(ctx)
	if err != nil {
		return err
	}
	if err := client.Authenticate(ctx, methodID); err != nil {
		return err
	}
	r.setState("ready", "")
	return nil
}

func (r *Runtime) Logout(ctx context.Context) error {
	client, err := r.ensureClient(ctx)
	if err != nil {
		return err
	}
	if err := client.Logout(ctx); err != nil {
		return err
	}
	r.setState("needs_auth", "logged out")
	return nil
}

func (r *Runtime) handleNotification(method, sessionID string, raw json.RawMessage) {
	if method != "session/update" {
		if strings.HasPrefix(method, "_cognition.ai/output") {
			var out struct {
				Level     string `json:"level"`
				Message   string `json:"message"`
				Channel   string `json:"channel"`
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(raw, &out) == nil && (out.Level == "warn" || out.Level == "error") {
				r.mgr.Sink().AgentLog(r.agent.ID, "["+out.Level+"] "+out.Channel+": "+out.Message)
			}
		}
		return
	}
	if sessionID == "" {
		return
	}
	s := r.findSession(sessionID)
	if s == nil {
		r.mu.Lock()
		list := r.pending[sessionID]
		if len(list) < 200 {
			r.pending[sessionID] = append(list, pendingNotif{method: method, raw: append(json.RawMessage(nil), raw...), at: time.Now()})
		}
		// Keep the buffer bounded even for sessions that never materialize.
		if len(r.pending) > 20 {
			for id, items := range r.pending {
				if len(items) == 0 || time.Since(items[0].at) > 10*time.Minute || id != sessionID {
					delete(r.pending, id)
				}
				break
			}
		}
		r.mu.Unlock()
		return
	}
	r.dispatchNotification(s, method, raw)
}

func (r *Runtime) dispatchNotification(s *Session, method string, raw json.RawMessage) {
	if s.replaying.Load() {
		return
	}
	var u acp.SessionUpdate
	if err := json.Unmarshal(raw, &u); err != nil {
		r.log.Debug("invalid session update", "error", err)
		return
	}
	u.Raw = raw
	switch u.SessionUpdate {
	case "config_option_update":
		if len(u.ConfigOptions) > 0 {
			s.mu.Lock()
			s.configOptions = append(json.RawMessage(nil), u.ConfigOptions...)
			configs := append(json.RawMessage(nil), s.configOptions...)
			s.mu.Unlock()
			r.mgr.Sink().SessionConfigUpdated(s, configs)
			_ = r.mgr.store.UpdateAgentConfigCache(context.Background(), r.agent.ID, configs)
		}
		return
	case "current_mode_update":
		// Mode is also reflected in config options on modern agents.
	}
	r.mgr.Sink().SessionUpdate(s, u, raw)
}

func (r *Runtime) onExit(err error) {
	r.mu.Lock()
	wasStopping := r.stopping
	// A restarted process can replace r.client while the old process is still
	// winding down. Never let a stale exit tear down the new runtime.
	if r.client != nil && r.client.Alive() {
		r.mu.Unlock()
		return
	}
	r.client = nil
	sessions := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		if s.isLoaded() {
			sessions = append(sessions, s)
		}
	}
	for _, s := range r.sessions {
		s.mu.Lock()
		s.loaded = false
		s.mu.Unlock()
	}
	r.mu.Unlock()
	if wasStopping {
		r.setState("stopped", "agent stopped")
		return
	}
	msg := "agent process exited"
	if err != nil {
		msg = trimErr(err)
	}
	r.setState("error", msg)
	r.mgr.Sink().AgentExit(r.agent.ID, err)
	for _, s := range sessions {
		r.mgr.Sink().SessionLost(s, errors.New(msg))
	}
}

func (s *Session) isLoaded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loaded
}

type runtimeHandler struct {
	r *Runtime
}

func (h *runtimeHandler) OnNotification(method, sessionID string, update json.RawMessage) {
	h.r.handleNotification(method, sessionID, update)
}

func (h *runtimeHandler) OnStderr(line string) {
	// Kept in the runtime ring buffer; surfaced through the agent logs endpoint.
}

func (h *runtimeHandler) OnExit(err error) {
	h.r.onExit(err)
}

func (h *runtimeHandler) OnRequest(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	switch method {
	case "session/request_permission":
		var req acp.PermissionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "invalid permission request"}
		}
		s := h.r.findSession(req.SessionID)
		if s == nil {
			return acp.PermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "cancelled"}}, nil
		}
		resp, err := h.r.mgr.Sink().Permission(ctx, s, req)
		if err != nil {
			return acp.PermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "cancelled"}}, nil
		}
		return resp, nil
	case "fs/read_text_file":
		return h.readFile(params)
	case "fs/write_text_file":
		return h.writeFile(params)
	case "terminal/create", "terminal/output", "terminal/wait_for_exit", "terminal/kill", "terminal/release":
		var ref struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(params, &ref)
		s := h.r.findSession(ref.SessionID)
		if s == nil {
			return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "unknown session"}
		}
		return h.r.mgr.terminals.Handle(ctx, method, params, s.Roots())
	case "elicitation/create":
		return nil, &acp.RPCError{Code: acp.CodeMethodNotFound, Message: "elicitation is not supported by this client"}
	default:
		return nil, &acp.RPCError{Code: acp.CodeMethodNotFound, Message: "unsupported client method"}
	}
}

func (h *runtimeHandler) readFile(raw json.RawMessage) (any, *acp.RPCError) {
	var req acp.FSReadRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "invalid read request"}
	}
	s := h.r.findSession(req.SessionID)
	if s == nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "unknown session"}
	}
	path, err := securityPath(s.Roots(), req.Path)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: err.Error()}
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeResourceNotFound, Message: err.Error()}
	}
	if st.Size() > 16<<20 {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "file is too large to read"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	lines := strings.SplitAfter(string(data), "\n")
	start := 0
	if req.Line != nil && *req.Line > 1 {
		start = *req.Line - 1
		if start > len(lines) {
			start = len(lines)
		}
	}
	end := len(lines)
	if req.Limit != nil && *req.Limit >= 0 && start+*req.Limit < end {
		end = start + *req.Limit
	}
	return acp.FSReadResponse{Content: strings.Join(lines[start:end], "")}, nil
}

func (h *runtimeHandler) writeFile(raw json.RawMessage) (any, *acp.RPCError) {
	var req acp.FSWriteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "invalid write request"}
	}
	s := h.r.findSession(req.SessionID)
	if s == nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "unknown session"}
	}
	path, err := securityPath(s.Roots(), req.Path)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: err.Error()}
	}
	if sensitivePath(path) {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "writing execution-control or secret files requires manual approval outside ACP"}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	if err := os.WriteFile(path, []byte(req.Content), 0o644); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	return map[string]any{}, nil
}

func securityPath(roots []string, p string) (string, error) {
	if len(roots) == 0 {
		return "", errors.New("session has no workspace root")
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("path must be absolute")
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if abs, err := security.SafePath(root, p, false); err == nil {
			return abs, nil
		}
	}
	return "", fmt.Errorf("path %q is outside the session workspace", p)
}

func sensitivePath(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "mcp_config.json" {
		return true
	}
	parts := strings.Split(filepath.Clean(p), string(os.PathSeparator))
	for i, part := range parts {
		lp := strings.ToLower(part)
		if lp == ".git" && i+1 < len(parts) && strings.ToLower(parts[i+1]) == "hooks" {
			return true
		}
		if lp == ".agents" || lp == ".claude" || lp == ".devin" {
			return true
		}
	}
	return false
}

var _ = atomic.Bool{}
