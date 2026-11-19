package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestConversationLabelsAutoAssign(t *testing.T) {
	st := testStore(t)
	defer st.Close()
	ctx := context.Background()
	ag := testAgent(t, st)

	var got []string
	for range 3 {
		conv, err := st.CreateConversation(ctx, ag.ID, "Room", "", "web", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if conv.Label == "" {
			t.Fatalf("conversation %s has no label", conv.ID)
		}
		got = append(got, conv.Label)
	}
	if got[0] != "cat" || got[1] != "dog" || got[2] != "tiger" {
		t.Fatalf("auto labels = %#v, want the first animals", got)
	}

	// A project conversation gets a handle too.
	proj, err := st.InsertProject(ctx, "NusaLLM", []string{filepath.Join(t.TempDir(), "root")})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := st.CreateProjectConversation(ctx, ag.ID, "In project", "", "web", "", "", proj.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pc.Label != "lion" {
		t.Fatalf("project conversation label = %q", pc.Label)
	}

	// The label is part of every conversation payload.
	list, err := st.ListConversations(ctx, ConversationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.Label == "" {
			t.Fatalf("listed conversation %s has empty label", c.ID)
		}
	}
}

func TestSetConversationLabel(t *testing.T) {
	st := testStore(t)
	defer st.Close()
	ctx := context.Background()
	ag := testAgent(t, st)
	first, err := st.CreateConversation(ctx, ag.ID, "One", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateConversation(ctx, ag.ID, "Two", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := st.SetConversationLabel(ctx, first.ID, "bug-hunter"); err != nil {
		t.Fatal(err)
	}
	resolved, err := st.FindConversationByLabel(ctx, "Bug-Hunter")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != first.ID {
		t.Fatalf("label resolved to %s", resolved.ID)
	}

	err = st.SetConversationLabel(ctx, second.ID, "bug-hunter")
	if !errors.Is(err, ErrLabelTaken) {
		t.Fatalf("duplicate label error = %v, want ErrLabelTaken", err)
	}
	if err := st.SetConversationLabel(ctx, "cv_missing", "owl"); err == nil {
		t.Fatal("unknown conversation should be not found")
	}
	// Clearing frees the handle for another room.
	if err := st.SetConversationLabel(ctx, first.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetConversationLabel(ctx, second.ID, "bug-hunter"); err != nil {
		t.Fatalf("freed label not reusable: %v", err)
	}
}

func TestLabelValidation(t *testing.T) {
	valid := []string{"cat", "bug-hunter", "room42", "a"}
	invalid := []string{"", "-cat", "cat-", "very long label that is over twenty four chars", "white space", "Room42"}
	for _, label := range valid {
		if !ValidLabel(label) {
			t.Fatalf("ValidLabel(%q) = false, want true", label)
		}
	}
	for _, label := range invalid {
		if ValidLabel(label) {
			t.Fatalf("ValidLabel(%q) = true, want false", label)
		}
	}
	if NormalizeLabel("  CAT ") != "cat" {
		t.Fatal("NormalizeLabel should lowercase and trim")
	}
}

func TestBackfillConversationLabels(t *testing.T) {
	st := testStore(t)
	defer st.Close()
	ctx := context.Background()
	ag := testAgent(t, st)
	// Simulate pre-label rows by clearing what creation assigned.
	var ids []string
	for range 3 {
		conv, err := st.CreateConversation(ctx, ag.ID, "Old", "", "web", "", "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, conv.ID)
		if _, err := st.db.ExecContext(ctx, `UPDATE conversations SET label='' WHERE id=?`, conv.ID); err != nil {
			t.Fatal(err)
		}
	}
	st.backfillConversationLabels(ctx)
	seen := map[string]bool{}
	for _, id := range ids {
		conv, err := st.GetConversation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if conv.Label == "" {
			t.Fatalf("row %s was not backfilled", id)
		}
		if seen[conv.Label] {
			t.Fatalf("backfill reused label %q", conv.Label)
		}
		seen[conv.Label] = true
	}

	// A row whose handle was manually cleared gets a fresh free animal —
	// not its old one back.
	kept, err := st.CreateConversation(ctx, ag.ID, "Kept", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetConversationLabel(ctx, kept.ID, "owl"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE conversations SET label='' WHERE id=?`, kept.ID); err != nil {
		t.Fatal(err)
	}
	st.backfillConversationLabels(ctx)
	after, err := st.GetConversation(ctx, kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Label == "" {
		t.Fatal("cleared row was not backfilled")
	}
	if _, err := st.FindConversationByLabel(ctx, "owl"); err == nil {
		t.Fatal("backfill should not resurrect the cleared handle")
	}
}

// A fresh Open assigns animals to conversations that predate the label
// column, so the operator's existing rooms are mentionable after upgrade.
func TestOpenBackfillsLegacyLabels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ag := testAgent(t, st)
	conv, err := st.CreateConversation(ctx, ag.ID, "Legacy", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE conversations SET label='' WHERE id=?`, conv.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	got, err := st2.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label == "" {
		t.Fatal("Open did not backfill the legacy label")
	}
}
