package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type blockingHandler struct {
	started chan struct{}
	release chan struct{}
}

func (h *blockingHandler) OnNotification(string, string, json.RawMessage) {
	close(h.started)
	<-h.release
}
func (*blockingHandler) OnRequest(context.Context, string, json.RawMessage) (any, *RPCError) {
	return nil, nil
}
func (*blockingHandler) OnStderr(string) {}
func (*blockingHandler) OnExit(error)    {}

func TestPromptResponseWaitsForEarlierUpdates(t *testing.T) {
	h := &blockingHandler{started: make(chan struct{}), release: make(chan struct{})}
	p := NewProcess(ProcessConfig{}, h)
	p.pending["1"] = &pendingCall{method: "session/prompt", ch: make(chan callResult, 1)}
	result := p.pending["1"].ch
	go p.notificationLoop()
	defer func() { close(p.done); <-p.notifDone }()
	p.handleMessage(rpcMessage{Method: "session/update", Params: json.RawMessage(`{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk"}}`)})
	<-h.started
	p.handleMessage(rpcMessage{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"stopReason":"end_turn"}`)})
	select {
	case <-result:
		t.Fatal("prompt completed before update was handled")
	default:
	}
	close(h.release)
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("prompt response did not arrive")
	}
}

type ownerHandler struct{ got chan string }

func (h *ownerHandler) OnNotification(string, string, json.RawMessage) {}
func (h *ownerHandler) OnRequest(ctx context.Context, _ string, _ json.RawMessage) (any, *RPCError) {
	h.got <- RequestOwner(ctx)
	return map[string]any{}, nil
}
func (*ownerHandler) OnStderr(string) {}
func (*ownerHandler) OnExit(error)    {}

func TestRequestScopedInputUsesOriginatingConversation(t *testing.T) {
	h := &ownerHandler{got: make(chan string, 1)}
	p := NewProcess(ProcessConfig{}, h)
	p.pending["12"] = &pendingCall{method: "session/new", ch: make(chan callResult, 1), ctx: WithRequestOwner(context.Background(), "conversation-1")}
	p.handleRequest(rpcMessage{ID: json.RawMessage(`"question"`), Method: "elicitation/create", Params: json.RawMessage(`{"requestId":12,"mode":"form"}`)})
	if got := <-h.got; got != "conversation-1" {
		t.Fatalf("owner = %q", got)
	}
}
