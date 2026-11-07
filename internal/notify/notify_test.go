package notify

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/store"
)

type capture struct {
	mu  sync.Mutex
	got []Notification
}

func (c *capture) Name() string    { return "capture" }
func (c *capture) Available() bool { return true }
func (c *capture) Notify(_ context.Context, n Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, n)
	return nil
}

func (c *capture) list() []Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Notification(nil), c.got...)
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func waitFor(t *testing.T, c *capture, n int) {
	t.Helper()
	for range 200 {
		if len(c.list()) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d notifications, got %d", n, len(c.list()))
}

func TestApprovalNotification(t *testing.T) {
	st := openStore(t)
	cap := &capture{}
	b := Wrap(events.Nop{}, Options{Sender: cap, Store: st})

	b.Broadcast(events.New("approval.created", "cv1", map[string]any{
		"approval": map[string]any{
			"id": "ap1", "status": "pending", "title": "Run rm -rf build/",
			"risk": "high", "conversationTitle": "Deploy", "agentName": "Devin",
		},
	}))
	waitFor(t, cap, 1)
	n := cap.list()[0]
	if n.Urgency != UrgencyCritical || n.Icon != "dialog-question" || n.Title != "Approval needed" {
		t.Fatalf("unexpected notification: %+v", n)
	}
	if got := n.Body; got == "" || !strings.Contains(got, "Run rm -rf build/") || !strings.Contains(got, "risk: high") {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestTurnCompletedNotifications(t *testing.T) {
	st := openStore(t)
	cap := &capture{}
	b := Wrap(events.Nop{}, Options{Sender: cap, Store: st})

	b.Broadcast(events.New("turn.completed", "cv1", map[string]any{"status": "completed"}))
	b.Broadcast(events.New("turn.completed", "cv1", map[string]any{"status": "cancelled"}))
	b.Broadcast(events.New("turn.completed", "cv2", map[string]any{"status": "error", "error": "agent process exited"}))
	waitFor(t, cap, 2)

	var done, fail *Notification
	for i := range cap.list() {
		n := cap.list()[i]
		switch n.Title {
		case "Agent finished":
			done = &n
		case "Agent error":
			fail = &n
		}
	}
	if done == nil || done.Urgency != UrgencyNormal {
		t.Fatalf("completed mapping wrong: %+v", done)
	}
	if fail == nil || fail.Urgency != UrgencyCritical || !strings.Contains(fail.Body, "agent process exited") {
		t.Fatalf("error mapping wrong: %+v", fail)
	}
}

func TestNotificationsDisabled(t *testing.T) {
	st := openStore(t)
	if err := st.SetSetting(context.Background(), "notify.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	cap := &capture{}
	b := Wrap(events.Nop{}, Options{Sender: cap, Store: st})
	b.Broadcast(events.New("turn.completed", "cv1", map[string]any{"status": "completed"}))
	for range 40 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(cap.list()) != 0 {
		t.Fatalf("notification sent while disabled: %+v", cap.list())
	}
	if b.Enabled(context.Background()) {
		t.Fatal("Enabled should be false after setting")
	}
}

func TestMute(t *testing.T) {
	st := openStore(t)
	cap := &capture{}
	b := Wrap(events.Nop{}, Options{Sender: cap, Store: st})
	b.Mute()
	b.Broadcast(events.New("approval.created", "cv1", map[string]any{
		"approval": map[string]any{"id": "ap1", "status": "pending", "title": "x"},
	}))
	time.Sleep(50 * time.Millisecond)
	if len(cap.list()) != 0 {
		t.Fatal("muted broadcaster still notified")
	}
}
