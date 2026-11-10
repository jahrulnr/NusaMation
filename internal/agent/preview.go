package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/store"
)

// errNoCatalog marks an agent without a usable cached option catalog, so the
// caller falls back to spawning a probe process.
var errNoCatalog = errors.New("no cached config catalog")

// previewState is the runtime's reusable "idle" ACP session. It exists only to
// answer catalog/config previews without paying a process spawn per request.
type previewState struct {
	client    *acp.Client
	sessionID string
	base      json.RawMessage // merged catalog as returned by session/new
	configs   json.RawMessage // current merged catalog after applied selections
	modes     *acp.SessionModeState
	legacy    bool
	applied   []store.ConfigSelection // effective selections applied so far
}

// selectionSession is the live ACP session a selection replay runs against —
// the throwaway probe session or a runtime's reusable preview session.
type selectionSession struct {
	client    *acp.Client
	sessionID string
	modes     *acp.SessionModeState
	configs   json.RawMessage
	legacy    bool
}

func (s *selectionSession) apply(ctx context.Context, choice store.ConfigSelection) error {
	if s.legacy && choice.ID == legacyModeID {
		modeID, ok := choice.Value.(string)
		if !ok {
			return fmt.Errorf("option %q expects a string value", choice.ID)
		}
		if err := s.client.SetMode(ctx, s.sessionID, modeID); err != nil {
			return err
		}
		s.configs, _ = setModeValue(s.configs, modeID)
		if s.modes != nil {
			s.modes.CurrentModeID = modeID
		}
		return nil
	}
	result, err := s.client.SetConfig(ctx, s.sessionID, choice.ID, choice.Value)
	if err != nil {
		return fmt.Errorf("set %s: %w", choice.ID, err)
	}
	s.configs, s.legacy = mergeLegacyModes(result.ConfigOptions, s.modes)
	return nil
}

// replaySelections validates and applies effective[appliedCount:] on the
// session — earlier entries only count toward duplicate detection because they
// were already applied. It then re-applies any verify selection whose value no
// longer holds (a later choice can reset an earlier one) and finally requires
// every verify selection to still hold, in order.
func replaySelections(ctx context.Context, s *selectionSession, appliedCount int, effective, verify []store.ConfigSelection) error {
	seen := make(map[string]bool, len(effective))
	for i, choice := range effective {
		if i < appliedCount {
			seen[choice.ID] = true
			continue
		}
		if seen[choice.ID] || !configAccepts(s.configs, choice.ID, choice.Value) {
			return fmt.Errorf("option %q or its value is not available in the current configuration", choice.ID)
		}
		seen[choice.ID] = true
		if anyEqual(findConfigCurrent(s.configs, choice.ID), choice.Value) {
			continue
		}
		if err := s.apply(ctx, choice); err != nil {
			return err
		}
	}
	for round := 0; round <= len(verify); round++ {
		settled := true
		for _, choice := range verify {
			if !configAccepts(s.configs, choice.ID, choice.Value) || anyEqual(findConfigCurrent(s.configs, choice.ID), choice.Value) {
				continue
			}
			if err := s.apply(ctx, choice); err != nil {
				return err
			}
			settled = false
		}
		if settled {
			break
		}
	}
	for _, choice := range verify {
		if !configAccepts(s.configs, choice.ID, choice.Value) || !anyEqual(findConfigCurrent(s.configs, choice.ID), choice.Value) {
			return fmt.Errorf("option %q was changed or became unavailable after a later choice; change the selection order", choice.ID)
		}
	}
	return nil
}

// effectiveSelections prepends the agent's default model when the user's
// selections do not pick a model themselves, so dependent options (e.g. an
// effort list that follows the model) resolve exactly like a live session.
func effectiveSelections(base json.RawMessage, ag store.Agent, selections []store.ConfigSelection) []store.ConfigSelection {
	modelID, _, _ := sessionConfigs(base, "model")
	hasModelChoice := false
	for _, choice := range selections {
		if choice.ID == modelID && modelID != "" {
			hasModelChoice = true
		}
	}
	if !hasModelChoice && ag.DefaultModel != "" && configAccepts(base, modelID, ag.DefaultModel) {
		return append([]store.ConfigSelection{{ID: modelID, Value: ag.DefaultModel}}, selections...)
	}
	return selections
}

// selectionsPrefix reports whether applied is a strict prefix of next, which
// lets a reused preview session apply only the newly appended choices.
func selectionsPrefix(applied, next []store.ConfigSelection) bool {
	if len(applied) > len(next) {
		return false
	}
	for i, choice := range applied {
		if next[i].ID != choice.ID || !anyEqual(next[i].Value, choice.Value) {
			return false
		}
	}
	return true
}

func setConfigCurrent(raw json.RawMessage, configID string, value any) json.RawMessage {
	var list []map[string]any
	if json.Unmarshal(raw, &list) != nil {
		return raw
	}
	for _, item := range list {
		if item["id"] == configID {
			item["currentValue"] = value
		}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return raw
	}
	return b
}

// previewSelectionsCached merges selections into the agent's last known
// catalog without touching the agent process. Options that change based on an
// earlier choice keep their cached values until a real session refreshes them;
// callers should fall back to a live probe when this returns an error.
func previewSelectionsCached(ag store.Agent, selections []store.ConfigSelection) (json.RawMessage, error) {
	configs, legacy := mergeLegacyModes(ag.ConfigOptions, nil)
	if len(configs) == 0 || string(configs) == "[]" {
		return nil, errNoCatalog
	}
	seen := make(map[string]bool, len(selections)+1)
	for _, choice := range effectiveSelections(configs, ag, selections) {
		if seen[choice.ID] || !configAccepts(configs, choice.ID, choice.Value) {
			return nil, fmt.Errorf("option %q or its value is not available in the current configuration", choice.ID)
		}
		seen[choice.ID] = true
		if legacy && choice.ID == legacyModeID {
			modeID, ok := choice.Value.(string)
			if !ok {
				return nil, fmt.Errorf("option %q expects a string value", choice.ID)
			}
			if updated, ok := setModeValue(configs, modeID); ok {
				configs = updated
			}
			continue
		}
		configs = setConfigCurrent(configs, choice.ID, choice.Value)
	}
	return configs, nil
}

// warmClient returns the runtime's live ACP client, if the agent process is
// up. A nil result means the agent is cold and previews should use the cached
// catalog instead of spawning.
func (r *Runtime) warmClient() *acp.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil && r.client.Alive() {
		r.lastActivity = time.Now()
		return r.client
	}
	return nil
}

// waitClient returns the live client, waiting only while a start is already
// in flight (warm-up or session prepare). It never spawns a process itself —
// a cold runtime yields nil so the caller can use the cached catalog instead
// of paying a cold launch.
func (r *Runtime) waitClient(ctx context.Context) (*acp.Client, error) {
	for {
		r.mu.Lock()
		if r.client != nil && r.client.Alive() {
			c := r.client
			r.lastActivity = time.Now()
			r.mu.Unlock()
			return c, nil
		}
		if !r.starting {
			r.mu.Unlock()
			return nil, nil
		}
		ch := r.startCh
		r.mu.Unlock()
		select {
		case <-ch:
			continue
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// registerPreview marks sessionID as a preview session. Callers hold
// previewMu; previewIDs is guarded by previewMu, pending stays under r.mu.
func (r *Runtime) registerPreview(sessionID string) {
	if r.previewIDs == nil {
		r.previewIDs = map[string]struct{}{}
	}
	r.previewIDs[sessionID] = struct{}{}
	// Updates emitted before the session id was registered sit in the pending
	// buffer; preview sessions never drain it.
	r.mu.Lock()
	delete(r.pending, sessionID)
	r.mu.Unlock()
}

func (r *Runtime) unregisterPreview(sessionID string) {
	delete(r.previewIDs, sessionID)
	r.mu.Lock()
	delete(r.pending, sessionID)
	r.mu.Unlock()
}

// discardPending drops buffered session/update notifications for a session
// that is consumed synchronously (probe, preview) and never drained.
func (r *Runtime) discardPending(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, sessionID)
}

// dropPreviewLocked closes and forgets the reusable preview session. Callers
// hold previewMu. The close is best-effort: the session may already be gone
// (unknown-session errors, process restart).
func (r *Runtime) dropPreviewLocked(ctx context.Context, client *acp.Client) {
	old := r.preview
	if old == nil {
		return
	}
	r.unregisterPreview(old.sessionID)
	r.preview = nil
	if old.client == client {
		closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = client.DiscardSession(closeCtx, old.sessionID)
		cancel()
	}
}

// newPreviewSession creates the runtime's reusable idle session on an already
// warm client. Callers hold previewMu.
func (r *Runtime) newPreviewSession(ctx context.Context, client *acp.Client, ag store.Agent) error {
	r.dropPreviewLocked(ctx, client)
	servers, err := r.mgr.store.ListMCP(ctx)
	if err != nil {
		return err
	}
	sessCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	state, err := client.NewSession(sessCtx, acp.NewSessionRequest{
		Cwd: ag.Cwd, MCPServers: mcpConfigs(servers, client.Capabilities().AgentCapabilities),
	})
	cancel()
	if err != nil {
		return err
	}
	configs, legacy := mergeLegacyModes(state.ConfigOptions, state.Modes)
	if len(configs) == 0 {
		configs = json.RawMessage("[]")
	}
	r.preview = &previewState{
		client: client, sessionID: state.SessionID, base: configs, configs: configs,
		modes: state.Modes, legacy: legacy,
	}
	r.registerPreview(state.SessionID)
	// A fresh session is also the freshest catalog; persist it so cold agents
	// can preview from cache and the picker model list stays current.
	_ = r.mgr.store.UpdateAgentConfigCache(context.Background(), ag.ID, configs)
	return nil
}

// previewSelections replays selections on the runtime's reusable idle session.
// Extending the previous selection list only applies the appended choices; a
// diverging list recreates the session so replay always starts from a clean
// catalog — in both cases the agent process is never respawned.
func (r *Runtime) previewSelections(ctx context.Context, client *acp.Client, ag store.Agent, selections []store.ConfigSelection) (json.RawMessage, error) {
	done := r.beginWork()
	defer done()
	r.previewMu.Lock()
	defer r.previewMu.Unlock()
	if r.preview == nil || r.preview.client != client {
		if err := r.newPreviewSession(ctx, client, ag); err != nil {
			return nil, err
		}
	}
	p := r.preview
	effective := effectiveSelections(p.base, ag, selections)
	applied := len(p.applied)
	if !selectionsPrefix(p.applied, effective) {
		if err := r.newPreviewSession(ctx, client, ag); err != nil {
			return nil, err
		}
		p = r.preview
		effective = effectiveSelections(p.base, ag, selections)
		applied = 0
	}
	s := &selectionSession{
		client: client, sessionID: p.sessionID, modes: p.modes,
		configs: p.configs, legacy: p.legacy,
	}
	if err := replaySelections(ctx, s, applied, effective, selections); err != nil {
		// A mid-sequence apply may have partially succeeded, leaving the
		// session ahead of p.applied. Drop it so the next preview rebuilds
		// from a clean catalog instead of extending a drifted session.
		r.dropPreviewLocked(ctx, client)
		return nil, err
	}
	p.configs = s.configs
	p.legacy = s.legacy
	p.applied = effective
	return append(json.RawMessage(nil), p.configs...), nil
}

// warm brings the agent process up and refreshes the idle preview session so
// composer previews answer without waiting for session/new. The session is
// rebuilt on every call — warms follow lifecycle events (boot, update, auth,
// MCP change) after which the advertised catalog may have changed.
func (r *Runtime) warm(ctx context.Context) {
	done := r.beginWork()
	defer done()
	client, err := r.ensureClient(ctx)
	if err != nil {
		r.log.Warn("idle agent failed to start", "agent", r.agent.Slug, "error", err)
		return
	}
	r.previewMu.Lock()
	defer r.previewMu.Unlock()
	if err := r.newPreviewSession(ctx, client, r.agent); err != nil {
		r.log.Warn("idle preview session failed", "agent", r.agent.Slug, "error", err)
	}
}
