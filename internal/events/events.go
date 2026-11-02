package events

import "time"

type Event struct {
	ID             uint64 `json:"id"`
	Type           string `json:"type"`
	ConversationID string `json:"conversationId,omitempty"`
	At             int64  `json:"at"`
	Data           any    `json:"data,omitempty"`
}

type Broadcaster interface {
	Broadcast(event Event)
}

type Nop struct{}

func (Nop) Broadcast(Event) {}

func New(typ, conversationID string, data any) Event {
	return Event{Type: typ, ConversationID: conversationID, At: time.Now().UnixMilli(), Data: data}
}
