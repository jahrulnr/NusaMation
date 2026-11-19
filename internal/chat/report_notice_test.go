package chat

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/store"
)

// ReportNotice surfaces a channel-side incident on the conversation that owns
// the chat, without starting a turn; an unlinked chat stays silent.
func TestReportNoticeOnOwningConversation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "notice-agent", Command: "true", Cwd: t.TempDir(), Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "Daily", t.TempDir(), "whatsapp", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MoveChannelLink(ctx, store.ChannelLink{
		ConversationID: conv.ID, Channel: "wa", ExternalID: "62@s.whatsapp.net",
	}); err != nil {
		t.Fatal(err)
	}
	logger := testLogger()
	mgr := channels.NewManager(events.Nop{}, logger)
	mgr.Register(&recChannel{})
	bus := &streamEventRecorder{}
	svc := &Service{store: st, channels: mgr, bus: bus, log: logger}

	in := channels.Inbound{Channel: "wa", ExternalID: "62@s.whatsapp.net", SenderID: "62@s.whatsapp.net"}
	if err := svc.ReportNotice(ctx, in, "Undecryptable WhatsApp message", "content missing"); err != nil {
		t.Fatal(err)
	}
	var notice map[string]any
	for _, ev := range bus.events {
		if ev.Type == "notice" && ev.ConversationID == conv.ID {
			if data, ok := ev.Data.(map[string]any); ok {
				notice = data
			}
		}
	}
	if notice == nil {
		t.Fatalf("no notice on the owning conversation, events = %#v", bus.events)
	}
	if notice["severity"] != "info" {
		t.Fatalf("severity = %v", notice["severity"])
	}

	// An unlinked chat stays log-only.
	other := channels.Inbound{Channel: "wa", ExternalID: "62999@s.whatsapp.net"}
	if err := svc.ReportNotice(ctx, other, "Undecryptable WhatsApp message", "content missing"); err == nil {
		t.Fatal("unlinked chat notice should fail, not disappear")
	}
	for _, ev := range bus.events {
		if ev.Type == "notice" && ev.ConversationID != conv.ID {
			t.Fatalf("notice emitted for an unlinked chat: %#v", ev)
		}
	}
}
