package agent

import (
	"context"
	"encoding/json"
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

// PreviewDefaults creates an isolated ACP session and replays choices in order.
// Each choice is validated against the options returned by the preceding one.
func (m *Manager) PreviewDefaults(ctx context.Context, agentID string, selections []store.ConfigSelection) (json.RawMessage, error) {
	ag, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if !ag.Enabled {
		return nil, fmt.Errorf("agent %q is disabled", ag.Name)
	}
	caps := acp.DefaultClientCapabilities()
	caps.Elicitation = nil
	client, err := acp.Start(ctx, acp.ClientConfig{
		ProcessConfig: acp.ProcessConfig{Command: ag.Command, Args: ag.Args, Env: envMap(ag.Env), Dir: ag.Cwd, Log: m.log.With("agent", ag.Slug)},
		ClientInfo:    acp.Implementation{Name: "nusamation", Title: "NusaMation", Version: config.Version}, Capabilities: &caps,
	}, probeHandler{})
	if err != nil {
		return nil, err
	}
	defer client.Close()
	servers, err := m.store.ListMCP(ctx, agentID)
	if err != nil {
		return nil, err
	}
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: ag.Cwd, MCPServers: mcpConfigs(servers, client.Capabilities().AgentCapabilities)})
	if err != nil {
		return nil, err
	}
	if client.SupportsClose() {
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = client.CloseSession(closeCtx, session.SessionID)
		}()
	}
	configs, legacy := mergeLegacyModes(session.ConfigOptions, session.Modes)
	if len(configs) == 0 {
		configs = json.RawMessage("[]")
	}
	modelID, _, _ := sessionConfigs(configs, "model")
	hasModelChoice := false
	for _, choice := range selections {
		if choice.ID == modelID && modelID != "" {
			hasModelChoice = true
		}
	}
	if !hasModelChoice && ag.DefaultModel != "" && configAccepts(configs, modelID, ag.DefaultModel) {
		result, err := client.SetConfig(ctx, session.SessionID, modelID, ag.DefaultModel)
		if err != nil {
			return nil, fmt.Errorf("select model %q: %w", ag.DefaultModel, err)
		}
		configs, legacy = mergeLegacyModes(result.ConfigOptions, session.Modes)
	}
	seen := make(map[string]bool)
	for _, choice := range selections {
		if seen[choice.ID] || !configAccepts(configs, choice.ID, choice.Value) {
			return nil, fmt.Errorf("option %q or its value is not available in the current configuration", choice.ID)
		}
		seen[choice.ID] = true
		if legacy && choice.ID == legacyModeID {
			if err := client.SetMode(ctx, session.SessionID, choice.Value.(string)); err != nil {
				return nil, err
			}
			configs, _ = setModeValue(configs, choice.Value.(string))
			if session.Modes != nil {
				session.Modes.CurrentModeID = choice.Value.(string)
			}
		} else {
			result, err := client.SetConfig(ctx, session.SessionID, choice.ID, choice.Value)
			if err != nil {
				return nil, fmt.Errorf("set %s: %w", choice.ID, err)
			}
			configs, legacy = mergeLegacyModes(result.ConfigOptions, session.Modes)
		}
	}
	for _, choice := range selections {
		if !configAccepts(configs, choice.ID, choice.Value) || !anyEqual(findConfigCurrent(configs, choice.ID), choice.Value) {
			return nil, fmt.Errorf("option %q was changed or became unavailable after a later choice; change the selection order", choice.ID)
		}
	}
	return configs, nil
}
