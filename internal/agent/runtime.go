package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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
	stderr   *acp.Ring

	previewMu  sync.Mutex
	preview    *previewState
	previewIDs map[string]struct{}
}

func (r *Runtime) Info() (string, int, time.Duration, []string) {
	r.mu.Lock()
	c := r.client
	state := r.state
	var logs []string
	if r.stderr != nil {
		logs = r.stderr.Lines()
	}
	r.mu.Unlock()
	if c == nil || !c.Alive() {
		return state, 0, 0, logs
	}
	return state, c.PID(), c.Uptime(), logs
}

func (r *Runtime) appendStderr(line string) {
	r.mu.Lock()
	if r.stderr == nil {
		r.stderr = acp.NewRing(400)
	}
	r.stderr.Add(line)
	r.mu.Unlock()
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
		if r.stopping {
			r.mu.Unlock()
			return nil, errors.New("agent is stopped")
		}
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
		procCfg, err := acpProcessConfig(r.agent, r.log)
		if err != nil {
			r.mu.Lock()
			r.starting = false
			close(r.startCh)
			r.startCh = nil
			stopping := r.stopping
			r.mu.Unlock()
			if !stopping {
				r.failStart(err)
			}
			return nil, err
		}
		startCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		client, err := acp.Start(startCtx, acp.ClientConfig{
			ProcessConfig: procCfg,
			ClientInfo:    acp.Implementation{Name: "nusamation", Title: "NusaMation", Version: config.Version},
		}, &runtimeHandler{r: r})
		cancel()

		r.mu.Lock()
		r.starting = false
		close(r.startCh)
		r.startCh = nil
		stopping := r.stopping
		if err == nil && !stopping {
			r.client = client
			r.state = "ready"
			r.lastErr = ""
		}
		r.mu.Unlock()
		if err != nil {
			if !stopping {
				r.failStart(err)
			}
			return nil, err
		}
		if stopping {
			// Stop/InvalidateAgent discarded this runtime while the process
			// was starting; shut the client down instead of installing it on
			// a runtime nobody owns anymore.
			_ = client.Close()
			return nil, errors.New("agent is stopped")
		}
		_ = r.mgr.store.UpdateAgentStatus(context.Background(), r.agent.ID, "ready", "")
		r.mgr.Sink().AgentState(r.agent.ID, "ready", "")
		return client, nil
	}
}

// failStart records a launch that never became a live ACP client. The runtime
// modal reads these lines after the process is already gone.
func (r *Runtime) failStart(err error) {
	msg := trimErr(err)
	r.mu.Lock()
	empty := r.stderr == nil || len(r.stderr.Lines()) == 0
	r.mu.Unlock()
	if empty && msg != "" {
		r.appendStderr(msg)
	}
	r.setState("error", msg)
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
	ctx = acp.WithRequestOwner(ctx, s.ConversationID())
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	if s.isLoaded() {
		return s, nil
	}
	client, err := r.ensureClient(ctx)
	if err != nil {
		return s, err
	}
	mcpList, err := r.mgr.store.ListMCP(ctx)
	if err != nil {
		return s, err
	}
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
	if state.Modes != nil {
		s.modes = state.Modes
	}
	switch {
	case len(state.ConfigOptions) > 0:
		s.setConfigsLocked(state.ConfigOptions)
	case len(s.configOptions) == 0 && len(req.CachedConfig) > 0:
		s.setConfigsLocked(req.CachedConfig)
	default:
		s.setConfigsLocked(s.configOptions)
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
	modelID, currentModel, models := sessionConfigs(configs, "model")
	modelDefault := false
	for _, choice := range req.DefaultConfig {
		if modelID != "" && modelID == choice.ID {
			modelDefault = true
		}
	}
	if !modelDefault && modelID != "" {
		model := req.DesiredModel
		if override, ok := req.DesiredConfig[modelID].(string); ok {
			model = override
		}
		if model != "" && !anyEqual(currentModel, model) && contains(models, model) {
			if err := r.applyConfig(ctx, client, s, modelID, model); err != nil {
				return fmt.Errorf("select model %q: %w", model, err)
			}
			configs = s.Config()
		}
	}
	// A later choice can reset an earlier one (e.g. a model id that implies an
	// effort level), so re-apply selections whose value no longer holds until
	// the state settles; the check below still rejects irreconcilable orders.
	for round := 0; round <= len(req.DefaultConfig); round++ {
		settled := true
		for _, choice := range req.DefaultConfig {
			value := choice.Value
			if override, ok := req.DesiredConfig[choice.ID]; ok {
				value = override
			}
			if !configAccepts(configs, choice.ID, value) {
				if round == 0 {
					return fmt.Errorf("default option %q or its value is no longer available", choice.ID)
				}
				continue
			}
			if !anyEqual(findConfigCurrent(configs, choice.ID), value) {
				if err := r.applyConfig(ctx, client, s, choice.ID, value); err != nil {
					return fmt.Errorf("select %s: %w", choice.ID, err)
				}
				configs = s.Config()
				settled = false
			}
		}
		if settled {
			break
		}
	}
	configs = s.Config()
	for _, choice := range req.DefaultConfig {
		value := choice.Value
		if override, ok := req.DesiredConfig[choice.ID]; ok {
			value = override
		}
		if !configAccepts(configs, choice.ID, value) || !anyEqual(findConfigCurrent(configs, choice.ID), value) {
			return fmt.Errorf("default option %q changed or became unavailable after a later setting", choice.ID)
		}
	}
	for id, value := range req.DesiredConfig {
		if id == "" || value == nil {
			continue
		}
		cur := findConfigCurrent(configs, id)
		if cur == nil || anyEqual(cur, value) || !configAccepts(configs, id, value) {
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
	return slices.Contains(list, v)
}

func (r *Runtime) applyConfig(ctx context.Context, client *acp.Client, s *Session, id string, value any) error {
	cfgCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	configs, err := r.setSessionConfig(cfgCtx, client, s, id, value)
	if err != nil {
		return err
	}
	r.mgr.Sink().SessionConfigUpdated(s, configs)
	_ = r.mgr.store.UpdateAgentConfigCache(context.Background(), r.agent.ID, configs)
	return nil
}

func (r *Runtime) setSessionConfig(ctx context.Context, client *acp.Client, s *Session, configID string, value any) (json.RawMessage, error) {
	s.mu.Lock()
	legacy := s.legacyMode && configID == legacyModeID
	s.mu.Unlock()
	if legacy {
		modeID, ok := value.(string)
		if !ok || modeID == "" {
			return nil, fmt.Errorf("mode must be one of the agent's modes")
		}
		if err := client.SetMode(ctx, s.ACPID(), modeID); err != nil {
			return nil, err
		}
		s.applyAgentMode(modeID)
		return s.Config(), nil
	}
	res, err := client.SetConfig(ctx, s.ACPID(), configID, value)
	if err != nil {
		return nil, err
	}
	if len(res.ConfigOptions) > 0 {
		s.mu.Lock()
		s.setConfigsLocked(res.ConfigOptions)
		s.mu.Unlock()
	}
	return s.Config(), nil
}

const legacyModeID = "mode"

func mergeLegacyModes(raw json.RawMessage, modes *acp.SessionModeState) (json.RawMessage, bool) {
	var list []map[string]any
	if len(raw) > 0 && json.Unmarshal(raw, &list) != nil {
		return raw, false
	}
	kept := make([]map[string]any, 0, len(list)+1)
	var prior map[string]any
	hasMode, hasID := false, false
	for _, item := range list {
		if isLegacyModeItem(item) {
			prior = item
			continue
		}
		if id, _ := item["id"].(string); id == legacyModeID {
			hasID = true
		}
		if cat, _ := item["category"].(string); strings.EqualFold(cat, "mode") {
			hasMode = true
		}
		kept = append(kept, item)
	}
	var legacy map[string]any
	switch {
	case hasMode || hasID:
	case modes != nil && len(modes.AvailableModes) > 0:
		legacy = legacyModeOption(modes)
	case prior != nil:
		legacy = prior
	}
	if legacy != nil {
		kept = append([]map[string]any{legacy}, kept...)
	}
	if len(kept) == 0 {
		if len(list) == 0 {
			return raw, false
		}
		return nil, false
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return raw, false
	}
	return out, legacy != nil
}

func legacyModeOption(modes *acp.SessionModeState) map[string]any {
	choices := make([]map[string]any, 0, len(modes.AvailableModes))
	for _, m := range modes.AvailableModes {
		choice := map[string]any{"value": m.ID, "name": m.Name}
		if m.Description != "" {
			choice["description"] = m.Description
		}
		choices = append(choices, choice)
	}
	return map[string]any{
		"id": legacyModeID, "name": "Mode", "category": "mode", "type": "select",
		"currentValue": modes.CurrentModeID, "options": choices,
		"_meta": map[string]any{"nusamation": map[string]any{"legacyModes": true}},
	}
}

func isLegacyModeItem(item map[string]any) bool {
	meta, _ := item["_meta"].(map[string]any)
	ns, _ := meta["nusamation"].(map[string]any)
	return ns["legacyModes"] == true
}

func setModeValue(raw json.RawMessage, modeID string) (json.RawMessage, bool) {
	var list []map[string]any
	if json.Unmarshal(raw, &list) != nil {
		return raw, false
	}
	found := false
	for _, item := range list {
		if cat, _ := item["category"].(string); strings.EqualFold(cat, "mode") {
			item["currentValue"] = modeID
			found = true
		}
	}
	if !found {
		return raw, false
	}
	out, err := json.Marshal(list)
	if err != nil {
		return raw, false
	}
	return out, true
}

func (s *Session) setConfigsLocked(raw json.RawMessage) {
	s.configOptions, s.legacyMode = mergeLegacyModes(raw, s.modes)
}

func (s *Session) applyAgentMode(modeID string) bool {
	if modeID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modes != nil {
		s.modes.CurrentModeID = modeID
	}
	updated, ok := setModeValue(s.configOptions, modeID)
	if ok {
		s.configOptions = updated
	}
	return ok
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
	ctx = acp.WithRequestOwner(ctx, s.ConversationID())
	client, err := r.ensureClient(ctx)
	if err != nil {
		return acp.PromptResult{}, err
	}
	s.touch()
	result, err := client.Prompt(ctx, s.ACPID(), blocks)
	if acp.IsAuthRequired(err) {
		r.setState("needs_auth", trimErr(err))
		return result, &AuthRequiredError{Methods: client.AuthMethods(), Err: err}
	}
	return result, err
}

// Cancel notifies a live agent process only; it never starts one.
func (r *Runtime) Cancel(_ context.Context, s *Session) error {
	r.mu.Lock()
	client := r.client
	r.mu.Unlock()
	if client == nil || !client.Alive() {
		return errors.New("agent is not running")
	}
	return client.Cancel(s.ACPID())
}

func (r *Runtime) SetConfig(ctx context.Context, s *Session, configID string, value any) (json.RawMessage, error) {
	ctx = acp.WithRequestOwner(ctx, s.ConversationID())
	client, err := r.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	configs, err := r.setSessionConfig(ctx, client, s, configID, value)
	if err != nil {
		return nil, err
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
	for _, method := range client.AuthMethods() {
		if method.ID == methodID && method.Type == "terminal" {
			return errors.New("this authentication method requires an interactive terminal, which is not available in the web UI")
		}
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
		r.previewMu.Lock()
		if _, ok := r.previewIDs[sessionID]; ok {
			// The preview session has no chat sink; fold pushed catalog and
			// mode updates into its tracked state so the next replay does not
			// validate against a stale copy.
			if p := r.preview; p != nil && p.sessionID == sessionID {
				var u acp.SessionUpdate
				if json.Unmarshal(raw, &u) == nil {
					switch u.SessionUpdate {
					case "config_option_update":
						if len(u.ConfigOptions) > 0 {
							p.configs, p.legacy = mergeLegacyModes(u.ConfigOptions, p.modes)
						}
					case "current_mode_update":
						if p.legacy {
							p.configs, _ = setModeValue(p.configs, u.CurrentModeID)
						}
						if p.modes != nil {
							p.modes.CurrentModeID = u.CurrentModeID
						}
					}
				}
			}
			r.previewMu.Unlock()
			return
		}
		r.previewMu.Unlock()
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
			s.setConfigsLocked(u.ConfigOptions)
			configs := append(json.RawMessage(nil), s.configOptions...)
			s.mu.Unlock()
			r.mgr.Sink().SessionConfigUpdated(s, configs)
			_ = r.mgr.store.UpdateAgentConfigCache(context.Background(), r.agent.ID, configs)
		}
		return
	case "current_mode_update":
		// Mode is also reflected in config options on modern agents.
		if s.applyAgentMode(u.CurrentModeID) {
			s.mu.Lock()
			configs := append(json.RawMessage(nil), s.configOptions...)
			s.mu.Unlock()
			r.mgr.Sink().SessionConfigUpdated(s, configs)
			_ = r.mgr.store.UpdateAgentConfigCache(context.Background(), r.agent.ID, configs)
		}
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
	h.r.appendStderr(line)
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
		return h.r.mgr.terminals.Handle(ctx, method, params, s.Roots(), func(update TerminalUpdate) {
			h.r.mgr.Sink().TerminalUpdate(s, update)
		})
	case "elicitation/create":
		var req struct {
			SessionID string `json:"sessionId"`
			Mode      string `json:"mode"`
		}
		if json.Unmarshal(params, &req) != nil || (req.Mode != "form" && req.Mode != "url") {
			return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "unsupported elicitation mode"}
		}
		s := h.r.findSession(req.SessionID)
		if s == nil && acp.RequestOwner(ctx) != "" {
			h.r.mu.Lock()
			s = h.r.sessions[acp.RequestOwner(ctx)]
			h.r.mu.Unlock()
		}
		if s == nil {
			return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "unknown elicitation session or request"}
		}
		response, err := h.r.mgr.Sink().Elicit(ctx, s, params)
		if err != nil {
			return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
		}
		return response, nil
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
	path, err := resolveClientPath(s, req.Path, false)
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
		start = min(*req.Line-1, len(lines))
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
	path, err := resolveClientPath(s, req.Path, true)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: err.Error()}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	if err := os.WriteFile(path, []byte(req.Content), 0o644); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	return map[string]any{}, nil
}

// Bypass Permissions auto-approves every tool call, so the client filesystem
// must not add a second workspace-root rejection on top of that mode.
func (s *Session) bypassesPermissions() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modes != nil && isBypassMode(s.modes.CurrentModeID) {
		return true
	}
	return isBypassMode(configModeID(s.configOptions))
}

func isBypassMode(id string) bool {
	return strings.EqualFold(strings.TrimSpace(id), "bypass")
}

func configModeID(raw json.RawMessage) string {
	var list []map[string]any
	if json.Unmarshal(raw, &list) != nil {
		return ""
	}
	for _, item := range list {
		category, _ := item["category"].(string)
		id, _ := item["id"].(string)
		if !strings.EqualFold(category, "mode") && id != "mode" {
			continue
		}
		if value, ok := item["currentValue"].(string); ok {
			return value
		}
	}
	return ""
}

func resolveClientPath(s *Session, p string, write bool) (string, error) {
	var abs string
	var err error
	if s.bypassesPermissions() {
		if !filepath.IsAbs(p) {
			return "", errors.New("path must be absolute")
		}
		abs = filepath.Clean(p)
	} else {
		abs, err = securityPath(s.Roots(), p)
		if err != nil {
			return "", err
		}
	}
	if write && sensitivePath(abs) {
		return "", errors.New("writing execution-control or secret files requires manual approval outside ACP")
	}
	return abs, nil
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
