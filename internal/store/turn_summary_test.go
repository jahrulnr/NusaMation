package store

import (
	"context"
	"testing"
)

func TestListLastTurnSummaries(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	ag := testAgent(t, st)
	conv, err := st.CreateConversation(ctx, ag.ID, "turns", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	makeTurn := func(prompt, answer, status string) Turn {
		user := Message{ConversationID: conv.ID, Role: "user", Kind: "text", Text: prompt}
		if err := st.AppendMessage(ctx, &user); err != nil {
			t.Fatal(err)
		}
		turn, err := st.CreateTurn(ctx, conv.ID, user.ID, "web", "")
		if err != nil {
			t.Fatal(err)
		}
		assistant := Message{ConversationID: conv.ID, TurnID: turn.ID, Role: "assistant", Kind: "text", Text: answer}
		if err := st.AppendMessage(ctx, &assistant); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishTurn(ctx, turn.ID, status, "end_turn", ""); err != nil {
			t.Fatal(err)
		}
		return turn
	}
	makeTurn("first prompt", "first answer", "completed")
	last := makeTurn("second prompt", "second answer", "error")

	summaries, err := st.ListLastTurnSummaries(ctx, []string{conv.ID})
	if err != nil {
		t.Fatal(err)
	}
	summary, ok := summaries[conv.ID]
	if !ok {
		t.Fatalf("missing summary: %+v", summaries)
	}
	if summary.ID != last.ID || summary.Status != "error" || summary.Prompt != "second prompt" || summary.Answer != "second answer" {
		t.Fatalf("summary = %+v", summary)
	}
	if empty, err := st.ListLastTurnSummaries(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty = %+v err=%v", empty, err)
	}
}
