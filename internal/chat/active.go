package chat

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/store"
)

const maxStreamBytes = 4 << 20

type toolState struct {
	MessageID string
	Title     string
	Name      string
	Kind      string
	Status    string
	Content   json.RawMessage
	Locations json.RawMessage
	RawInput  json.RawMessage
	RawOutput json.RawMessage
	Meta      json.RawMessage
}

func (t *toolState) json() json.RawMessage {
	m := map[string]any{"toolCallId": ""}
	if t.Title != "" {
		m["title"] = t.Title
	}
	if t.Name != "" {
		m["name"] = t.Name
	}
	if t.Kind != "" {
		m["kind"] = t.Kind
	}
	if t.Status != "" {
		m["status"] = t.Status
	}
	if len(t.Content) > 0 {
		m["content"] = t.Content
	}
	if len(t.Locations) > 0 {
		m["locations"] = t.Locations
	}
	if len(t.RawInput) > 0 {
		m["rawInput"] = t.RawInput
	}
	if len(t.RawOutput) > 0 {
		m["rawOutput"] = t.RawOutput
	}
	if len(t.Meta) > 0 {
		m["_meta"] = t.Meta
	}
	b, _ := json.Marshal(m)
	return b
}

func (t *toolState) display() string {
	if t.Title != "" {
		return t.Title
	}
	if t.Name != "" {
		return t.Name
	}
	return "Tool call"
}

type activeTurn struct {
	mu sync.Mutex

	conversationID string
	turn           store.Turn
	conv           store.Conversation
	agent          store.Agent
	user           store.Message
	session        *agent.Session

	ctx       context.Context
	cancel    context.CancelFunc
	flushStop chan struct{}

	assistantID    string
	assistantACPID string
	assistant      strings.Builder
	assistantDirty bool
	assistantDone  bool

	thoughtID    string
	thought      strings.Builder
	thoughtDirty bool
	thoughtDone  bool

	tools     map[string]*toolState
	toolCount int

	delivery     channels.Delivery
	deliveryText string
	deliveryAt   time.Time

	approvals       map[string]bool
	recapApplied    bool
	cancelRequested bool
	finished        bool
	finalStatus     string
	finalError      string
	noOutput        bool
	finalDelivery   bool
	startedAt       time.Time
	lastFlush       time.Time
}

func (a *activeTurn) appendAssistant(delta string) (text string, first bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.assistant.Len() >= maxStreamBytes {
		return "", false
	}
	if delta == "" {
		return "", false
	}
	first = a.assistant.Len() == 0
	remaining := maxStreamBytes - a.assistant.Len()
	if len(delta) > remaining {
		delta = delta[:remaining]
	}
	a.assistant.WriteString(delta)
	a.assistantDirty = true
	return delta, first
}

func (a *activeTurn) appendThought(delta string) (text string, first bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.thought.Len() >= maxStreamBytes/2 {
		return "", false
	}
	if delta == "" {
		return "", false
	}
	first = a.thought.Len() == 0
	remaining := maxStreamBytes/2 - a.thought.Len()
	if len(delta) > remaining {
		delta = delta[:remaining]
	}
	a.thought.WriteString(delta)
	a.thoughtDirty = true
	return delta, first
}

func (a *activeTurn) assistantSnapshot() (id, text string, dirty bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	dirty = a.assistantDirty
	a.assistantDirty = false
	return a.assistantID, a.assistant.String(), dirty
}

func (a *activeTurn) thoughtSnapshot() (id, text string, dirty bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	dirty = a.thoughtDirty
	a.thoughtDirty = false
	return a.thoughtID, a.thought.String(), dirty
}

func (a *activeTurn) setDelivery(d channels.Delivery) {
	a.mu.Lock()
	a.delivery = d
	a.mu.Unlock()
}

func (a *activeTurn) getTool(id string) *toolState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tools[id]
}

func (a *activeTurn) ensureTool(id string) *toolState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.tools[id]; ok {
		return t
	}
	t := &toolState{Status: "pending"}
	a.tools[id] = t
	a.toolCount++
	return t
}
