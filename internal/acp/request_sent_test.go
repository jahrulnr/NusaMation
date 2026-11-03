package acp

import (
	"bytes"
	"context"
	"testing"
	"time"
)

type memStdin struct{ bytes.Buffer }

func (*memStdin) Close() error { return nil }

func TestRequestSentRunsAfterWrite(t *testing.T) {
	in := &memStdin{}
	p := NewProcess(ProcessConfig{}, &ownerHandler{got: make(chan string, 1)})
	p.stdin = in
	sent := 0
	base := WithRequestSent(context.Background(), func() {
		sent++
		if in.Len() == 0 {
			t.Error("sent callback ran before the request was written")
		}
	})
	ctx, cancel := context.WithTimeout(base, 50*time.Millisecond)
	defer cancel()
	if err := p.Call(ctx, "session/prompt", map[string]any{}, nil); err != context.DeadlineExceeded {
		t.Fatalf("Call error = %v", err)
	}
	if sent != 1 || in.Len() == 0 {
		t.Fatalf("sent=%d written=%d", sent, in.Len())
	}
}

func TestCallWithCancelledContextWritesNothing(t *testing.T) {
	in := &memStdin{}
	p := NewProcess(ProcessConfig{}, &ownerHandler{got: make(chan string, 1)})
	p.stdin = in
	sent := 0
	ctx, cancel := context.WithCancel(WithRequestSent(context.Background(), func() { sent++ }))
	cancel()
	if err := p.Call(ctx, "session/prompt", map[string]any{}, nil); err != context.Canceled {
		t.Fatalf("Call error = %v", err)
	}
	if sent != 0 || in.Len() != 0 {
		t.Fatalf("cancelled call dispatched: sent=%d written=%d", sent, in.Len())
	}
}
