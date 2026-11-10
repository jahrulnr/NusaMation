package browser

// Stream lease + share-screen fan-out: every attached viewer receives the
// same frames/meta, one viewer (the controller) drives input, a takeover
// transfers the lease, an agent lock mutes all viewers until a human claims
// control back, and a controller disconnect hands the lease to the oldest
// surviving viewer. NUSAMATION_BROWSER_TEST=1 for the browser-backed test.

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// drainStream mimics a pane's reader: counts frames, collects meta + control
// payloads, and reports the first terminal {type:"closed"} message.
type streamSink struct {
	frames  atomic.Int64
	metas   atomic.Int64
	control chan string
	closed  chan string
}

func (sk *streamSink) run(sub *Stream) {
	control := func(b []byte) {
		var m struct {
			Type  string `json:"type"`
			Code  string `json:"code"`
			Owner string `json:"owner"`
		}
		if json.Unmarshal(b, &m) != nil {
			return
		}
		switch m.Type {
		case "meta":
			sk.metas.Add(1)
		case "control":
			select {
			case sk.control <- m.Owner:
			default:
			}
		case "closed":
			select {
			case sk.closed <- m.Code:
			default:
			}
		}
	}
	for {
		select {
		case <-sub.Frames():
			sk.frames.Add(1)
		case b := <-sub.Control():
			control(b)
		case <-sub.Done():
			for {
				select {
				case b := <-sub.Control():
					control(b)
				default:
					return
				}
			}
		}
	}
}

// TestInputLease covers the lease state machine without a browser.
func TestInputLease(t *testing.T) {
	s := &Session{status: "active"}
	a, b := newStream(), newStream()

	if s.InputAllowed(a) {
		t.Fatal("no controller, yet input allowed")
	}
	// First attach controls; the second watches.
	s.streams = []*Stream{a, b}
	s.controller = a
	if !s.InputAllowed(a) || s.InputAllowed(b) {
		t.Fatal("first stream should control, second should watch")
	}

	// Takeover transfers the lease to the claiming viewer.
	s.Takeover(b)
	if s.InputAllowed(a) || !s.InputAllowed(b) {
		t.Fatal("takeover must hand input to the claiming viewer")
	}

	// The agent lock mutes every viewer.
	s.SetAgentLock(true)
	if s.InputAllowed(b) || !s.AgentLocked() {
		t.Fatal("agent lock must mute all viewer input")
	}
	// A human takeover always preempts the agent.
	s.Takeover(a)
	if s.AgentLocked() {
		t.Fatal("human takeover must clear the agent lock")
	}
	if !s.InputAllowed(a) || s.InputAllowed(b) {
		t.Fatal("takeover during agent lock must hand input to the human")
	}

	// Controller disconnect hands the lease to the oldest surviving viewer.
	s.Detach(a)
	if !s.InputAllowed(b) {
		t.Fatal("lease must return to the oldest attached viewer")
	}
	// Last viewer leaving stops ownership.
	s.Detach(b)
	if s.InputAllowed(b) {
		t.Fatal("no viewers left — input must not be allowed")
	}
}

// TestStreamFanOutDetach exercises the slice-mutation pattern the review
// flagged: Detach must publish a fresh slice so fan-out sites that snapshot
// s.streams under the lock can iterate it safely after unlocking. Run with
// -race — the old in-place delete wrote the same array a snapshot reads.
func TestStreamFanOutDetach(t *testing.T) {
	s := &Session{status: "active"}
	for i := 0; i < 8; i++ {
		s.streams = append(s.streams, newStream())
	}
	s.controller = s.streams[0]
	subs := append([]*Stream(nil), s.streams...)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.broadcastControl()
			}
		}()
	}
	for _, sub := range subs {
		wg.Add(1)
		go func(sub *Stream) {
			defer wg.Done()
			s.Detach(sub)
		}(sub)
	}
	wg.Wait()
	if len(s.streams) != 0 || s.controller != nil {
		t.Fatal("all viewers detached — streams and controller must be empty")
	}
}

func TestSharedViewTakeover(t *testing.T) {
	if os.Getenv("NUSAMATION_BROWSER_TEST") == "" {
		t.Skip("set NUSAMATION_BROWSER_TEST=1")
	}
	m := NewManager(Options{})
	defer m.Close()
	sess, err := m.CreateSession(context.Background(), "cv_shared", htmlDataURL(
		`<html><body><button id="b" style="width:200px;height:80px" onclick="window.clicks=(window.clicks||0)+1">hit</button><script>window.clicks=0</script></body></html>`),
		800, 600)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer sess.Close()

	// Two panes attach — both keep watching, nobody is replaced.
	sub1, err := sess.Attach()
	if err != nil {
		t.Fatalf("attach1: %v", err)
	}
	sub2, err := sess.Attach()
	if err != nil {
		t.Fatalf("attach2: %v", err)
	}
	var sk1, sk2 streamSink
	sk1.control = make(chan string, 4)
	sk1.closed = make(chan string, 1)
	sk2.control = make(chan string, 4)
	sk2.closed = make(chan string, 1)
	go sk1.run(sub1)
	go sk2.run(sub2)
	time.Sleep(600 * time.Millisecond)

	// Share-screen: both viewers see frames; the joiner also saw the viewport
	// hint in its attach meta.
	if sk1.frames.Load() == 0 || sk2.frames.Load() == 0 {
		t.Fatalf("both viewers must receive frames, got %d and %d", sk1.frames.Load(), sk2.frames.Load())
	}
	if !sess.InputAllowed(sub1) || sess.InputAllowed(sub2) {
		t.Fatal("first attacher must control, joiner must watch")
	}

	// Takeover moves the lease to the second viewer.
	sess.Takeover(sub2)
	if sess.InputAllowed(sub1) || !sess.InputAllowed(sub2) {
		t.Fatal("takeover must transfer the lease")
	}
	// Control payloads ride the stream's async queue — keep draining until the
	// lease state we expect arrives (earlier values may still be in flight).
	waitOwner := func(ch chan string, want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case got := <-ch:
				if got == want {
					return
				}
			case <-time.After(20 * time.Millisecond):
			}
		}
		t.Fatalf("control owner never reached %q", want)
	}
	waitOwner(sk1.control, "other")
	waitOwner(sk2.control, "you")

	// Agent lock mutes the human controller until they take over.
	sess.SetAgentLock(true)
	if sess.InputAllowed(sub2) {
		t.Fatal("agent lock must mute viewer input")
	}
	waitOwner(sk2.control, "agent")
	sess.Takeover(sub2)
	if !sess.InputAllowed(sub2) || sess.AgentLocked() {
		t.Fatal("human takeover must reclaim input and clear the lock")
	}

	// Click through the live session path still lands on the page.
	if err := sess.Click(40, 40, "left", 1); err != nil {
		t.Fatalf("Click dispatch failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	res, err := chromedp.Call(sess.tabCtx, runtime.Evaluate, runtime.EvaluateParams{
		Expression: "window.clicks", ReturnByValue: new(bool),
	})
	if err != nil {
		t.Fatalf("eval clicks: %v", err)
	}
	if string(res.Result.Value) != "1" {
		t.Fatalf("clicks on page = %s, want 1", string(res.Result.Value))
	}

	// Controller detaches — lease hands off to the remaining viewer.
	sess.Detach(sub2)
	if !sess.InputAllowed(sub1) {
		t.Fatal("lease must return to the surviving viewer")
	}
}
