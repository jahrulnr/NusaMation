package chat

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/store"
)

// A permission request that arrives after the cancel signal closed must be
// answered as cancelled immediately, without storing an approval or auto-approving.
func TestPermissionAfterCancelSignalIsCancelledWithoutApproval(t *testing.T) {
	e := newAfterToolsEnvWithPolicy(t, "ask")
	ctx := context.Background()
	if err := e.svc.PrepareSession(ctx, e.conv.ID); err != nil {
		t.Fatal(err)
	}
	sess := e.agents.Session(e.conv.ID)
	if sess == nil {
		t.Fatal("no live session for the conversation")
	}

	a := newActiveTurn(e.conv, store.Turn{ID: "turn-late-permission"},
		store.Agent{ID: e.conv.AgentID, ApprovalPolicy: "auto_all"}, store.Message{}, ctx, func() {})
	close(a.cancelSignal)
	e.svc.setActive(a)
	defer e.svc.clearActive(a)

	req := acp.PermissionRequest{
		SessionID: sess.ACPID(),
		ToolCall:  json.RawMessage(`{"toolCallId":"tool-late","title":"Late step","kind":"edit"}`),
		Options:   []acp.PermissionOption{{OptionID: "allow-once", Name: "Allow once", Kind: "allow_once"}},
	}
	resp, err := e.svc.Permission(ctx, sess, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Outcome.Outcome != "cancelled" {
		t.Fatalf("outcome = %#v, want cancelled", resp.Outcome)
	}
	list, err := e.st.ListApprovalsForConversation(ctx, e.conv.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("late permission stored %d approvals: %#v", len(list), list)
	}
}
