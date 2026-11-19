package whatsapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// pacerChannel is a bare channel wired only for pacer tests: the run closures
// are the sends, so no whatsmeow client is involved.
func pacerChannel(t *testing.T, settings Settings) *Channel {
	t.Helper()
	c := &Channel{log: slog.New(slog.NewTextHandler(io.Discard, nil)), settings: settings}
	c.pacer = *newPacer()
	return c
}

type sendLog struct {
	mu   sync.Mutex
	at   []time.Time
	text []string
	err  error
}

func (l *sendLog) record(text string) {
	l.mu.Lock()
	l.at = append(l.at, time.Now())
	l.text = append(l.text, text)
	l.mu.Unlock()
}

func (l *sendLog) all() ([]time.Time, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]time.Time(nil), l.at...), append([]string(nil), l.text...)
}

func (l *sendLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.at)
}

func send(c *Channel, chat, text string, log *sendLog) error {
	return c.pacerSend(context.Background(), chat, func(context.Context) error {
		log.record(text)
		return nil
	})
}

// waitClaimed waits until the chat's worker has taken the queue head, so a
// subsequent offer lands in the waiting queue.
func waitClaimed(t *testing.T, c *Channel, chat string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.pacer.mu.Lock()
		q := c.pacer.queues[chat]
		claimed := q != nil && len(q.tasks) == 0
		c.pacer.mu.Unlock()
		if claimed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker never claimed the queue head")
}

// Two consecutive sends to the same chat keep at least the configured gap;
// the second one waits in the queue instead of being dropped.
func TestPacerGapsSameChat(t *testing.T) {
	c := pacerChannel(t, DefaultSettings()) // SendPacing 5, QueueCap 20
	log := &sendLog{}
	start := time.Now()
	if err := send(c, "chatA", "one", log); err != nil {
		t.Fatal(err)
	}
	if err := send(c, "chatA", "two", log); err != nil {
		t.Fatal(err)
	}
	ats, texts := log.all()
	if len(ats) != 2 || texts[0] != "one" || texts[1] != "two" {
		t.Fatalf("sends = %v %v", texts, ats)
	}
	if gap := ats[1].Sub(ats[0]); gap < 5*time.Second {
		t.Fatalf("gap = %v, want at least 5s", gap)
	}
	if waited := ats[1].Sub(start); waited > 8*time.Second {
		t.Fatalf("second send waited %v, too long", waited)
	}
}

// Different chats never wait for each other.
func TestPacerIndependentPerChat(t *testing.T) {
	c := pacerChannel(t, DefaultSettings())
	log := &sendLog{}
	done := make(chan error, 2)
	go func() { done <- send(c, "chatA", "a", log) }()
	go func() { done <- send(c, "chatB", "b", log) }()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if log.count() != 2 {
		t.Fatalf("sends = %d", log.count())
	}
	_, texts := log.all()
	if texts[0] == texts[1] {
		t.Fatalf("both chats sent in order %v", texts)
	}
	if ats, _ := log.all(); ats[1].Sub(ats[0]) > time.Second {
		t.Fatalf("chats paced each other: gap %v", ats[1].Sub(ats[0]))
	}
}

// FIFO per chat, even when tasks arrive from different goroutines while a
// send is already running.
func TestPacerFIFO(t *testing.T) {
	c := pacerChannel(t, Settings{SendPacing: 0, QueueCap: 20})
	log := &sendLog{}
	release := make(chan struct{})
	headDone := make(chan error, 1)
	go func() {
		headDone <- c.pacerSend(context.Background(), "chat", func(context.Context) error {
			<-release
			log.record("head")
			return nil
		})
	}()
	waitClaimed(t, c, "chat")
	// Enqueue in a known order while the head is running.
	tasks := make([]*sendTask, 0, 3)
	for _, text := range []string{"1", "2", "3"} {
		task := &sendTask{
			ctx:  context.Background(),
			done: make(chan error, 1),
			run: func(context.Context) error {
				log.record(text)
				return nil
			},
		}
		if err := c.pacerOffer("chat", task); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	close(release)
	if err := <-headDone; err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if err := <-task.done; err != nil {
			t.Fatal(err)
		}
	}
	_, texts := log.all()
	if strings.Join(texts, ",") != "head,1,2,3" {
		t.Fatalf("queue order broken: %v", texts)
	}
}

// Beyond the queue cap the send is refused, not queued.
func TestPacerQueueFull(t *testing.T) {
	// Pacing 0 isolates the cap: only the queue depth can reject.
	c := pacerChannel(t, Settings{SendPacing: 0, QueueCap: 2})
	log := &sendLog{}
	release := make(chan struct{})
	headDone := make(chan error, 1)
	go func() {
		headDone <- c.pacerSend(context.Background(), "chat", func(context.Context) error {
			<-release
			log.record("head")
			return nil
		})
	}()
	waitClaimed(t, c, "chat")
	// Two queued behind the long-running head: the next offer hits the cap.
	queued := make([]*sendTask, 0, 2)
	for _, text := range []string{"queued-1", "queued-2"} {
		task := &sendTask{
			ctx:  context.Background(),
			done: make(chan error, 1),
			run: func(context.Context) error {
				log.record(text)
				return nil
			},
		}
		if err := c.pacerOffer("chat", task); err != nil {
			t.Fatal(err)
		}
		queued = append(queued, task)
	}
	over := &sendTask{ctx: context.Background(), done: make(chan error, 1), run: func(context.Context) error {
		log.record("over")
		return nil
	}}
	if err := c.pacerOffer("chat", over); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("error = %v, want ErrQueueFull", err)
	}
	close(release)
	if err := <-headDone; err != nil {
		t.Fatal(err)
	}
	for _, task := range queued {
		if err := <-task.done; err != nil {
			t.Fatal(err)
		}
	}
	if _, texts := log.all(); strings.Join(texts, ",") != "head,queued-1,queued-2" {
		t.Fatalf("sends = %v", texts)
	}
}

// A caller whose context is already done when its task reaches the queue head
// is skipped without sending, and the queue keeps flowing.
func TestPacerSkipsCanceledCaller(t *testing.T) {
	c := pacerChannel(t, Settings{SendPacing: 0, QueueCap: 20})
	log := &sendLog{}
	release := make(chan struct{})
	headDone := make(chan error, 1)
	go func() {
		headDone <- c.pacerSend(context.Background(), "chat", func(context.Context) error {
			<-release
			log.record("head")
			return nil
		})
	}()
	waitClaimed(t, c, "chat")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.pacerSend(ctx, "chat", func(context.Context) error {
			log.record("canceled")
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-headDone; err != nil {
		t.Fatal(err)
	}
	if err := send(c, "chat", "after", log); err != nil {
		t.Fatal(err)
	}
	if _, texts := log.all(); strings.Join(texts, ",") != "head,after" {
		t.Fatalf("sends = %v, want the canceled task skipped", texts)
	}
}

// A failed send does not pace the next one.
func TestPacerFailedSendDoesNotDelay(t *testing.T) {
	c := pacerChannel(t, Settings{SendPacing: 5, QueueCap: 20})
	log := &sendLog{}
	failing := &sendLog{err: errors.New("not connected")}
	if err := c.pacerSend(context.Background(), "chat", func(context.Context) error { return failing.err }); err == nil {
		t.Fatal("expected the failure to surface")
	}
	start := time.Now()
	if err := send(c, "chat", "next", log); err != nil {
		t.Fatal(err)
	}
	if ats, _ := log.all(); ats[0].Sub(start) > 500*time.Millisecond {
		t.Fatalf("send after a failure waited %v", ats[0].Sub(start))
	}
}

// Pacing disabled sends back-to-back immediately.
func TestPacerZeroDisables(t *testing.T) {
	c := pacerChannel(t, Settings{SendPacing: 0, QueueCap: 20})
	log := &sendLog{}
	if err := send(c, "chat", "one", log); err != nil {
		t.Fatal(err)
	}
	if err := send(c, "chat", "two", log); err != nil {
		t.Fatal(err)
	}
	if ats, _ := log.all(); ats[1].Sub(ats[0]) > 500*time.Millisecond {
		t.Fatalf("pacing 0 still delayed: %v", ats[1].Sub(ats[0]))
	}
}

// Channel stop fails every queued task so callers' failure paths run.
func TestPacerStopFailsQueued(t *testing.T) {
	c := pacerChannel(t, Settings{SendPacing: 60, QueueCap: 20})
	log := &sendLog{}
	release := make(chan struct{})
	headDone := make(chan error, 1)
	go func() {
		headDone <- c.pacerSend(context.Background(), "chat", func(context.Context) error {
			<-release
			log.record("head")
			return nil
		})
	}()
	waitClaimed(t, c, "chat")
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.pacerSend(context.Background(), "chat", func(context.Context) error {
			log.record("queued")
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)
	c.pacerStop()
	if err := <-errCh; !errors.Is(err, errPacerStopped) {
		t.Fatalf("error = %v, want errPacerStopped", err)
	}
	close(release)
	if err := <-headDone; err != nil {
		t.Fatal(err)
	}
	if _, texts := log.all(); len(texts) != 1 || texts[0] != "head" {
		t.Fatalf("sends = %v, want only the head", texts)
	}
}
