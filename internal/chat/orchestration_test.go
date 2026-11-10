package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestSetOrchestrationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)

	svc := New(Options{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx := context.Background()
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Agent", Slug: "agent", Command: "/bin/echo", Cwd: dir, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "Chat", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}

	updated, err := svc.SetOrchestration(ctx, conv.ID, OrchestrationPolicy{Enabled: true, ReadOnly: true, Scope: "project"})
	if err != nil {
		t.Fatal(err)
	}
	got := OrchestrationFrom(updated.Metadata)
	if !got.Enabled || !got.ReadOnly || got.Scope != "project" {
		t.Fatalf("policy = %+v", got)
	}

	if _, err := svc.SetOrchestration(ctx, conv.ID, OrchestrationPolicy{Enabled: true, Scope: "nope"}); !errors.Is(err, ErrInvalidOrchestrationScope) {
		t.Fatalf("invalid scope err = %v", err)
	}
	after, err := st.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := OrchestrationFrom(after.Metadata); got.Scope != "project" {
		t.Fatalf("invalid scope changed metadata: %+v", got)
	}
}

func TestOrchestrationFromDefaultsAndCorruption(t *testing.T) {
	if p := OrchestrationFrom(nil); p.Enabled {
		t.Fatalf("missing metadata enabled: %+v", p)
	}
	p := OrchestrationFrom(json.RawMessage(`{"orchestration":{"enabled":true}}`))
	if !p.Enabled || p.Scope != "all" || p.ReadOnly {
		t.Fatalf("default policy = %+v", p)
	}
	if p := OrchestrationFrom(json.RawMessage(`{"orchestration":{"enabled":true,"scope":"wat"}}`)); p.Enabled {
		t.Fatalf("invalid scope enabled: %+v", p)
	}
	if p := OrchestrationFrom(json.RawMessage(`not json`)); p.Enabled {
		t.Fatalf("corrupt metadata enabled: %+v", p)
	}
}
