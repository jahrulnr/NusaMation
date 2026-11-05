package chat

import (
	"context"
	"testing"

	"github.com/jahrulnr/nusamation/internal/store"
)

func TestArchiveAndDeleteAllAgentSessions(t *testing.T) {
	e := newAfterToolsEnv(t)
	ctx := context.Background()
	if err := e.svc.PrepareSession(ctx, e.conv.ID); err != nil {
		t.Fatal(err)
	}
	enabled := true
	ag, err := e.st.CreateAgent(ctx, store.AgentInput{Name: "Other", Slug: "other-test", Command: buildFakeAgent(t), Cwd: e.conv.Cwd, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SwitchAgent(ctx, e.conv.ID, ag.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PrepareSession(ctx, e.conv.ID); err != nil {
		t.Fatal(err)
	}
	archived := true
	if _, err := e.svc.UpdateConversation(ctx, e.conv.ID, ConversationPatch{Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	if got := countTrace(e.trace(), "session_close"); got != 2 {
		t.Fatalf("close calls = %d, trace: %v", got, e.trace())
	}
	if err := e.svc.DeleteConversation(ctx, e.conv.ID); err != nil {
		t.Fatal(err)
	}
	if got := countTrace(e.trace(), "session_delete"); got != 2 {
		t.Fatalf("delete calls = %d, trace: %v", got, e.trace())
	}
}
