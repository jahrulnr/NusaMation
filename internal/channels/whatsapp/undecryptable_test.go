package whatsapp

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/jahrulnr/nusamation/internal/channels"
)

type noticeRecordingHandler struct {
	mu       sync.Mutex
	notices  int
	lastText string
}

func (h *noticeRecordingHandler) HandleInbound(context.Context, channels.Inbound) error { return nil }
func (h *noticeRecordingHandler) ReportNotice(_ context.Context, in channels.Inbound, title, description string) error {
	h.mu.Lock()
	h.notices++
	h.lastText = title + ": " + description
	h.mu.Unlock()
	return nil
}

func (h *noticeRecordingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.notices
}

// An undecryptable message surfaces once per chat per minute on the handler's
// notice channel — never as a turn, never twice in a row.
func TestUndecryptableReportsOncePerChat(t *testing.T) {
	c, _ := newGateChannel(t)
	handler := &noticeRecordingHandler{}
	c.handler = handler

	info := types.MessageInfo{
		Chat:   types.JID{User: "62", Server: types.DefaultUserServer},
		Sender: types.JID{User: "62", Server: types.DefaultUserServer},
		ID:     types.MessageID("u1"), PushName: "Tester",
	}
	c.handleEvent(&events.UndecryptableMessage{Info: info})
	c.handleEvent(&events.UndecryptableMessage{Info: info}) // inside the throttle window
	if got := handler.count(); got != 1 {
		t.Fatalf("notices = %d, want 1", got)
	}
	// A different chat reports independently.
	other := info
	other.Chat = types.JID{User: "63", Server: types.DefaultUserServer}
	other.Sender = other.Chat
	c.handleEvent(&events.UndecryptableMessage{Info: other})
	if got := handler.count(); got != 2 {
		t.Fatalf("notices after other chat = %d, want 2", got)
	}
	// Push the last timestamp back one minute: the same chat reports again.
	c.noticeMu.Lock()
	c.noticeAt[info.Chat.String()] = time.Now().Add(-2 * time.Minute)
	c.noticeMu.Unlock()
	c.handleEvent(&events.UndecryptableMessage{Info: info})
	if got := handler.count(); got != 3 {
		t.Fatalf("notices after the window = %d, want 3", got)
	}
}
