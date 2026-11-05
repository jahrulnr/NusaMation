package whatsapp

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestKeepTypingRefreshesUntilStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	states := make(chan types.ChatPresence, 8)
	go keepTyping(ctx, 15*time.Millisecond, func(state types.ChatPresence) {
		states <- state
	})

	wait := time.After(time.Second)
	var got []types.ChatPresence
	for len(got) < 2 {
		select {
		case state := <-states:
			got = append(got, state)
		case <-wait:
			t.Fatalf("typing beats = %v", got)
		}
	}
	if got[0] != types.ChatPresenceComposing || got[1] != types.ChatPresenceComposing {
		t.Fatalf("beats = %v", got)
	}
	cancel()
	select {
	case state := <-states:
		if state != types.ChatPresencePaused {
			t.Fatalf("after stop = %v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("typing did not pause")
	}
}
