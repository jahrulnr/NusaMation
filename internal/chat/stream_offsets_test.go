package chat

import (
	"testing"

	"github.com/jahrulnr/nusamation/internal/events"
)

type streamEventRecorder struct{ events []events.Event }

func (r *streamEventRecorder) Broadcast(event events.Event) { r.events = append(r.events, event) }

func TestStreamOffsetsAndLiveSnapshot(t *testing.T) {
	a := &activeTurn{conversationID: "room", assistantID: "answer", thoughtID: "thought"}
	if _, _, offset := a.appendAssistant("Hi 🙂"); offset != 0 {
		t.Fatalf("first offset = %d", offset)
	}
	if _, _, offset := a.appendAssistant("é"); offset != len("Hi 🙂") {
		t.Fatalf("assistant offset = %d, want %d", offset, len("Hi 🙂"))
	}
	if _, _, offset := a.appendThought("思"); offset != 0 {
		t.Fatalf("first thought offset = %d", offset)
	}
	if _, _, offset := a.appendThought("考"); offset != len("思") {
		t.Fatalf("thought offset = %d, want %d", offset, len("思"))
	}
	svc := &Service{active: map[string]*activeTurn{"room": a}}
	got := svc.ActiveMessageTexts("room")
	if got["answer"] != "Hi 🙂é" || got["thought"] != "思考" {
		t.Fatalf("live snapshot = %#v", got)
	}
}

func TestDeltaEventsCarryByteOffsets(t *testing.T) {
	recorder := &streamEventRecorder{}
	svc := &Service{bus: recorder}
	a := &activeTurn{conversationID: "room", assistantID: "answer"}
	svc.appendAgentText(a, "", "A🙂")
	svc.appendAgentText(a, "", "é")
	if len(recorder.events) != 2 {
		t.Fatalf("events = %d, want 2", len(recorder.events))
	}
	first := recorder.events[0].Data.(map[string]any)
	second := recorder.events[1].Data.(map[string]any)
	if first["offsetBytes"] != 0 || second["offsetBytes"] != len("A🙂") {
		t.Fatalf("offsets = %v, %v", first["offsetBytes"], second["offsetBytes"])
	}
}
