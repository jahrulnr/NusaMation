package store

import (
	"context"
	"fmt"
	"testing"
)

func TestWhatsAppSearchPaginationAndHistoryDeduplication(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	for i := 0; i < 55; i++ {
		m := WhatsAppMessage{Chat: "group@g.us", ID: fmt.Sprint(i), Sender: "123@s.whatsapp.net", Timestamp: int64(i / 2), Text: "upgrade php", Live: true}
		if err := st.SaveWhatsAppMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveWhatsAppMessage(ctx, WhatsAppMessage{Chat: "group@g.us", ID: "54", Timestamp: 27, Text: "older copy"}); err != nil {
		t.Fatal(err)
	}
	first, err := st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Query: "UPGRADE PHP", Limit: 50})
	if err != nil || len(first.Messages) != 50 || first.NextCursor == "" {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	second, err := st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Query: "upgrade php", Limit: 50, Cursor: first.NextCursor})
	if err != nil || len(second.Messages) != 5 || second.NextCursor != "" {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	seen := map[string]bool{}
	for _, m := range append(first.Messages, second.Messages...) {
		if seen[m.ID] {
			t.Fatalf("duplicate %q", m.ID)
		}
		seen[m.ID] = true
	}
	if _, err := st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Limit: 50, Cursor: "invalid"}); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func TestWhatsAppMessageUpdatesKeepNewestLiveContent(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	for _, m := range []WhatsAppMessage{
		{Chat: "x", ID: "one", Timestamp: 20, Text: "edited", Raw: []byte(`{"text":"edited"}`), Live: true},
		{Chat: "x", ID: "one", Timestamp: 10, Text: "stale live", Live: true},
		{Chat: "x", ID: "one", Timestamp: 30, Text: "stale history"},
	} {
		if err := st.SaveWhatsAppMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	page, err := st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Limit: 50, Raw: true})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "edited" || string(page.Messages[0].Raw) != `{"text":"edited"}` {
		t.Fatalf("page = %#v, %v", page, err)
	}
}
