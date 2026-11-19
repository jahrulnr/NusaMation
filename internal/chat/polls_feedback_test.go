package chat

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/store"
)

type notifyRecordingChannel struct {
	fakePollChannel
	mu      sync.Mutex
	notes   []string
	targets []string
}

func (f *notifyRecordingChannel) Notify(_ context.Context, target channels.Target, text string) error {
	f.mu.Lock()
	f.notes = append(f.notes, text)
	f.targets = append(f.targets, target.ExternalID)
	f.mu.Unlock()
	return nil
}

func (f *notifyRecordingChannel) replied() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.notes...)
}

// A vote on one of our polls whose pending request is gone gets one short
// feedback instead of vanishing.
func TestStalePollVoteGetsFeedback(t *testing.T) {
	svc := testPollService()
	mgr := channels.NewManager(events.Nop{}, svc.log)
	fc := &notifyRecordingChannel{name: "whatsapp"}
	mgr.Register(fc)
	svc.channels = mgr
	if err := svc.resolvePollVote(context.Background(), channels.Inbound{
		Channel: "whatsapp", ExternalID: "62@s.whatsapp.net",
		PollVote: &channels.PollVote{PollMessageID: "GHOST", Selected: []string{"Allow once"}},
	}); err != nil {
		t.Fatal(err)
	}
	notes := fc.replied()
	if len(notes) != 1 || !strings.Contains(notes[0], "already ended") {
		t.Fatalf("feedback = %#v", notes)
	}
	if fc.targets[0] != "62@s.whatsapp.net" {
		t.Fatalf("feedback target = %q", fc.targets[0])
	}
}

// In a group, another participant's tap on an approval poll must not
// broadcast the guard message into the room.
func TestNonRequesterGroupVoteStaysQuiet(t *testing.T) {
	svc := testPollService()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc.store = st
	mgr := channels.NewManager(events.Nop{}, svc.log)
	fc := &notifyRecordingChannel{name: "whatsapp"}
	mgr.Register(fc)
	svc.channels = mgr
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Fake", Slug: "fake-poll-quiet", Command: "true", Cwd: t.TempDir(), Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(context.Background(), ag.ID, "t", t.TempDir(), "whatsapp", "", "")
	if err != nil {
		t.Fatal(err)
	}
	userMsg := &store.Message{ConversationID: conv.ID, Role: "user", Text: "go"}
	if err := st.AppendMessage(context.Background(), userMsg); err != nil {
		t.Fatal(err)
	}
	turn, err := st.CreateTurn(context.Background(), conv.ID, userMsg.ID, "whatsapp", `{"senderId":"owner"}`)
	if err != nil {
		t.Fatal(err)
	}
	svc.registerPollWaiter(&pollWaiter{
		channel: "whatsapp", pollMessageID: "P9", conversationID: conv.ID,
		kind: "approval", approvalID: "ap9", turnID: turn.ID,
		options:   map[string]string{"Allow once": "o1"},
		createdAt: time.Now(),
	})
	if err := svc.resolvePollVote(context.Background(), channels.Inbound{
		Channel: "whatsapp", ExternalID: "grp@g.us", SenderID: "intruder", IsGroup: true,
		PollVote: &channels.PollVote{PollMessageID: "P9", Selected: []string{"Allow once"}},
	}); err != nil {
		t.Fatal(err)
	}
	if notes := fc.replied(); len(notes) != 0 {
		t.Fatalf("group guard message leaked: %#v", notes)
	}
	if svc.pollWaiters[pollWaiterKey("whatsapp", "P9")] == nil {
		t.Fatal("guard-rejected vote consumed the waiter")
	}
}
