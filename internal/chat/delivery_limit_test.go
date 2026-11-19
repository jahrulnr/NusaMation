package chat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	appstore "github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

// cappedDelivery records final texts through recChannel and can fail the
// Update call, so tests can exercise the declared per-message limit and the
// delivery-failure notice.
type cappedDelivery struct {
	ch    *recChannel
	limit int
	fail  bool
}

func (d *cappedDelivery) ID() string { return "capped" }

func (d *cappedDelivery) DeliveredTextLimit() int { return d.limit }

func (d *cappedDelivery) Update(_ context.Context, text string, final bool) error {
	if final {
		d.ch.mu.Lock()
		d.ch.events = append(d.ch.events, recEvent{final: true, text: text})
		d.ch.mu.Unlock()
	}
	if d.fail {
		return errors.New("WhatsApp is not connected")
	}
	return nil
}

func newDeliveryTestService(rec *recChannel, bus *streamEventRecorder) *Service {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := channels.NewManager(events.Nop{}, logger)
	manager.Register(rec)
	return &Service{channels: manager, log: logger, bus: bus}
}

// A channel that promises the agent a large reply limit must actually
// deliver that much; the 3,500-rune default must not truncate it.
func TestChannelLimitComesFromDelivery(t *testing.T) {
	rec := &recChannel{}
	svc := newDeliveryTestService(rec, &streamEventRecorder{})
	long := strings.Repeat("x", 5000)
	a := &activeTurn{
		turn:             appstore.Turn{OriginChannel: "wa", OriginRef: `{"externalId":"chat-1"}`},
		delivery:         &cappedDelivery{ch: rec, limit: 30000},
		deliverToChannel: true,
		finalStatus:      "completed",
	}
	a.assistantAll.WriteString(long)
	svc.updateDelivery(a, true)
	got := rec.finals()
	if len(got) != 1 || got[0] != long {
		t.Fatalf("delivered %d runes, want the full 5000-rune reply", len([]rune(got[0])))
	}
}

// Channels that declare no limit keep the chat-layer default.
func TestDefaultReplyLimitStillTruncates(t *testing.T) {
	rec := &recChannel{}
	svc := newDeliveryTestService(rec, &streamEventRecorder{})
	long := strings.Repeat("x", 5000)
	a := &activeTurn{
		turn:             appstore.Turn{OriginChannel: "wa", OriginRef: `{"externalId":"chat-1"}`},
		delivery:         &cappedDelivery{ch: rec},
		deliverToChannel: true,
		finalStatus:      "completed",
	}
	a.assistantAll.WriteString(long)
	svc.updateDelivery(a, true)
	got := rec.finals()
	if want := textfmt.TruncateRunes(long, defaultReplyRunes); len(got) != 1 || got[0] != want {
		t.Fatalf("delivered %d runes, want the default %d-rune cut", len([]rune(got[0])), defaultReplyRunes)
	}
}

// A failing final send must surface in the web conversation as a notice —
// the operator cannot otherwise tell that the WhatsApp reply was lost.
func TestFailedChannelReplyEmitsNotice(t *testing.T) {
	rec := &recChannel{}
	bus := &streamEventRecorder{}
	svc := newDeliveryTestService(rec, bus)
	a := &activeTurn{
		turn:             appstore.Turn{OriginChannel: "wa", OriginRef: `{"externalId":"chat-1"}`},
		delivery:         &cappedDelivery{ch: rec, fail: true},
		deliverToChannel: true,
		finalStatus:      "completed",
	}
	a.assistantAll.WriteString("hello")
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
		t.Fatalf("no notice emitted, events = %#v", bus.events)
	}
	if notice["severity"] != "error" {
		t.Fatalf("notice severity = %v, want error", notice["severity"])
	}
	if description, _ := notice["description"].(string); !strings.Contains(description, "wa") || !strings.Contains(description, "not connected") {
		t.Fatalf("notice description = %q", description)
	}
}
