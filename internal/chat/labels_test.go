package chat

import (
	"context"
	"strings"
	"testing"
)

func TestUpdateConversationLabel(t *testing.T) {
	svc, st, _, ag := newProjectEnv(t, "")
	ctx := context.Background()
	first, err := st.CreateConversation(ctx, ag.ID, "Hunter", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateConversation(ctx, ag.ID, "Fixer", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.UpdateConversation(ctx, first.ID, ConversationPatch{Label: new("Bug-Hunter")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "bug-hunter" {
		t.Fatalf("label = %q", got.Label)
	}

	// Mentions resolve case-insensitively to the stored handle.
	resolved, err := st.FindConversationByLabel(ctx, "BUG-HUNTER")
	if err != nil || resolved.ID != first.ID {
		t.Fatalf("resolve = %v %v", resolved.ID, err)
	}

	_, err = svc.UpdateConversation(ctx, first.ID, ConversationPatch{Label: new("not a handle!")})
	if err == nil || !strings.Contains(err.Error(), "not a valid handle") {
		t.Fatalf("invalid label error = %v", err)
	}
	_, err = svc.UpdateConversation(ctx, second.ID, ConversationPatch{Label: new("bug-hunter")})
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("duplicate label error = %v", err)
	}
	// The refused patch left the room's own label untouched.
	after, err := st.GetConversation(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Label == "bug-hunter" {
		t.Fatal("duplicate label overwrote the target")
	}

	// Clearing frees the handle.
	if _, err := svc.UpdateConversation(ctx, first.ID, ConversationPatch{Label: new("")}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateConversation(ctx, second.ID, ConversationPatch{Label: new("bug-hunter")}); err != nil {
		t.Fatalf("freed handle not reusable: %v", err)
	}
}

//go:fix inline
func strPtrLocal(s string) *string { return new(s) }
