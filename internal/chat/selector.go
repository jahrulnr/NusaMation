package chat

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusamation/internal/store"
)

// Modifier is one key=value entry inside a selector's [..] suffix.
type Modifier struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ModifierFailure reports a modifier that did not land after the model switch.
type ModifierFailure struct {
	Modifier Modifier `json:"modifier"`
	OptionID string   `json:"optionId,omitempty"`
	Reason   string   `json:"reason"`
	Offers   []string `json:"offers,omitempty"`
}

// SelectorResult is the structured outcome of SetSelector: what landed, what
// failed, and — when the model part was ambiguous — the candidates to show.
type SelectorResult struct {
	Model      string            `json:"model,omitempty"`
	Applied    []Modifier        `json:"applied,omitempty"`
	Failed     []ModifierFailure `json:"failed,omitempty"`
	Candidates []ConfigChoice    `json:"candidates,omitempty"`
}

// AmbiguousError means the input matched more than one choice; Candidates are
// the values to present instead of guessing.
type AmbiguousError struct {
	Input      string
	Candidates []ConfigChoice
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q matches more than one option", e.Input)
}

// ParseSelector splits "model[k=v,k=v]" into the model part and its modifiers.
// The model side is opaque — it is never split on '/', '-', ':' or '.', and the
// bracket suffix is only peeled when the whole argument ends with ']'.
func ParseSelector(raw string) (model string, mods []Modifier, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil, errors.New("a model value or selector is required")
	}
	if !strings.HasSuffix(raw, "]") {
		return raw, nil, nil
	}
	i := strings.LastIndex(raw, "[")
	if i < 0 {
		return raw, nil, nil
	}
	model = strings.TrimSpace(raw[:i])
	if model == "" {
		return "", nil, fmt.Errorf("invalid selector %q: the model part is empty", raw)
	}
	for part := range strings.SplitSeq(raw[i+1:len(raw)-1], ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if k = strings.TrimSpace(k); !ok || k == "" {
			return "", nil, fmt.Errorf("invalid modifier %q: expected key=value", part)
		}
		mods = append(mods, Modifier{Key: k, Value: strings.TrimSpace(v)})
	}
	return model, mods, nil
}

// ResolveModel matches raw against the model option's values: exact, then
// case-insensitive, prefix, and finally a segment-anchored match ("opus" hits
// "claude-opus-4.5"). The ConfigChoice keeps group/name for candidate lists.
func ResolveModel(opts []ConfigOption, raw string) (ConfigChoice, error) {
	opt, ok := configByCategory(opts, "model")
	if !ok {
		return ConfigChoice{}, errors.New("this agent has no model option")
	}
	choices := flattenChoices(opt.Options)
	raw = strings.TrimSpace(raw)
	for _, c := range choices {
		if c.Value == raw {
			return c, nil
		}
	}
	for _, c := range choices {
		if strings.EqualFold(c.Value, raw) {
			return c, nil
		}
	}
	var cands []ConfigChoice
	for _, c := range choices {
		if hasPrefixFold(c.Value, raw) {
			cands = append(cands, c)
		}
	}
	if len(cands) == 1 {
		return cands[0], nil
	}
	if len(cands) > 1 {
		return ConfigChoice{}, &AmbiguousError{Input: raw, Candidates: cands}
	}
	for _, c := range choices {
		if segmentMatch(c.Value, raw) {
			cands = append(cands, c)
		}
	}
	switch len(cands) {
	case 1:
		return cands[0], nil
	case 0:
		return ConfigChoice{}, fmt.Errorf("no model matches %q", raw)
	default:
		return ConfigChoice{}, &AmbiguousError{Input: raw, Candidates: cands}
	}
}

// ResolveModifierKey finds an option by id first, then category, then name —
// all case-insensitive. Real catalogs mix them ("effort" id under a
// "thought_level" category), so every spelling lands on the same option.
func ResolveModifierKey(opts []ConfigOption, key string) (ConfigOption, bool) {
	for _, o := range opts {
		if strings.EqualFold(o.ID, key) {
			return o, true
		}
	}
	for _, o := range opts {
		if o.Category != "" && strings.EqualFold(o.Category, key) {
			return o, true
		}
	}
	for _, o := range opts {
		if o.Name != "" && strings.EqualFold(o.Name, key) {
			return o, true
		}
	}
	return ConfigOption{}, false
}

// ResolveModifierValue validates value against the option's allowed values and
// returns the canonical (exact-case) form. The second return is the offer list
// shown when the value is unknown.
func ResolveModifierValue(opt ConfigOption, value string) (string, []string) {
	if strings.EqualFold(opt.Type, "boolean") {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true":
			return "true", nil
		case "false":
			return "false", nil
		}
		return "", []string{"true", "false"}
	}
	choices := flattenChoices(opt.Options)
	if len(choices) == 0 && !strings.EqualFold(opt.Type, "select") {
		return value, nil
	}
	for _, c := range choices {
		if c.Value == value {
			return c.Value, nil
		}
	}
	for _, c := range choices {
		if strings.EqualFold(c.Value, value) {
			return c.Value, nil
		}
	}
	offers := make([]string, 0, len(choices))
	for _, c := range choices {
		offers = append(offers, c.Value)
	}
	return "", offers
}

// EchoSelector prints the selector that reproduces the current model — the
// bare value only, so pasting it back never clobbers effort/mode choices that
// are already active.
func EchoSelector(conv store.Conversation, opts []ConfigOption) string {
	if conv.Model != "" {
		return conv.Model
	}
	if opt, ok := configByCategory(opts, "model"); ok {
		return configValueString(opt.CurrentValue)
	}
	return ""
}

// flattenChoices lists a select option's values including nested groups —
// the same set configValueAllowed accepts.
func flattenChoices(list []ConfigChoice) []ConfigChoice {
	var out []ConfigChoice
	for _, c := range list {
		out = append(out, c)
		out = append(out, flattenChoices(c.Options)...)
	}
	return out
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// segmentMatch reports whether raw appears in value anchored at a segment
// boundary — the start of the value or right after '/' or '-'.
func segmentMatch(value, raw string) bool {
	if hasPrefixFold(value, raw) {
		return true
	}
	lv, lr := strings.ToLower(value), strings.ToLower(raw)
	return strings.Contains(lv, "-"+lr) || strings.Contains(lv, "/"+lr)
}
