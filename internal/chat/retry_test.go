package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/store"
)

// Retry is anchored to the failed turn but its payload is a neutral
// "continue" nudge — never a resend of the original prompt, and never the
// newest user message (an error card can sit below later successes).
func TestRetrySendsContinueAfterFailedTurn(t *testing.T) {
	e := newAfterToolsEnv(t)
	ctx := context.Background()

	// Fabricate the transcript of a failed turn: original prompt, errored
	// turn, and the system error card the UI anchors its button to.
	original := store.Message{ConversationID: e.conv.ID, Role: "user", Kind: "text", Text: "original request"}
	if err := e.st.AppendMessage(ctx, &original); err != nil {
		t.Fatal(err)
	}
	failed, err := e.st.CreateTurn(ctx, e.conv.ID, original.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.FinishTurn(ctx, failed.ID, "error", "", "boom"); err != nil {
		t.Fatal(err)
	}
	card := store.Message{ConversationID: e.conv.ID, TurnID: failed.ID, Role: "system", Kind: "error", Status: "error", Text: "The agent hit an error: boom"}
	if err := e.st.AppendMessage(ctx, &card); err != nil {
		t.Fatal(err)
	}
	// A newer request that SUCCEEDED sits above the failure — retry must not
	// re-run it.
	later := store.Message{ConversationID: e.conv.ID, Role: "user", Kind: "text", Text: "later request"}
	if err := e.st.AppendMessage(ctx, &later); err != nil {
		t.Fatal(err)
	}
	good, err := e.st.CreateTurn(ctx, e.conv.ID, later.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.FinishTurn(ctx, good.ID, "completed", "end_turn", ""); err != nil {
		t.Fatal(err)
	}

	retried, err := e.svc.Retry(ctx, e.conv.ID, failed.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	// The nudge lands as a fresh user message tied to the retried turn.
	msgs, err := e.st.ListMessages(ctx, e.conv.ID, 0, 0, 5)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("messages: %v", err)
	}
	newest := msgs[len(msgs)-1]
	if newest.Role != "user" || newest.Text != "continue" {
		t.Fatalf("newest message = %q %q, want user continue", newest.Role, newest.Text)
	}
	if retried.UserMessageID != newest.ID {
		t.Fatalf("retried turn bound to %q, want the continue message %q", retried.UserMessageID, newest.ID)
	}
	// The agent actually receives the nudge (the prompt carries session
	// instructions and the nusamation-context block around it).
	waitFor(t, func() bool {
		for _, line := range e.trace() {
			if strings.HasPrefix(line, "prompt_start ") && strings.Contains(line, ">continue  <nusamation-context>") {
				return true
			}
		}
		return false
	}, "the agent to receive the continue nudge")
	e.waitStatus(retried.ID, "completed")
}

// A card that is not a failed turn cannot fire a retry: completed turns are
// refused, and a foreign conversation's turn does not pass the anchor check.
func TestRetryRefusesNonFailedTurns(t *testing.T) {
	e := newAfterToolsEnv(t)
	ctx := context.Background()
	msg := store.Message{ConversationID: e.conv.ID, Role: "user", Kind: "text", Text: "done"}
	if err := e.st.AppendMessage(ctx, &msg); err != nil {
		t.Fatal(err)
	}
	done, err := e.st.CreateTurn(ctx, e.conv.ID, msg.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.FinishTurn(ctx, done.ID, "completed", "end_turn", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Retry(ctx, e.conv.ID, done.ID, "web", ""); err == nil || !strings.Contains(err.Error(), "only failed turns") {
		t.Fatalf("completed-turn retry error = %v", err)
	}
	if _, err := e.svc.Retry(ctx, e.conv.ID, "turn_missing", "web", ""); err == nil || !strings.Contains(err.Error(), "nothing to retry") {
		t.Fatalf("missing-turn retry error = %v", err)
	}
	// A failed turn from another conversation is refused as not belonging.
	other, err := e.svc.CreateConversation(ctx, e.conv.AgentID, "Other", e.conv.Cwd, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	otherMsg := store.Message{ConversationID: other.ID, Role: "user", Kind: "text", Text: "x"}
	if err := e.st.AppendMessage(ctx, &otherMsg); err != nil {
		t.Fatal(err)
	}
	otherFailed, err := e.st.CreateTurn(ctx, other.ID, otherMsg.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.FinishTurn(ctx, otherFailed.ID, "error", "", "boom"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Retry(ctx, e.conv.ID, otherFailed.ID, "web", ""); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("foreign-turn retry error = %v", err)
	}
}
