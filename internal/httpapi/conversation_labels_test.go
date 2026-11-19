package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/store"
)

// A room's label addresses it from every control tool, so "@cat" (the label
// the composer inserts) resolves without the caller knowing the id.
func TestConversationControlTargetsByLabel(t *testing.T) {
	f := newControlFixture(t)
	f.startCallerTurn(t, "web")
	ctx := context.Background()
	if err := f.store.SetConversationLabel(ctx, f.target.ID, "bug-hunter"); err != nil {
		t.Fatal(err)
	}

	status, out := f.post(t, map[string]any{"tool": "read", "conversationId": "Bug-Hunter"})
	if status != http.StatusOK {
		t.Fatalf("read by label status=%d body=%v", status, out)
	}
	conv, _ := out["conversation"].(map[string]any)
	if conv == nil || conv["id"] != f.target.ID || conv["label"] != "bug-hunter" {
		t.Fatalf("label-resolved conversation = %#v", conv)
	}

	// The plain id keeps working, and an unknown handle is a 404 like an
	// unknown id — no ambiguity between the two forms.
	status, out = f.post(t, map[string]any{"tool": "read", "conversationId": f.target.ID})
	if status != http.StatusOK {
		t.Fatalf("read by id status=%d body=%v", status, out)
	}
	status, out = f.post(t, map[string]any{"tool": "read", "conversationId": "no-such-room"})
	if status != http.StatusNotFound {
		t.Fatalf("unknown label status=%d body=%v", status, out)
	}

	// The list rows carry the labels an agent can mention back.
	status, out = f.post(t, map[string]any{"tool": "list"})
	if status != http.StatusOK {
		t.Fatalf("list status=%d body=%v", status, out)
	}
	rows, _ := out["conversations"].([]any)
	var seen bool
	for _, row := range rows {
		m, _ := row.(map[string]any)
		if m != nil && m["id"] == f.target.ID {
			if m["label"] != "bug-hunter" {
				t.Fatalf("list label = %#v", m["label"])
			}
			seen = true
		}
	}
	if !seen {
		t.Fatal("target missing from list rows")
	}
}

// A wait call by label reports the room the label resolved to.
func TestConversationControlWaitByLabel(t *testing.T) {
	f := newControlFixture(t)
	f.startCallerTurn(t, "web")
	ctx := context.Background()
	if err := f.store.SetConversationLabel(ctx, f.target.ID, "owl"); err != nil {
		t.Fatal(err)
	}
	status, out := f.post(t, map[string]any{"tool": "wait", "conversationId": "owl", "timeoutSeconds": 1})
	if status != http.StatusOK {
		t.Fatalf("wait by label status=%d body=%v", status, out)
	}
	if out["timedOut"] != true {
		t.Fatalf("wait by label = %v", out)
	}
	conv, _ := out["conversation"].(map[string]any)
	if conv == nil || conv["id"] != f.target.ID || conv["label"] != "owl" {
		t.Fatalf("label-resolved wait conversation = %#v", conv)
	}
}

// An idle caller whose agent runs a turn in another room gets a warning:
// one agent process can host several rooms, and its conversation tools can
// be routed through another room's connection. The caller's own running
// turn means the identity is in use, so no warning fires then.
func TestConversationControlIdentityWarning(t *testing.T) {
	f := newControlFixture(t)
	ctx := context.Background()

	// Idle caller, nothing running: no warning.
	status, out := f.post(t, map[string]any{"tool": "list"})
	if status != http.StatusOK {
		t.Fatalf("idle list status=%d body=%v", status, out)
	}
	if _, warned := out["warning"]; warned {
		t.Fatalf("warning without a running turn: %v", out["warning"])
	}

	// The same agent runs a turn in the target room while the caller idles.
	msg := store.Message{ConversationID: f.target.ID, Role: "user", Kind: "text", Text: "work"}
	if err := f.store.AppendMessage(ctx, &msg); err != nil {
		t.Fatal(err)
	}
	turn, err := f.store.CreateTurn(ctx, f.target.ID, msg.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if started, err := f.store.StartTurn(ctx, turn.ID); err != nil || !started {
		t.Fatalf("start = %v err=%v", started, err)
	}
	status, out = f.post(t, map[string]any{"tool": "list"})
	if status != http.StatusOK {
		t.Fatalf("busy-elsewhere list status=%d body=%v", status, out)
	}
	warning, _ := out["warning"].(string)
	if !strings.Contains(warning, "idle while its agent is running a turn") || !strings.Contains(warning, "isSelf") {
		t.Fatalf("warning = %q", warning)
	}

	// The caller's own turn running: identity in use, no warning.
	f.startCallerTurn(t, "web")
	status, out = f.post(t, map[string]any{"tool": "list"})
	if status != http.StatusOK {
		t.Fatalf("self-turn list status=%d body=%v", status, out)
	}
	if _, warned := out["warning"]; warned {
		t.Fatalf("warning while the caller's own turn runs: %v", out["warning"])
	}
}
