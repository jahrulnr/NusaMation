package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
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

type silentHandler struct{}

func (silentHandler) OnNotification(string, string, json.RawMessage) {}
func (silentHandler) OnRequest(context.Context, string, json.RawMessage) (any, *RPCError) {
	return map[string]any{}, nil
}
func (silentHandler) OnStderr(string) {}
func (silentHandler) OnExit(error)    {}

func TestStartIncludesStderrWhenProcessExits(t *testing.T) {
	_, err := Start(context.Background(), ClientConfig{
		ProcessConfig: ProcessConfig{Command: "sh", Args: []string{"-c", "echo 'TERM is dumb' >&2; exit 1"}},
	}, silentHandler{})
	if err == nil || !strings.Contains(err.Error(), "TERM is dumb") {
		t.Fatalf("err = %v", err)
	}
}

// The exit path drains stderr before reporting the death, so lines written
// right up to the process exit always land in the ring.
func TestStderrWrittenBeforeExitIsCaptured(t *testing.T) {
	p := NewProcess(ProcessConfig{
		Command: "sh",
		Args:    []string{"-c", "printf 'first line\\nlast line\\n' >&2; exit 1"},
	}, silentHandler{})
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	got := strings.Join(p.StderrLog(), "\n")
	if !strings.Contains(got, "first line") || !strings.Contains(got, "last line") {
		t.Fatalf("stderr = %q", got)
	}
}

// A Call racing the process exit must get the normalized error — either
// ErrProcessExited or the RPC error the exit path fails pending calls with —
// never a raw os.PathError like `write |1: file already closed`.
func TestCallRacingProcessExitNeverReturnsPipeError(t *testing.T) {
	for i := 0; i < 20; i++ {
		p := NewProcess(ProcessConfig{
			Command: "sh",
			Args:    []string{"-c", "exit 1"},
		}, silentHandler{})
		if err := p.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		err := p.Call(context.Background(), "initialize", map[string]any{}, nil)
		if err == nil {
			t.Fatal("call succeeded against an exiting process")
		}
		var rpcErr *RPCError
		if !errors.Is(err, ErrProcessExited) && !errors.As(err, &rpcErr) {
			t.Fatalf("call returned %T: %v", err, err)
		}
		<-p.Done()
	}
}

type failWriter struct{ err error }

func (w *failWriter) Write([]byte) (int, error) { return 0, w.err }
func (*failWriter) Close() error                { return nil }

// Pipe write failures after the far end went away surface as
// ErrProcessExited, not the os.PathError the os.Pipe produced.
func TestWriteErrorIsNormalized(t *testing.T) {
	p := NewProcess(ProcessConfig{}, silentHandler{})
	for _, pipeErr := range []error{
		&os.PathError{Op: "write", Path: "|1", Err: syscall.EPIPE},
		&os.PathError{Op: "write", Path: "|1", Err: os.ErrClosed},
	} {
		p.stdin = &failWriter{err: pipeErr}
		if err := p.Notify("test/ping", nil); !errors.Is(err, ErrProcessExited) {
			t.Fatalf("pipeErr %v: Notify = %v", pipeErr, err)
		}
	}
}

// Closing with a call in flight resolves the call and lets every process
// goroutine wind down.
func TestCloseDuringInflightCall(t *testing.T) {
	p := NewProcess(ProcessConfig{
		Command: "sh",
		Args:    []string{"-c", "while read -r x; do :; done"},
	}, silentHandler{})
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sent := make(chan struct{})
	ctx := WithRequestSent(context.Background(), func() { close(sent) })
	errCh := make(chan error, 1)
	go func() { errCh <- p.Call(ctx, "session/prompt", map[string]any{}, nil) }()
	<-sent
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("in-flight call returned nil")
		}
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			t.Fatalf("in-flight call returned raw pipe error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight call never returned")
	}
	<-p.Done()
	for name, ch := range map[string]chan struct{}{
		"readStdout":       p.stdoutDone,
		"readStderr":       p.stderrDone,
		"notificationLoop": p.notifDone,
	} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s goroutine leaked", name)
		}
	}
}
