package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/store"
)

// A delivered channel reply records the WhatsApp message id on the turn's
// final assistant message, and incoming receipts patch the stored state and
// republish the message.
func TestChannelMessageIDAndReceiptFlow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "receipt-agent", Command: "true", Cwd: t.TempDir(), Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "t", t.TempDir(), "whatsapp", "", "")
	if err != nil {
		t.Fatal(err)
	}
	userMsg := &store.Message{ConversationID: conv.ID, Role: "user", Text: "go"}
	if err := st.AppendMessage(ctx, userMsg); err != nil {
		t.Fatal(err)
	}
	turn, err := st.CreateTurn(ctx, conv.ID, userMsg.ID, "whatsapp", `{"externalId":"62@s.whatsapp.net"}`)
	if err != nil {
		t.Fatal(err)
	}
	reply := &store.Message{ConversationID: conv.ID, TurnID: turn.ID, Role: "assistant", Kind: "text", Status: "complete", Text: "answer"}
	if err := st.AppendMessage(ctx, reply); err != nil {
		t.Fatal(err)
	}
	bus := &streamEventRecorder{}
	svc := &Service{store: st, bus: bus, log: testLogger()}
	svc.rememberChannelMessage(ctx, conv.ID, reply.ID, "WA-MSG-1")

	got, err := st.GetMessage(ctx, reply.ID)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(got.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["channelMessageId"] != "WA-MSG-1" {
		t.Fatalf("metadata = %#v, want channelMessageId WA-MSG-1", meta)
	}

	// Receipts flow delivered → read and never downgrade.
	if err := svc.ReportReceipt(context.Background(), "62@s.whatsapp.net", []string{"WA-MSG-1"}, "delivered"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetMessage(ctx, reply.ID)
	meta = map[string]any{}
	_ = json.Unmarshal(got.Metadata, &meta)
	if meta["channelReceipt"] != "delivered" {
		t.Fatalf("receipt = %v, want delivered", meta["channelReceipt"])
	}
	if err := svc.ReportReceipt(context.Background(), "62@s.whatsapp.net", []string{"WA-MSG-1"}, "read"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReportReceipt(context.Background(), "62@s.whatsapp.net", []string{"WA-MSG-1"}, "delivered"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetMessage(ctx, reply.ID)
	meta = map[string]any{}
	_ = json.Unmarshal(got.Metadata, &meta)
	if meta["channelReceipt"] != "read" {
		t.Fatalf("receipt = %v, want read to stick", meta["channelReceipt"])
	}
	// The republished message carried the receipt to the web.
	found := false
	for _, ev := range bus.events {
		if ev.Type == "message.updated" && ev.ConversationID == conv.ID {
			if m, ok := ev.Data.(store.Message); ok && strings.Contains(string(m.Metadata), "read") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no republished message with the receipt, events = %#v", bus.events)
	}
	// Unknown ids are a no-op.
	if err := svc.ReportReceipt(context.Background(), "62@s.whatsapp.net", []string{"GHOST"}, "read"); err != nil {
		t.Fatal(err)
	}
}
