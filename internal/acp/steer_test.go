package acp

import (
	"encoding/json"
	"testing"
)

func TestInjectionCapabilities(t *testing.T) {
	mid := InitializeResult{AgentCapabilities: AgentCapabilities{Meta: json.RawMessage(`{"cognition.ai/midTurnPrompts":true}`)}}
	if !mid.SupportsMidTurn() || mid.SupportsSteering() {
		t.Fatalf("mid-turn parse: mid %v steer %v", mid.SupportsMidTurn(), mid.SupportsSteering())
	}
	top := InitializeResult{Meta: json.RawMessage(`{"steering":{"supported":true}}`)}
	if top.SupportsMidTurn() || !top.SupportsSteering() {
		t.Fatalf("top-level steering: mid %v steer %v", top.SupportsMidTurn(), top.SupportsSteering())
	}
	nested := InitializeResult{AgentCapabilities: AgentCapabilities{Meta: json.RawMessage(`{"steering":{"supported":true}}`)}}
	if !nested.SupportsSteering() {
		t.Fatal("agentCapabilities._meta.steering not detected")
	}
	if (SteerResult{Outcome: "injected"}).Accepted() != true || (SteerResult{Outcome: "promptRequired"}).Accepted() {
		t.Fatal("steer acceptance")
	}
}
