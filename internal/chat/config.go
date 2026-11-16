package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/store"
)

type ConfigChoice struct {
	Value       string          `json:"value"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Meta        json.RawMessage `json:"_meta,omitempty"`
	Group       string          `json:"group,omitempty"`
	Options     []ConfigChoice  `json:"options,omitempty"`
}

type ConfigOption struct {
	ID           string          `json:"id"`
	Name         string          `json:"name,omitempty"`
	Description  string          `json:"description,omitempty"`
	Category     string          `json:"category,omitempty"`
	Type         string          `json:"type,omitempty"`
	CurrentValue any             `json:"currentValue"`
	Options      []ConfigChoice  `json:"options,omitempty"`
	Meta         json.RawMessage `json:"_meta,omitempty"`
}

func ParseConfigOptions(raw json.RawMessage) []ConfigOption {
	var opts []ConfigOption
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &opts)
	}
	if opts == nil {
		opts = []ConfigOption{}
	}
	return opts
}

func findConfigOption(opts []ConfigOption, id string) (ConfigOption, bool) {
	for _, o := range opts {
		if o.ID == id {
			return o, true
		}
	}
	return ConfigOption{}, false
}

func configByCategory(opts []ConfigOption, category string) (ConfigOption, bool) {
	for _, o := range opts {
		if strings.EqualFold(o.Category, category) {
			return o, true
		}
	}
	return ConfigOption{}, false
}

func modelFromConfigs(raw json.RawMessage) string {
	opt, ok := configByCategory(ParseConfigOptions(raw), "model")
	if !ok {
		return ""
	}
	if v, ok := opt.CurrentValue.(string); ok {
		return v
	}
	return ""
}

func configValueString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func configValueAllowed(opt ConfigOption, value any) error {
	switch strings.ToLower(opt.Type) {
	case "select":
		want := configValueString(value)
		for _, c := range opt.Options {
			if c.Value == want {
				return nil
			}
			for _, nested := range c.Options {
				if nested.Value == want {
					return nil
				}
			}
		}
		return fmt.Errorf("%q is not a valid value for %s", want, opt.Name)
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s expects true or false", opt.Name)
		}
	}
	return nil
}

func setConfigValue(raw json.RawMessage, configID string, value any) json.RawMessage {
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

// SetConfig changes a live ACP session option (model, mode, thought level,
// speed, ...). If no session is running yet the choice is stored and applied
// when the conversation starts next.
func (s *Service) SetConfig(ctx context.Context, conversationID, configID string, value any) (store.Conversation, error) {
	configID = strings.TrimSpace(configID)
	if configID == "" {
		return store.Conversation{}, fmt.Errorf("config id is required")
	}
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return conv, err
	}
	ag, err := s.store.GetAgent(ctx, conv.AgentID)
	if err != nil {
		return conv, err
	}
	configs := conv.ConfigOptions
	if len(ParseConfigOptions(configs)) == 0 {
		configs = ag.ConfigOptions
	}
	opts := ParseConfigOptions(configs)
	opt, ok := findConfigOption(opts, configID)
	if !ok {
		return conv, fmt.Errorf("this agent has no %q option", configID)
	}
	if err := configValueAllowed(opt, value); err != nil {
		return conv, err
	}

	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	live := s.agents.Session(conversationID)
	applied := false
	if live != nil && live.AgentID() == conv.AgentID && live.ACPID() != "" {
		if updatedConfigs, err := s.agents.SetConfig(ctx, live, configID, value); err == nil {
			model := conv.Model
			if m := modelFromConfigs(updatedConfigs); m != "" {
				model = m
			}
			_ = s.store.SetConversationConfig(ctx, conv.ID, model, updatedConfigs)
			applied = true
		} else {
			return conv, fmt.Errorf("the agent did not accept this setting: %w", err)
		}
	}

	// Nested merge, so it cannot go through patchMeta; hold metaMu across the
	// read-modify-write like patchMeta does.
	s.metaMu.Lock()
	if fresh, err := s.store.GetConversation(ctx, conv.ID); err == nil {
		meta, _ := s.metaMap(fresh)
		desired, _ := meta["desiredConfig"].(map[string]any)
		if desired == nil {
			desired = map[string]any{}
		}
		desired[configID] = value
		meta["desiredConfig"] = desired
		if err := s.store.UpdateConversationMetadata(ctx, conv.ID, mustJSON(meta)); err != nil {
			s.log.Warn("failed to persist desired config", "conversation", conv.ID, "error", err)
		}
	}
	s.metaMu.Unlock()

	if !applied {
		model := conv.Model
		newConfigs := setConfigValue(configs, configID, value)
		if strings.EqualFold(opt.Category, "model") {
			model = configValueString(value)
		}
		_ = s.store.SetConversationConfig(ctx, conv.ID, model, newConfigs)
	}
	updated, err := s.store.GetConversation(ctx, conv.ID)
	if err != nil {
		return updated, err
	}
	s.emit("config.updated", conversationID, map[string]any{
		"conversationId": conversationID, "configId": configID, "value": value,
		"configOptions": updated.ConfigOptions, "model": updated.Model,
	})
	s.emit("conversation.updated", conversationID, updated)
	return updated, nil
}

// SetSelector applies a "model[k=v,...]" selector in two phases: the model is
// switched first, then each modifier is resolved against the catalog the
// switch produced — options can appear, disappear, or reset when the model
// changes, so validating modifiers up front would reject legitimate values.
// A modifier failure never rolls the model back; the result reports exactly
// what landed.
func (s *Service) SetSelector(ctx context.Context, conversationID, raw string) (SelectorResult, error) {
	var res SelectorResult
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return res, errors.New("a model value or selector is required")
	}
	opts, err := s.ConversationConfig(ctx, conversationID)
	if err != nil {
		return res, err
	}
	modelOpt, ok := configByCategory(opts, "model")
	if !ok {
		return res, errors.New("this agent has no model option")
	}
	choice, rerr := ResolveModel(opts, raw)
	var mods []Modifier
	if rerr != nil {
		model, m, perr := ParseSelector(raw)
		switch {
		case perr != nil:
			return res, perr
		case model == raw:
			// No bracket suffix: report the original resolve error.
		default:
			choice, rerr = ResolveModel(opts, model)
			if rerr == nil {
				mods = m
			}
		}
	}
	if rerr != nil {
		if amb, ok := errors.AsType[*AmbiguousError](rerr); ok {
			res.Candidates = amb.Candidates
		}
		return res, rerr
	}
	updated, err := s.SetConfig(ctx, conversationID, modelOpt.ID, choice.Value)
	if err != nil {
		return res, err
	}
	res.Model = updated.Model
	if res.Model == "" {
		res.Model = choice.Value
	}
	if len(mods) == 0 {
		return res, nil
	}
	opts, err = s.ConversationConfig(ctx, conversationID)
	if err != nil {
		return res, err
	}
	for _, m := range mods {
		opt, ok := ResolveModifierKey(opts, m.Key)
		if !ok {
			res.Failed = append(res.Failed, ModifierFailure{Modifier: m, Reason: "unknown option"})
			continue
		}
		value, offers := ResolveModifierValue(opt, m.Value)
		if offers != nil {
			res.Failed = append(res.Failed, ModifierFailure{Modifier: m, OptionID: opt.ID, Reason: "not available", Offers: offers})
			continue
		}
		v := any(value)
		if strings.EqualFold(opt.Type, "boolean") {
			v = value == "true"
		}
		if _, err := s.SetConfig(ctx, conversationID, opt.ID, v); err != nil {
			res.Failed = append(res.Failed, ModifierFailure{Modifier: m, OptionID: opt.ID, Reason: shortErr(err)})
			continue
		}
		res.Applied = append(res.Applied, Modifier{Key: opt.ID, Value: value})
	}
	return res, nil
}

func (s *Service) SetModel(ctx context.Context, conversationID, model string) (store.Conversation, error) {
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return conv, err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return conv, fmt.Errorf("model is required")
	}
	configID := "model"
	opts := ParseConfigOptions(conv.ConfigOptions)
	if opt, ok := configByCategory(opts, "model"); ok {
		configID = opt.ID
	}
	return s.SetConfig(ctx, conversationID, configID, model)
}

func (s *Service) ConversationConfig(ctx context.Context, conversationID string) ([]ConfigOption, error) {
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	raw := conv.ConfigOptions
	if len(ParseConfigOptions(raw)) == 0 {
		if ag, err := s.store.GetAgent(ctx, conv.AgentID); err == nil {
			raw = ag.ConfigOptions
		}
	}
	return ParseConfigOptions(raw), nil
}
