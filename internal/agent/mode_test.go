package agent

import (
	"encoding/json"
	"testing"

	"github.com/jahrulnr/nusamation/internal/acp"
)

func decodeModeTestOptions(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode options %s: %v", raw, err)
	}
	return list
}

func TestMergeLegacyModesSynthesizesModeOption(t *testing.T) {
	modes := &acp.SessionModeState{CurrentModeID: "ask", AvailableModes: []acp.SessionMode{
		{ID: "ask", Name: "Ask"},
		{ID: "bypass", Name: "Bypass permissions", Description: "Runs tools without asking"},
	}}
	out, synthesized := mergeLegacyModes(json.RawMessage(`[{"id":"model","category":"model","type":"select","currentValue":"fast"}]`), modes)
	if !synthesized {
		t.Fatal("expected a synthesized mode option")
	}
	opts := decodeModeTestOptions(t, out)
	if len(opts) != 2 || opts[0]["id"] != legacyModeID || opts[0]["category"] != "mode" || opts[0]["currentValue"] != "ask" {
		t.Fatalf("unexpected options: %v", opts)
	}
	if choices, _ := opts[0]["options"].([]any); len(choices) != 2 {
		t.Fatalf("expected 2 mode choices, got %v", opts[0]["options"])
	}
	if opts[1]["id"] != "model" {
		t.Fatalf("model option was not kept: %v", opts[1])
	}
}

func TestMergeLegacyModesDefersToAgentModeOption(t *testing.T) {
	raw := json.RawMessage(`[{"id":"mode","category":"mode","type":"select","currentValue":"ask","options":[{"value":"ask","name":"Ask"}]}]`)
	modes := &acp.SessionModeState{CurrentModeID: "ask", AvailableModes: []acp.SessionMode{{ID: "ask", Name: "Ask"}}}
	out, synthesized := mergeLegacyModes(raw, modes)
	if synthesized {
		t.Fatal("an agent-published mode option must not be duplicated")
	}
	if opts := decodeModeTestOptions(t, out); len(opts) != 1 || opts[0]["id"] != "mode" {
		t.Fatalf("unexpected options: %v", opts)
	}
}

func TestMergeLegacyModesReplacesStaleSynthesizedOption(t *testing.T) {
	first, _ := mergeLegacyModes(nil, &acp.SessionModeState{CurrentModeID: "ask", AvailableModes: []acp.SessionMode{{ID: "ask", Name: "Ask"}}})
	out, synthesized := mergeLegacyModes(first, &acp.SessionModeState{CurrentModeID: "bypass", AvailableModes: []acp.SessionMode{{ID: "bypass", Name: "Bypass"}}})
	if !synthesized {
		t.Fatal("expected the option to be synthesized again")
	}
	if opts := decodeModeTestOptions(t, out); len(opts) != 1 || opts[0]["currentValue"] != "bypass" {
		t.Fatalf("stale synthesized option survived: %v", opts)
	}
}

func TestMergeLegacyModesKeepsUnparseableConfig(t *testing.T) {
	out, synthesized := mergeLegacyModes(json.RawMessage(`not-json`), &acp.SessionModeState{CurrentModeID: "ask", AvailableModes: []acp.SessionMode{{ID: "ask", Name: "Ask"}}})
	if synthesized || string(out) != "not-json" {
		t.Fatalf("unparseable config must be kept as-is, got %s (synthesized=%v)", out, synthesized)
	}
}

func TestApplyAgentModeLegacyUpdatesModesAndOption(t *testing.T) {
	s := &Session{modes: &acp.SessionModeState{CurrentModeID: "ask", AvailableModes: []acp.SessionMode{{ID: "ask", Name: "Ask"}, {ID: "bypass", Name: "Bypass"}}}}
	s.setConfigsLocked(nil)
	if !s.legacyMode {
		t.Fatal("expected legacy routing for modes without a config option")
	}
	if !s.applyAgentMode("bypass") {
		t.Fatal("expected the mode to be applied")
	}
	if s.modes.CurrentModeID != "bypass" {
		t.Fatalf("modes state not updated: %q", s.modes.CurrentModeID)
	}
	if opts := decodeModeTestOptions(t, s.Config()); opts[0]["currentValue"] != "bypass" {
		t.Fatalf("mode option not updated: %v", opts)
	}
}

func TestApplyAgentModeConfigOptionUpdatesValue(t *testing.T) {
	s := &Session{}
	s.setConfigsLocked(json.RawMessage(`[{"id":"mode","category":"mode","type":"select","currentValue":"ask","options":[{"value":"ask"},{"value":"bypass"}]}]`))
	if s.legacyMode {
		t.Fatal("an agent-published option must not use legacy routing")
	}
	if !s.applyAgentMode("bypass") {
		t.Fatal("expected the mode to be applied")
	}
	if opts := decodeModeTestOptions(t, s.Config()); opts[0]["currentValue"] != "bypass" {
		t.Fatalf("mode option not updated: %v", opts)
	}
}

func TestMergeLegacyModesKeepsPriorOptionWhenModesMissing(t *testing.T) {
	first, _ := mergeLegacyModes(nil, &acp.SessionModeState{CurrentModeID: "ask", AvailableModes: []acp.SessionMode{{ID: "ask", Name: "Ask"}, {ID: "bypass", Name: "Bypass"}}})
	out, synthesized := mergeLegacyModes(first, nil)
	if !synthesized {
		t.Fatal("a resumed session without modes must keep the legacy option")
	}
	if opts := decodeModeTestOptions(t, out); len(opts) != 1 || opts[0]["id"] != legacyModeID || opts[0]["currentValue"] != "ask" {
		t.Fatalf("prior option not kept: %v", opts)
	}
}

func TestApplyAgentModeLegacyWithoutModesState(t *testing.T) {
	first, _ := mergeLegacyModes(nil, &acp.SessionModeState{CurrentModeID: "ask", AvailableModes: []acp.SessionMode{{ID: "ask", Name: "Ask"}, {ID: "bypass", Name: "Bypass"}}})
	s := &Session{}
	s.setConfigsLocked(first)
	if !s.legacyMode {
		t.Fatal("expected legacy routing from the kept option")
	}
	if !s.applyAgentMode("bypass") {
		t.Fatal("expected the kept option to update")
	}
	if opts := decodeModeTestOptions(t, s.Config()); opts[0]["currentValue"] != "bypass" {
		t.Fatalf("kept option not updated: %v", opts)
	}
}

func TestApplyAgentModeWithoutModeOption(t *testing.T) {
	s := &Session{}
	s.setConfigsLocked(json.RawMessage(`[{"id":"model","category":"model","type":"select","currentValue":"fast"}]`))
	if s.applyAgentMode("bypass") {
		t.Fatal("no mode option exists, so nothing should change")
	}
}
