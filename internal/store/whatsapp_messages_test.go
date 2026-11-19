package store

import (
	"context"
	"fmt"
	"testing"
)

func TestWhatsAppSearchPaginationAndHistoryDeduplication(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	for i := range 55 {
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

func TestWhatsAppMessageUpsertPreservesChatName(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	m := WhatsAppMessage{Chat: "group@g.us", ID: "m1", Sender: "123@s.whatsapp.net", ChatName: "Team", Timestamp: 10, Text: "hi", Live: true}
	if err := st.SaveWhatsAppMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	// A live upsert without a chat name must not erase the known one.
	if err := st.SaveWhatsAppMessage(ctx, WhatsAppMessage{Chat: "group@g.us", ID: "m1", Sender: "123@s.whatsapp.net", Timestamp: 20, Text: "hi again", Live: true}); err != nil {
		t.Fatal(err)
	}
	page, err := st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Limit: 10})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("page = %#v, %v", page, err)
	}
	if page.Messages[0].ChatName != "Team" {
		t.Fatalf("chat_name = %q, want %q", page.Messages[0].ChatName, "Team")
	}
	// A newer non-empty name still wins.
	if err := st.SaveWhatsAppMessage(ctx, WhatsAppMessage{Chat: "group@g.us", ID: "m1", ChatName: "Team Renamed", Timestamp: 30, Text: "hi again", Live: true}); err != nil {
		t.Fatal(err)
	}
	page, err = st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Limit: 10})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ChatName != "Team Renamed" {
		t.Fatalf("page = %#v, %v", page, err)
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

func TestWhatsAppTombstoneReceiptsPurgeAndFTS(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.SaveWhatsAppMessage(ctx, WhatsAppMessage{
		Chat: "62@s.whatsapp.net", ID: "m1", Sender: "62@s.whatsapp.net",
		Timestamp: 100, Text: "secret plans", Raw: []byte(`{"text":"secret plans"}`), Live: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWhatsAppMessage(ctx, WhatsAppMessage{
		Chat: "62@s.whatsapp.net", ID: "out1", Sender: "me@s.whatsapp.net", FromMe: true,
		Timestamp: 110, Text: "my reply", Live: true,
	}); err != nil {
		t.Fatal(err)
	}

	// A revoke clears the content so a deleted message is unsearchable.
	if err := st.RevokeWhatsAppMessage(ctx, "62@s.whatsapp.net", "m1"); err != nil {
		t.Fatal(err)
	}
	page, err := st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Query: "secret", Limit: 50})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("revoked message still searchable: %#v, %v", page, err)
	}
	if err := st.RevokeWhatsAppMessage(ctx, "62@s.whatsapp.net", "missing"); err != nil {
		t.Fatalf("revoking an unknown row failed: %v", err)
	}

	// Receipts progress delivered → read and never downgrade.
	if err := st.MarkWhatsAppReceipt(ctx, "62@s.whatsapp.net", []string{"out1"}, "delivered"); err != nil {
		t.Fatal(err)
	}
	page, err = st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Query: "reply", Limit: 50})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Receipt != "delivered" {
		t.Fatalf("receipt = %#v, %v", page, err)
	}
	if err := st.MarkWhatsAppReceipt(ctx, "62@s.whatsapp.net", []string{"out1"}, "read"); err != nil {
		t.Fatal(err)
	}
	// The delivered receipt must not overwrite the read state.
	if err := st.MarkWhatsAppReceipt(ctx, "62@s.whatsapp.net", []string{"out1"}, "delivered"); err != nil {
		t.Fatal(err)
	}
	page, err = st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Query: "reply", Limit: 50})
	if err != nil || page.Messages[0].Receipt != "read" {
		t.Fatalf("receipt downgrade: %#v", page)
	}
	// Inbound messages are never marked.
	if err := st.MarkWhatsAppReceipt(ctx, "62@s.whatsapp.net", []string{"m1"}, "read"); err != nil {
		t.Fatal(err)
	}
	page, err = st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Query: "secret", Limit: 50})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("inbound row resurrected: %#v", page)
	}

	// Punctuation-heavy queries fall back to the substring scan.
	page, err = st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Query: `"my reply"`, Limit: 50})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("quoted query = %#v, %v", page, err)
	}

	// Purge removes only rows older than the cutoff.
	n, err := st.PurgeWhatsAppMessages(ctx, 105)
	if err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	page, err = st.SearchWhatsAppMessages(ctx, WhatsAppSearch{Query: "reply", Limit: 50})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("purge removed the wrong rows: %#v, %v", page, err)
	}
}
