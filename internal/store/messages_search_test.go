package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/security"
)

func TestSearchIndexBackfillsOnMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	ctx := context.Background()
	ag := testAgent(t, st)
	conv, err := st.CreateConversation(ctx, ag.ID, "backfill", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	msg := Message{ConversationID: conv.ID, Role: "user", Kind: "text", Text: "legacy deploy"}
	if err := st.AppendMessage(ctx, &msg); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP TRIGGER IF EXISTS messages_fts_ai;
		DROP TRIGGER IF EXISTS messages_fts_ad;
		DROP TRIGGER IF EXISTS messages_fts_au;
		DROP TABLE IF EXISTS messages_fts;
		PRAGMA user_version = 9;`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hits, err := st.SearchMessages(ctx, MessageSearchFilter{Query: "legacy", Kinds: []string{"text"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].MessageID != msg.ID {
		t.Fatalf("backfilled hits = %#v", hits)
	}
}

func TestListMessagesAscending(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	ag := testAgent(t, st)
	conv, err := st.CreateConversation(ctx, ag.ID, "ascending", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		msg := Message{ConversationID: conv.ID, Role: "user", Kind: "text", Text: string(rune('a' + i))}
		if err := st.AppendMessage(ctx, &msg); err != nil {
			t.Fatal(err)
		}
	}
	first, err := st.ListMessagesAscending(ctx, conv.ID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Text != "a" || first[1].Text != "b" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := st.ListMessagesAscending(ctx, conv.ID, first[len(first)-1].Seq, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Text != "c" {
		t.Fatalf("second page = %#v", second)
	}
}

func TestSearchMessages(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	ag := testAgent(t, st)
	conv, err := st.CreateConversation(ctx, ag.ID, "Search target", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateConversation(ctx, ag.ID, "Other", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	text := Message{ConversationID: conv.ID, Role: "assistant", Kind: "text", Text: "deploy the backend to staging"}
	if err := st.AppendMessage(ctx, &text); err != nil {
		t.Fatal(err)
	}
	tool := Message{ConversationID: conv.ID, Role: "assistant", Kind: "tool", Text: "deploy command output"}
	if err := st.AppendMessage(ctx, &tool); err != nil {
		t.Fatal(err)
	}
	thought := Message{ConversationID: conv.ID, Role: "assistant", Kind: "thought", Text: "deploy reasoning"}
	if err := st.AppendMessage(ctx, &thought); err != nil {
		t.Fatal(err)
	}
	otherMsg := Message{ConversationID: other.ID, Role: "user", Kind: "text", Text: "deploy somewhere else"}
	if err := st.AppendMessage(ctx, &otherMsg); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{"deploy/backend", "deploy-backend", "deploy.backend"} {
		hits, err := st.SearchMessages(ctx, MessageSearchFilter{Query: query, Kinds: []string{"text"}})
		if err != nil || len(hits) != 1 || hits[0].MessageID != text.ID {
			t.Fatalf("punctuated query %q: hits=%v err=%v", query, hits, err)
		}
	}

	hits, err := st.SearchMessages(ctx, MessageSearchFilter{Query: "deploy", Kinds: []string{"text"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("text hits = %#v", hits)
	}
	if hits[0].ConversationID == "" || hits[0].Snippet == "" || !strings.Contains(strings.ToLower(hits[0].Snippet), "deploy") {
		t.Fatalf("hit = %#v", hits[0])
	}
	if hits[0].Score == 0 {
		t.Fatalf("missing score: %#v", hits[0])
	}

	hits, err = st.SearchMessages(ctx, MessageSearchFilter{Query: "deploy!", Kinds: []string{"tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].MessageID != tool.ID {
		t.Fatalf("tool hits = %#v", hits)
	}

	hits, err = st.SearchMessages(ctx, MessageSearchFilter{Query: "deploy", ConversationID: conv.ID, Kinds: []string{"text"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].MessageID != text.ID {
		t.Fatalf("conversation hits = %#v", hits)
	}

	if err := st.UpdateMessageText(ctx, text.ID, "release the backend", "complete"); err != nil {
		t.Fatal(err)
	}
	hits, err = st.SearchMessages(ctx, MessageSearchFilter{Query: "deploy", Kinds: []string{"text"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].MessageID != otherMsg.ID {
		t.Fatalf("after update hits = %#v", hits)
	}
	hits, err = st.SearchMessages(ctx, MessageSearchFilter{Query: "release", Kinds: []string{"text"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].MessageID != text.ID {
		t.Fatalf("release hits = %#v", hits)
	}

	if err := st.DeleteMessage(ctx, otherMsg.ID); err != nil {
		t.Fatal(err)
	}
	hits, err = st.SearchMessages(ctx, MessageSearchFilter{Query: "deploy", Kinds: []string{"text"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("after delete hits = %#v", hits)
	}

	hits, err = st.SearchMessages(ctx, MessageSearchFilter{Query: "   "})
	if err != nil || len(hits) != 0 {
		t.Fatalf("empty query = %#v err=%v", hits, err)
	}
}
