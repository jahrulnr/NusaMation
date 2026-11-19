package chat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/store"
)

// unreachableChannel refuses both a streaming delivery and a plain message,
// the way WhatsApp does while the account is disconnected.
type unreachableChannel struct{ name string }

func (c *unreachableChannel) Name() string                { return c.name }
func (c *unreachableChannel) Label() string               { return c.name }
func (c *unreachableChannel) Start(context.Context) error { return nil }
func (c *unreachableChannel) Stop() error                 { return nil }
func (c *unreachableChannel) Status() channels.Status     { return channels.Status{Name: c.name} }
func (c *unreachableChannel) Begin(context.Context, channels.Target, string) (channels.Delivery, error) {
	return nil, errors.New("WhatsApp channel is disabled")
}
func (c *unreachableChannel) Notify(context.Context, channels.Target, string) error {
	return errors.New("WhatsApp is not connected")
}

// A final reply that cannot start a channel delivery and whose plain-message
// fallback also fails must still reach the web conversation as a notice. Today
// the failure is only written to the log, so the operator never learns the
// reply was lost.
func TestUnstartedDeliveryFailureEmitsNotice(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := channels.NewManager(events.Nop{}, logger)
	manager.Register(&unreachableChannel{name: "wa"})
	bus := &streamEventRecorder{}
	svc := &Service{channels: manager, log: logger, bus: bus}
	a := &activeTurn{
		turn:             store.Turn{OriginChannel: "wa", OriginRef: `{"externalId":"chat-1"}`},
		deliverToChannel: true,
		finalStatus:      "completed",
	}
	a.assistantAll.WriteString("hello")
	a.delivery = svc.openDelivery(a, "On it…")
	svc.updateDelivery(a, true)
	var notice map[string]any
	for _, ev := range bus.events {
		if ev.Type != "notice" || ev.ConversationID != a.conversationID {
			continue
		}
		if data, ok := ev.Data.(map[string]any); ok {
			notice = data
		}
	}
	if notice == nil {
		t.Fatalf("no notice emitted for an undeliverable reply, events = %#v", bus.events)
	}
	if notice["severity"] != "error" {
		t.Fatalf("notice severity = %v, want error", notice["severity"])
	}
}
