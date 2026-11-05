package store

import (
	"context"
	"testing"
)

func TestConversationACPSessionsSurviveSwitchAndAgentRemoval(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	first := testAgent(t, st)
	enabled := true
	second, err := st.CreateAgent(ctx, AgentInput{Name: "Second", Slug: "second-agent", Command: "/bin/echo", Cwd: t.TempDir(), Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, first.ID, "test", first.Cwd, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old", "recovered", "recovered"} {
		if err := st.SetConversationACPSession(ctx, conv.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.UpdateConversation(ctx, conv.ID, ConversationUpdate{AgentID: &second.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetConversationACPSession(ctx, conv.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAgent(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	sessions, err := st.ListConversationACPSessions(ctx, conv.ID)
	if err != nil || len(sessions) != 3 {
		t.Fatalf("sessions = %#v, error = %v", sessions, err)
	}
	if err := st.DeleteConversation(ctx, conv.ID); err != nil {
		t.Fatal(err)
	}
	sessions, err = st.ListConversationACPSessions(ctx, conv.ID)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("sessions after deletion = %#v, error = %v", sessions, err)
	}
}
