package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestListConversationsFiltersStatusAndProject(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	ag := testAgent(t, st)
	project, err := st.InsertProject(ctx, "Project", []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	loose, err := st.CreateConversation(ctx, ag.ID, "loose", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	member, err := st.CreateProjectConversation(ctx, ag.ID, "member", "", "web", "", "", project.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetConversationStatus(ctx, loose.ID, "active"); err != nil {
		t.Fatal(err)
	}

	active, err := st.ListConversations(ctx, ConversationFilter{Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != loose.ID {
		t.Fatalf("active = %+v", active)
	}

	inProject, err := st.ListConversations(ctx, ConversationFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(inProject) != 1 || inProject[0].ID != member.ID {
		t.Fatalf("project = %+v", inProject)
	}
	if n, err := st.CountProjectConversations(ctx, project.ID); err != nil || n != 1 {
		t.Fatalf("project count = %d err=%v", n, err)
	}

	all, err := st.ListConversations(ctx, ConversationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %+v", all)
	}
}

func TestCountSpawnedConversations(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	ag := testAgent(t, st)
	parent, err := st.CreateConversation(ctx, ag.ID, "parent", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := st.CreateConversation(ctx, ag.ID, "child", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountSpawnedConversations(ctx, parent.ID); err != nil || n != 0 {
		t.Fatalf("initial count = %d err=%v", n, err)
	}
	meta, _ := json.Marshal(map[string]any{"spawnedBy": parent.ID})
	if err := st.UpdateConversationMetadata(ctx, child.ID, meta); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountSpawnedConversations(ctx, parent.ID); err != nil || n != 1 {
		t.Fatalf("count = %d err=%v", n, err)
	}
	if _, err := st.UpdateConversation(ctx, child.ID, ConversationUpdate{Archived: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountSpawnedConversations(ctx, parent.ID); err != nil || n != 0 {
		t.Fatalf("archived count = %d err=%v", n, err)
	}
}

func boolPtr(v bool) *bool { return &v }

func TestConversationProgress(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	ag := testAgent(t, st)
	conv, err := st.CreateConversation(ctx, ag.ID, "progress", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}

	first := Message{ConversationID: conv.ID, Role: "user", Kind: "text", Text: "one"}
	if err := st.AppendMessage(ctx, &first); err != nil {
		t.Fatal(err)
	}
	second := Message{ConversationID: conv.ID, Role: "assistant", Kind: "text", Text: "two"}
	if err := st.AppendMessage(ctx, &second); err != nil {
		t.Fatal(err)
	}

	turn, err := st.CreateTurn(ctx, conv.ID, first.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if started, err := st.StartTurn(ctx, turn.ID); err != nil || !started {
		t.Fatalf("start = %v err=%v", started, err)
	}
	ap := Approval{ConversationID: conv.ID, TurnID: turn.ID, Title: "Approve", Kind: "execute"}
	if err := st.CreateApproval(ctx, &ap); err != nil {
		t.Fatal(err)
	}

	prog, err := st.ConversationProgress(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prog.HeadSeq != second.Seq {
		t.Fatalf("head = %d, want %d", prog.HeadSeq, second.Seq)
	}
	if prog.Active == nil || prog.Active.ID != turn.ID {
		t.Fatalf("active = %+v", prog.Active)
	}
	if prog.Queued != 0 || prog.PendingApprovals != 1 {
		t.Fatalf("progress = %+v", prog)
	}

	if _, err := st.CreateTurn(ctx, conv.ID, first.ID, "web", ""); err != nil {
		t.Fatal(err)
	}
	prog, err = st.ConversationProgress(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Queued != 1 {
		t.Fatalf("queued = %d", prog.Queued)
	}

	if err := st.FinishTurn(ctx, turn.ID, "completed", "end_turn", ""); err != nil {
		t.Fatal(err)
	}
	prog, err = st.ConversationProgress(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Active != nil {
		t.Fatalf("active after finish = %+v", prog.Active)
	}
}
