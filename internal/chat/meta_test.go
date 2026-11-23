package chat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestPatchMetaFuncWritesOnlyWhenChanged(t *testing.T) {
	ctx := context.Background()
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
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "Fake", Slug: "fake-meta", Command: "/bin/echo", Cwd: dir, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "meta", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateConversationMetadata(ctx, conv.ID, json.RawMessage(`{"keep":"yes","drop":"old"}`)); err != nil {
		t.Fatal(err)
	}

	updated, changed, err := svc.patchMetaFunc(ctx, conv.ID, func(meta map[string]any) {
		delete(meta, "drop")
		meta["nilKey"] = nil
		meta["add"] = "new"
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a mutation that changes keys must write")
	}
	meta := map[string]any{}
	if err := json.Unmarshal(updated.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["keep"] != "yes" || meta["add"] != "new" {
		t.Fatalf("metadata after patch = %#v", meta)
	}
	if _, ok := meta["drop"]; ok {
		t.Fatalf("mutate delete must persist: %#v", meta)
	}
	if _, ok := meta["nilKey"]; ok {
		t.Fatalf("a nil value must delete the key: %#v", meta)
	}

	if _, changed, err = svc.patchMetaFunc(ctx, conv.ID, func(meta map[string]any) {}); err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("an unchanged mutation must not write")
	}
}
