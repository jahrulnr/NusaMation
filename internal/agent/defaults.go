package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/store"
)

// configAccepts checks the current advertised choices, including grouped choices.
func configAccepts(raw json.RawMessage, id string, value any) bool {
	var options []struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Options []struct {
			Value   string `json:"value"`
			Options []struct {
				Value string `json:"value"`
			} `json:"options"`
		} `json:"options"`
	}
	if json.Unmarshal(raw, &options) != nil {
		return false
	}
	for _, o := range options {
		if o.ID != id {
			continue
		}
		if strings.EqualFold(o.Type, "boolean") {
			_, ok := value.(bool)
			return ok
		}
		v, ok := value.(string)
		if !ok || !strings.EqualFold(o.Type, "select") {
			return false
		}
		for _, c := range o.Options {
			if c.Value == v && len(c.Options) == 0 {
				return true
			}
			for _, nested := range c.Options {
				if nested.Value == v {
					return true
				}
			}
		}
	}
	return false
}

// PreviewDefaults replays choices in order against the agent's option catalog
// and returns the resulting configuration. A running agent answers on its
// reusable preview session (any live runtime qualifies — not only idle-mode
// agents); a cold agent merges into the cached catalog when one exists;
// otherwise a throwaway probe session does the replay and seeds the cache.
func (m *Manager) PreviewDefaults(ctx context.Context, agentID string, selections []store.ConfigSelection) (json.RawMessage, error) {
	return m.previewDefaults(ctx, agentID, selections, true)
}

// ValidateDefaults is PreviewDefaults for the save path. It skips the
// approximate cached catalog — the cache is a "last seen" snapshot that real
// sessions also rewrite post-selection — so stored defaults are always proven
// against a live session (warm preview session or throwaway probe).
func (m *Manager) ValidateDefaults(ctx context.Context, agentID string, selections []store.ConfigSelection) (json.RawMessage, error) {
	return m.previewDefaults(ctx, agentID, selections, false)
}

func (m *Manager) previewDefaults(ctx context.Context, agentID string, selections []store.ConfigSelection, allowCache bool) (json.RawMessage, error) {
	ag, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if !ag.Enabled {
		return nil, fmt.Errorf("agent %q is disabled", ag.Name)
	}
	if r := m.runtimeFor(agentID); r != nil {
		// waitClient joins an in-flight start (warm-up or session prepare)
		// instead of spawning a parallel probe process.
		client, err := r.waitClient(ctx)
		if err != nil {
			return nil, err
		}
		if client != nil {
			return r.previewSelections(ctx, client, ag, selections)
		}
	}
	if allowCache {
		configs, err := previewSelectionsCached(ag, selections)
		if err == nil {
			return configs, nil
		}
		if !errors.Is(err, errNoCatalog) {
			m.log.Debug("cached catalog rejected selections; probing live", "agent", ag.Slug, "error", err)
		}
	}
	return m.previewDefaultsLive(ctx, ag, selections)
}

// previewDefaultsLive spawns a short-lived probe session for the replay — the
// slow path, used when there is no warm runtime or usable cached catalog.
func (m *Manager) previewDefaultsLive(ctx context.Context, ag store.Agent, selections []store.ConfigSelection) (json.RawMessage, error) {
	caps := acp.DefaultClientCapabilities()
	caps.Elicitation = nil
	procCfg, err := acpProcessConfig(ag, m.log.With("agent", ag.Slug))
	if err != nil {
		return nil, err
	}
	client, err := acp.Start(ctx, acp.ClientConfig{
		ProcessConfig: procCfg,
		ClientInfo:    acp.Implementation{Name: "nusamation", Title: "NusaMation", Version: config.Version}, Capabilities: &caps,
	}, probeHandler{})
	if err != nil {
		return nil, err
	}
	defer client.Close()
	servers, err := m.store.ListMCP(ctx)
	if err != nil {
		return nil, err
	}
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: ag.Cwd, MCPServers: mcpConfigs(servers, client.Capabilities().AgentCapabilities)})
	if err != nil {
		return nil, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.DiscardSession(closeCtx, session.SessionID)
	}()
	configs, legacy := mergeLegacyModes(session.ConfigOptions, session.Modes)
	if len(configs) == 0 {
		configs = json.RawMessage("[]")
	}
	s := &selectionSession{
		client: client, sessionID: session.SessionID, modes: session.Modes,
		configs: configs, legacy: legacy,
	}
	if err := replaySelections(ctx, s, 0, effectiveSelections(configs, ag, selections), selections); err != nil {
		return nil, err
	}
	// Cache the fresh-session catalog, not the post-selection result — a cached
	// baseline polluted by the replayed values would wrongly validate dependent
	// options the next preview does not re-select.
	_ = m.store.UpdateAgentConfigCache(ctx, ag.ID, configs)
	return s.configs, nil
}
