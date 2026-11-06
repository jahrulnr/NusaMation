package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/store"
)

type Sink interface {
	AgentState(agentID, status, lastError string)
	SessionCreated(s *Session, configOptions json.RawMessage, recovered bool)
	SessionUpdate(s *Session, update acp.SessionUpdate, raw json.RawMessage)
	TerminalUpdate(s *Session, update TerminalUpdate)
	SessionConfigUpdated(s *Session, configOptions json.RawMessage)
	Permission(ctx context.Context, s *Session, req acp.PermissionRequest) (acp.PermissionResponse, error)
	Elicit(ctx context.Context, s *Session, request json.RawMessage) (any, error)
	AgentLog(agentID, line string)
	AgentExit(agentID string, err error)
	SessionLost(s *Session, err error)
}

type NopSink struct{}

func (NopSink) AgentState(string, string, string)                          {}
func (NopSink) SessionCreated(*Session, json.RawMessage, bool)             {}
func (NopSink) SessionUpdate(*Session, acp.SessionUpdate, json.RawMessage) {}
func (NopSink) TerminalUpdate(*Session, TerminalUpdate)                    {}
func (NopSink) SessionConfigUpdated(*Session, json.RawMessage)             {}
func (NopSink) Permission(context.Context, *Session, acp.PermissionRequest) (acp.PermissionResponse, error) {
	return acp.PermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "cancelled"}}, nil
}
func (NopSink) Elicit(context.Context, *Session, json.RawMessage) (any, error) {
	return map[string]any{"action": "cancel"}, nil
}
func (NopSink) AgentLog(string, string)     {}
func (NopSink) AgentExit(string, error)     {}
func (NopSink) SessionLost(*Session, error) {}

type Session struct {
	agentID        string
	conversationID string
	acpID          string
	cwd            string
	roots          []string
	configOptions  json.RawMessage
	modes          *acp.SessionModeState
	legacyMode     bool
	loaded         bool
	recovered      bool
	createdAt      time.Time
	lastUsed       time.Time
	mu             sync.Mutex
	ensureMu       sync.Mutex
	replaying      atomic.Bool
}

func (s *Session) AgentID() string        { return s.agentID }
func (s *Session) ConversationID() string { return s.conversationID }
func (s *Session) ACPID() string          { s.mu.Lock(); defer s.mu.Unlock(); return s.acpID }
func (s *Session) Cwd() string            { s.mu.Lock(); defer s.mu.Unlock(); return s.cwd }
func (s *Session) Config() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(json.RawMessage(nil), s.configOptions...)
}
func (s *Session) Roots() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.roots...)
}
func (s *Session) Recovered() bool      { s.mu.Lock(); defer s.mu.Unlock(); return s.recovered }
func (s *Session) ClearRecovered()      { s.mu.Lock(); s.recovered = false; s.mu.Unlock() }
func (s *Session) CreatedAt() time.Time { return s.createdAt }
func (s *Session) LastUsed() time.Time  { s.mu.Lock(); defer s.mu.Unlock(); return s.lastUsed }
func (s *Session) touch()               { s.mu.Lock(); s.lastUsed = time.Now(); s.mu.Unlock() }

type EnsureRequest struct {
	AgentID        string
	ConversationID string
	Cwd            string
	ACPSessionID   string
	AdditionalDirs []string
	DesiredModel   string
	DesiredConfig  map[string]any
	DefaultConfig  []store.ConfigSelection
	CachedConfig   json.RawMessage
	ForceNew       bool
}

type Manager struct {
	store     *store.Store
	log       *slog.Logger
	terminals *TerminalManager

	mu       sync.Mutex
	sink     Sink
	runtimes map[string]*Runtime
	byConv   map[string]*Session
}

func NewManager(st *store.Store, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		store:     st,
		log:       log.With("component", "agents"),
		terminals: NewTerminalManager(log),
		sink:      NopSink{},
		runtimes:  map[string]*Runtime{},
		byConv:    map[string]*Session{},
	}
}

func (m *Manager) SetSink(s Sink) {
	if s == nil {
		s = NopSink{}
	}
	m.mu.Lock()
	m.sink = s
	m.mu.Unlock()
}

func (m *Manager) Sink() Sink {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sink
}

func (m *Manager) ensureRuntime(agent store.Agent) *Runtime {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runtimes[agent.ID]; ok {
		return r
	}
	r := &Runtime{mgr: m, agent: agent, sessions: map[string]*Session{}, byACP: map[string]*Session{}, pending: map[string][]pendingNotif{}, state: "unknown"}
	m.runtimes[agent.ID] = r
	return r
}

func (m *Manager) InvalidateAgent(agentID string) {
	m.mu.Lock()
	r := m.runtimes[agentID]
	delete(m.runtimes, agentID)
	for id, session := range m.byConv {
		if session.AgentID() == agentID {
			delete(m.byConv, id)
		}
	}
	m.mu.Unlock()
	if r != nil {
		r.Stop("agent configuration changed")
	}
}

func (m *Manager) InvalidateAllAgents() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.runtimes))
	for id := range m.runtimes {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.InvalidateAgent(id)
	}
}

// WarmAgent starts an enabled auto-start agent in the background and keeps its
// idle preview session ready so option/catalog requests skip the cold launch.
// It is a no-op for agents without auto-start.
func (m *Manager) WarmAgent(agentID string) {
	agent, err := m.store.GetAgent(context.Background(), agentID)
	if err != nil || !agent.Enabled || !agent.AutoStart {
		return
	}
	r := m.ensureRuntime(agent)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		r.warm(ctx)
	}()
}

// WarmAutoStart warms every enabled agent marked auto-start, e.g. at boot or
// after a change invalidated all runtimes.
func (m *Manager) WarmAutoStart() {
	list, err := m.store.ListAgents(context.Background())
	if err != nil {
		m.log.Warn("could not list agents for warm-up", "error", err)
		return
	}
	for _, a := range list {
		if !a.Enabled || !a.AutoStart {
			continue
		}
		r := m.ensureRuntime(a)
		go func(r *Runtime) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			r.warm(ctx)
		}(r)
	}
}

func (m *Manager) EnsureSession(ctx context.Context, req EnsureRequest) (*Session, error) {
	agent, err := m.store.GetAgent(ctx, req.AgentID)
	if err != nil {
		return nil, err
	}
	if !agent.Enabled {
		return nil, fmt.Errorf("agent %q is disabled", agent.Name)
	}
	if req.Cwd == "" {
		req.Cwd = agent.Cwd
	}
	if !filepath.IsAbs(req.Cwd) {
		return nil, fmt.Errorf("working directory must be absolute")
	}
	if st, err := os.Stat(req.Cwd); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("working directory %s is not accessible", req.Cwd)
	}
	for _, d := range req.AdditionalDirs {
		if d == "" {
			continue
		}
		if !filepath.IsAbs(d) {
			return nil, fmt.Errorf("additional directory %q must be absolute", d)
		}
	}
	r := m.ensureRuntime(agent)
	sess, err := r.EnsureSession(ctx, req)
	if err != nil {
		if sess != nil && sess.isLoaded() {
			r.DropSession(context.Background(), sess)
		}
		return sess, err
	}
	conv, err := m.store.GetConversation(ctx, req.ConversationID)
	if err != nil || conv.AgentID != req.AgentID {
		r.DropSession(context.Background(), sess)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("agent changed while starting the session")
	}
	m.mu.Lock()
	m.byConv[req.ConversationID] = sess
	m.mu.Unlock()
	return sess, nil
}

// Session returns the live ACP session for a conversation, if one is loaded.
func (m *Manager) Session(conversationID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byConv[conversationID]
}

// PromptCapabilities reports the active agent's prompt capabilities. It is
// intentionally conservative: agents that have not started yet advertise no
// optional capabilities until probed.
func (m *Manager) PromptCapabilities(agentID string) acp.PromptCapabilities {
	r := m.runtimeFor(agentID)
	if r == nil {
		return acp.PromptCapabilities{}
	}
	r.mu.Lock()
	c := r.client
	r.mu.Unlock()
	if c == nil {
		return acp.PromptCapabilities{}
	}
	return c.PromptCapabilities()
}

func (m *Manager) Prompt(ctx context.Context, s *Session, blocks []acp.ContentBlock) (acp.PromptResult, error) {
	r := m.runtimeFor(s.agentID)
	if r == nil {
		return acp.PromptResult{}, errors.New("agent runtime is not running")
	}
	return r.Prompt(ctx, s, blocks)
}

// InjectionMode reports how a live agent accepts input during a running turn.
// midTurn means a second session/prompt is injected (Devin). steering means
// the _session/steering extension is advertised (Codex, Claude).
func (m *Manager) InjectionMode(agentID string) (midTurn, steering bool) {
	r := m.runtimeFor(agentID)
	if r == nil {
		return false, false
	}
	r.mu.Lock()
	c := r.client
	r.mu.Unlock()
	if c == nil || !c.Alive() {
		return false, false
	}
	init := c.InitializeResult()
	return init.SupportsMidTurn(), init.SupportsSteering()
}

func (m *Manager) Steer(ctx context.Context, s *Session, blocks []acp.ContentBlock) (acp.SteerResult, error) {
	r := m.runtimeFor(s.agentID)
	if r == nil {
		return acp.SteerResult{}, errors.New("agent runtime is not running")
	}
	r.mu.Lock()
	c := r.client
	r.mu.Unlock()
	if c == nil || !c.Alive() {
		return acp.SteerResult{}, errors.New("agent is not running")
	}
	return c.Steer(ctx, s.ACPID(), blocks)
}

func (m *Manager) Cancel(ctx context.Context, s *Session) error {
	r := m.runtimeFor(s.agentID)
	if r == nil {
		return nil
	}
	return r.Cancel(ctx, s)
}

func (m *Manager) SetConfig(ctx context.Context, s *Session, configID string, value any) (json.RawMessage, error) {
	r := m.runtimeFor(s.agentID)
	if r == nil {
		return nil, errors.New("agent runtime is not running")
	}
	return r.SetConfig(ctx, s, configID, value)
}

func (m *Manager) ResetConversation(ctx context.Context, conversationID string) {
	m.mu.Lock()
	s := m.byConv[conversationID]
	m.mu.Unlock()
	if s == nil {
		return
	}
	r := m.runtimeFor(s.agentID)
	if r != nil {
		r.DropSession(ctx, s)
	}
	m.mu.Lock()
	if m.byConv[conversationID] == s {
		delete(m.byConv, conversationID)
	}
	m.mu.Unlock()
}

// DeleteStoredSession uses the original agent, or a reconnected agent with the
// same slug if the original configuration has been removed.
func (m *Manager) DeleteStoredSession(ctx context.Context, session store.ConversationACPSession) error {
	ag, err := m.store.GetAgent(ctx, session.AgentID)
	if store.IsNotFound(err) {
		ag, err = m.store.GetAgentBySlug(ctx, session.AgentSlug)
	}
	if err != nil {
		return err
	}
	if !ag.Enabled {
		return fmt.Errorf("agent %q is disabled", ag.Name)
	}
	client, err := m.ensureRuntime(ag).ensureClient(ctx)
	if err != nil {
		return err
	}
	if !client.Capabilities().SupportsDelete() {
		return fmt.Errorf("agent %q does not support session/delete", ag.Name)
	}
	return client.DeleteSession(ctx, session.SessionID)
}

func (m *Manager) runtimeFor(agentID string) *Runtime {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runtimes[agentID]
}

func (m *Manager) AgentRuntimeInfo(agentID string) (status string, pid int, uptime time.Duration, logs []string) {
	r := m.runtimeFor(agentID)
	if r == nil {
		return "stopped", 0, 0, nil
	}
	return r.Info()
}

func (m *Manager) Authenticate(ctx context.Context, agentID, methodID string) error {
	agent, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	r := m.ensureRuntime(agent)
	return r.Authenticate(ctx, methodID)
}

// TerminalAuthCommand reproduces the configured agent launch and applies the
// terminal authentication method advertised by that agent.
func (m *Manager) TerminalAuthCommand(ctx context.Context, agentID, methodID string) (*exec.Cmd, error) {
	ag, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	r := m.ensureRuntime(ag)
	client, err := r.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	var selected *acp.AuthMethod
	for _, method := range client.AuthMethods() {
		if method.ID == methodID && method.Type == "terminal" {
			m := method
			selected = &m
			break
		}
	}
	if selected == nil {
		return nil, errors.New("terminal authentication method is not available")
	}
	command, args, extra, err := resolveACPLaunch(ag.Command, ag.Args)
	if err != nil {
		return nil, err
	}
	args = append(args, selected.Args...)
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = ag.Cwd
	env := map[string]string{}
	for _, value := range os.Environ() {
		key, v, ok := strings.Cut(value, "=")
		if ok {
			env[key] = v
		}
	}
	for key, value := range extra {
		env[key] = value
	}
	for key, value := range envMap(ag.Env) {
		env[key] = value
	}
	for key, value := range selected.Env {
		env[key] = value
	}
	env["TERM"] = "xterm-256color"
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd, nil
}

func (m *Manager) Logout(ctx context.Context, agentID string) error {
	r := m.runtimeFor(agentID)
	if r == nil {
		return nil
	}
	return r.Logout(ctx)
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	runtimes := make([]*Runtime, 0, len(m.runtimes))
	for _, r := range m.runtimes {
		runtimes = append(runtimes, r)
	}
	m.mu.Unlock()
	for _, r := range runtimes {
		r.Stop("server shutting down")
	}
	m.terminals.CloseAll()
}

type ProbeResult struct {
	AgentInfo     *acp.Implementation `json:"agentInfo,omitempty"`
	Capabilities  json.RawMessage     `json:"capabilities"`
	AuthMethods   json.RawMessage     `json:"authMethods"`
	ConfigOptions json.RawMessage     `json:"configOptions"`
	Models        json.RawMessage     `json:"models"`
	Protocol      int                 `json:"protocolVersion"`
	DurationMS    int64               `json:"durationMs"`
}

type probeHandler struct{}

func (probeHandler) OnNotification(string, string, json.RawMessage) {}
func (probeHandler) OnStderr(string)                                {}
func (probeHandler) OnExit(error)                                   {}
func (probeHandler) OnRequest(_ context.Context, method string, _ json.RawMessage) (any, *acp.RPCError) {
	return nil, &acp.RPCError{Code: acp.CodeMethodNotFound, Message: "probe client does not support " + method}
}

func (m *Manager) Probe(ctx context.Context, agentID string) (ProbeResult, error) {
	agent, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return ProbeResult{}, err
	}
	start := time.Now()
	var client *acp.Client
	var rt *Runtime
	if r := m.runtimeFor(agentID); r != nil {
		client = r.warmClient()
		rt = r
	}
	if client == nil {
		_ = m.store.UpdateAgentStatus(ctx, agentID, "starting", "")
		m.Sink().AgentState(agentID, "starting", "")
		procCfg, err := acpProcessConfig(agent, m.log.With("agent", agent.Slug))
		if err != nil {
			_ = m.store.UpdateAgentStatus(ctx, agentID, "error", err.Error())
			m.Sink().AgentState(agentID, "error", err.Error())
			return ProbeResult{}, err
		}
		caps := acp.DefaultClientCapabilities()
		caps.Elicitation = nil
		client, err = acp.Start(ctx, acp.ClientConfig{
			ProcessConfig: procCfg,
			ClientInfo:    acp.Implementation{Name: "nusamation", Title: "NusaMation", Version: config.Version},
			Capabilities:  &caps,
		}, probeHandler{})
		if err != nil {
			_ = m.store.UpdateAgentStatus(ctx, agentID, "error", err.Error())
			m.Sink().AgentState(agentID, "error", err.Error())
			return ProbeResult{}, err
		}
		defer client.Close()
	}

	sessionCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	sess, err := client.NewSession(sessionCtx, acp.NewSessionRequest{Cwd: agent.Cwd, MCPServers: []acp.MCPServerConfig{}})
	if err != nil {
		_ = m.store.UpdateAgentStatus(ctx, agentID, "error", err.Error())
		m.Sink().AgentState(agentID, "error", err.Error())
		return ProbeResult{}, err
	}
	if client.SupportsClose() {
		_ = client.CloseSession(context.Background(), sess.SessionID)
	}
	if rt != nil {
		rt.discardPending(sess.SessionID)
	}
	agentCaps, _ := json.Marshal(client.Capabilities().AgentCapabilities)
	auths, _ := json.Marshal(client.AuthMethods())
	configs := sess.ConfigOptions
	if len(configs) == 0 {
		configs = json.RawMessage("[]")
	}
	models := store.ExtractModels(configs)
	res := ProbeResult{
		AgentInfo:     client.AgentInfo(),
		Capabilities:  agentCaps,
		AuthMethods:   auths,
		ConfigOptions: configs,
		Models:        models,
		Protocol:      client.Capabilities().ProtocolVersion,
		DurationMS:    time.Since(start).Milliseconds(),
	}
	if err := m.store.UpdateAgentProbe(ctx, agentID, "ready", "", models, configs, agentCaps, auths); err != nil {
		return res, err
	}
	m.Sink().AgentState(agentID, "ready", "")
	return res, nil
}

func envMap(env store.SecretList) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		if e.Name != "" {
			out[e.Name] = e.Value
		}
	}
	return out
}

func mcpConfigs(list []store.MCPServer, caps acp.AgentCapabilities) []acp.MCPServerConfig {
	var out []acp.MCPServerConfig
	for _, s := range list {
		if !s.Enabled {
			continue
		}
		cfg := acp.MCPServerConfig{Name: s.Name, Type: s.Transport}
		switch s.Transport {
		case "http":
			if !caps.MCPCapabilities.HTTP {
				continue
			}
			cfg.URL = s.URL
			for _, h := range s.Headers {
				cfg.Headers = append(cfg.Headers, acp.EnvVar{Name: h.Name, Value: h.Value})
			}
		case "sse":
			if !caps.MCPCapabilities.SSE {
				continue
			}
			cfg.URL = s.URL
			for _, h := range s.Headers {
				cfg.Headers = append(cfg.Headers, acp.EnvVar{Name: h.Name, Value: h.Value})
			}
		default:
			cfg.Type = ""
			cfg.Command = s.Command
			cfg.Args = s.Args
			for _, e := range s.Env {
				cfg.Env = append(cfg.Env, acp.EnvVar{Name: e.Name, Value: e.Value})
			}
		}
		out = append(out, cfg)
	}
	return out
}

func sessionConfigs(raw json.RawMessage, category string) (configID string, current any, choices []string) {
	var opts []struct {
		ID       string          `json:"id"`
		Category string          `json:"category"`
		Type     string          `json:"type"`
		Current  json.RawMessage `json:"currentValue"`
		Options  json.RawMessage `json:"options"`
	}
	if json.Unmarshal(raw, &opts) != nil {
		return "", nil, nil
	}
	for _, o := range opts {
		if o.Category != category {
			continue
		}
		configID = o.ID
		_ = json.Unmarshal(o.Current, &current)
		var flat []struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(o.Options, &flat) == nil {
			for _, f := range flat {
				if f.Value != "" {
					choices = append(choices, f.Value)
				}
			}
		}
		if len(choices) == 0 {
			var groups []struct {
				Options []struct {
					Value string `json:"value"`
				} `json:"options"`
			}
			if json.Unmarshal(o.Options, &groups) == nil {
				for _, g := range groups {
					for _, f := range g.Options {
						choices = append(choices, f.Value)
					}
				}
			}
		}
		return
	}
	return
}

func findConfigCurrent(raw json.RawMessage, id string) any {
	var opts []struct {
		ID      string          `json:"id"`
		Current json.RawMessage `json:"currentValue"`
	}
	if json.Unmarshal(raw, &opts) != nil {
		return nil
	}
	for _, o := range opts {
		if o.ID == id {
			var v any
			_ = json.Unmarshal(o.Current, &v)
			return v
		}
	}
	return nil
}

func anyEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func AgentStatusForError(err error) (status, msg string) {
	if err == nil {
		return "ready", ""
	}
	if acp.IsAuthRequired(err) {
		return "needs_auth", err.Error()
	}
	return "error", err.Error()
}

func trimErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 2000 {
		s = s[:2000]
	}
	return s
}

func joinCommand(cmd string, args []string) string {
	return strings.TrimSpace(cmd + " " + strings.Join(args, " "))
}
