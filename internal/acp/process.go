package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jahrulnr/nusamation/internal/procsys"
)

var (
	ErrProcessExited = errors.New("ACP process exited")
	ErrClosed        = errors.New("ACP connection is closed")
	maxMessageSize   = 64 * 1024 * 1024
)

type Handler interface {
	OnNotification(method string, sessionID string, update json.RawMessage)
	OnRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError)
	OnStderr(line string)
	OnExit(err error)
}

type ProcessConfig struct {
	Command string
	Args    []string
	Env     map[string]string
	Dir     string
	Log     *slog.Logger
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type callResult struct {
	result json.RawMessage
	err    *RPCError
}

type pendingCall struct {
	method string
	ch     chan callResult
	ctx    context.Context
}

type requestOwnerKey struct{}

// WithRequestOwner associates agent-initiated requests with the conversation
// that initiated an ACP call, including requests made before session/new returns.
func WithRequestOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, requestOwnerKey{}, owner)
}

func RequestOwner(ctx context.Context) string {
	owner, _ := ctx.Value(requestOwnerKey{}).(string)
	return owner
}

type requestSentKey struct{}

// WithRequestSent registers fn to run synchronously right after a request made
// with ctx has been written to the agent. fn must be cheap and non-blocking.
func WithRequestSent(ctx context.Context, fn func()) context.Context {
	return context.WithValue(ctx, requestSentKey{}, fn)
}

func requestSent(ctx context.Context) func() {
	fn, _ := ctx.Value(requestSentKey{}).(func())
	return fn
}

type Process struct {
	cfg     ProcessConfig
	handler Handler
	log     *slog.Logger

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[string]*pendingCall
	closed  bool
	exitErr error

	done       chan struct{}
	stdoutDone chan struct{}
	stderrDone chan struct{}
	exitOnce   sync.Once

	notifCh   chan notifMsg
	notifDone chan struct{}
	ring      *Ring
	pid       int
	startedAt time.Time
}

type notifMsg struct {
	method    string
	sessionID string
	update    json.RawMessage
	barrier   chan struct{}
}

func NewProcess(cfg ProcessConfig, handler Handler) *Process {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Process{
		cfg:        cfg,
		handler:    handler,
		log:        cfg.Log.With("acp", "proc"),
		pending:    make(map[string]*pendingCall),
		done:       make(chan struct{}),
		notifCh:    make(chan notifMsg, 2048),
		notifDone:  make(chan struct{}),
		stdoutDone: make(chan struct{}),
		stderrDone: make(chan struct{}),
		ring:       NewRing(400),
	}
}

func (p *Process) Start(ctx context.Context) error {
	if p.cmd != nil {
		return errors.New("process already started")
	}
	cmd := exec.Command(p.cfg.Command, p.cfg.Args...)
	cmd.Dir = p.cfg.Dir
	env := os.Environ()
	for k, v := range p.cfg.Env {
		env = append(env, k+"="+v)
	}
	if !hasEnv(env, "TERM") {
		env = append(env, "TERM=dumb")
	}
	if !hasEnv(env, "NO_COLOR") {
		env = append(env, "NO_COLOR=1")
	}
	cmd.Env = env
	// On Unix the child gets its own process group so shutdown can signal the
	// whole tree; on Windows this is a no-op.
	procsys.StartInOwnGroup(cmd)
	// Create the pipes by hand instead of using the StdinPipe/StdoutPipe/
	// StderrPipe helpers: cmd.Wait() closes the helper pipes' parent ends as
	// soon as the child exits, while the writer and the scanner goroutines
	// can still be using them. With plain os.Pipe files the parent keeps
	// ownership and closes its ends only after the readers drained whatever
	// the child wrote right up to the exit.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return err
	}
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		return fmt.Errorf("start ACP agent: %w", err)
	}
	// The child inherited its side of each pipe; closing the parent's copies
	// is what lets the readers see EOF once the process exits.
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()
	p.cmd = cmd
	p.stdin = stdinW
	p.stdout = stdoutR
	p.stderr = stderrR
	p.pid = cmd.Process.Pid
	p.startedAt = time.Now()
	p.log.Info("agent process started", "pid", p.pid, "command", p.cfg.Command, "dir", p.cfg.Dir)

	go p.readStdout()
	go p.readStderr()
	go p.notificationLoop()
	go p.wait()
	return nil
}

func hasEnv(env []string, key string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			return true
		}
	}
	return false
}

func (p *Process) readStdout() {
	defer close(p.stdoutDone)
	scanner := bufio.NewScanner(p.stdout)
	scanner.Buffer(make([]byte, 256*1024), maxMessageSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			p.log.Warn("invalid JSON from agent stdout", "error", err, "line", truncate(string(line), 500))
			continue
		}
		p.handleMessage(msg)
	}
	if err := scanner.Err(); err != nil && !p.isClosed() {
		p.log.Warn("agent stdout closed", "error", err)
	}
}

func (p *Process) handleMessage(msg rpcMessage) {
	switch {
	case msg.Method != "" && len(msg.ID) > 0:
		go p.handleRequest(msg)
	case msg.Method != "":
		var notif struct {
			SessionID string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		}
		if err := json.Unmarshal(msg.Params, &notif); err != nil || len(notif.Update) == 0 {
			// Non-session notifications (e.g. _cognition.ai/output) are passed through too.
			select {
			case p.notifCh <- notifMsg{method: msg.Method, update: msg.Params}:
			case <-p.done:
			}
			return
		}
		select {
		case p.notifCh <- notifMsg{method: msg.Method, sessionID: notif.SessionID, update: notif.Update}:
		case <-p.done:
		}
	case len(msg.ID) > 0:
		// A prompt response must not overtake earlier session updates. Keep
		// reading stdout while the notification handler drains, since it may
		// need to service an agent request in the meantime.
		p.mu.Lock()
		call := p.pending[idKey(msg.ID)]
		p.mu.Unlock()
		if call != nil && call.method == "session/prompt" {
			barrier := make(chan struct{})
			select {
			case p.notifCh <- notifMsg{barrier: barrier}:
				go func() {
					select {
					case <-barrier:
						p.handleResponse(msg)
					case <-p.done:
					}
				}()
			case <-p.done:
			}
		} else {
			p.handleResponse(msg)
		}
	}
}

func (p *Process) handleResponse(msg rpcMessage) {
	key := idKey(msg.ID)
	p.mu.Lock()
	call, ok := p.pending[key]
	if ok {
		delete(p.pending, key)
	}
	p.mu.Unlock()
	if !ok {
		return
	}
	call.ch <- callResult{result: msg.Result, err: msg.Error}
}

func (p *Process) handleRequest(msg rpcMessage) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("panic handling agent request", "method", msg.Method, "panic", r)
			p.respond(msg.ID, nil, &RPCError{Code: CodeInternalError, Message: "internal client error"})
		}
	}()
	parent := context.Background()
	var ref struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(msg.Params, &ref) == nil && len(ref.RequestID) > 0 {
		p.mu.Lock()
		if call := p.pending[idKey(ref.RequestID)]; call != nil && call.ctx != nil {
			parent = call.ctx
		}
		p.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Minute)
	defer cancel()
	go func() {
		select {
		case <-p.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	result, rpcErr := p.handler.OnRequest(ctx, msg.Method, msg.Params)
	p.respond(msg.ID, result, rpcErr)
}

func (p *Process) respond(id json.RawMessage, result any, rpcErr *RPCError) {
	msg := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id)}
	if rpcErr != nil {
		msg["error"] = rpcErr
	} else {
		if result == nil {
			result = map[string]any{}
		}
		msg["result"] = result
	}
	if err := p.writeJSON(msg); err != nil {
		p.log.Warn("failed to respond to agent", "error", err)
	}
}

func (p *Process) notificationLoop() {
	defer close(p.notifDone)
	for {
		select {
		case n := <-p.notifCh:
			if n.barrier != nil {
				close(n.barrier)
				continue
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						p.log.Error("panic in notification handler", "panic", r)
					}
				}()
				p.handler.OnNotification(n.method, n.sessionID, n.update)
			}()
		case <-p.done:
			// Drain anything already queued before exiting.
			for {
				select {
				case n := <-p.notifCh:
					if n.barrier != nil {
						close(n.barrier)
						continue
					}
					func() {
						defer func() { _ = recover() }()
						p.handler.OnNotification(n.method, n.sessionID, n.update)
					}()
				default:
					return
				}
			}
		}
	}
}

func (p *Process) readStderr() {
	defer close(p.stderrDone)
	scanner := bufio.NewScanner(p.stderr)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		p.ring.Add(line)
		p.handler.OnStderr(line)
		if p.log.Enabled(context.Background(), slog.LevelDebug) {
			p.log.Debug("agent stderr", "line", truncate(line, 1000))
		}
	}
}

func (p *Process) wait() {
	err := p.cmd.Wait()
	p.exitOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.exitErr = err
		var pending []*pendingCall
		for k, c := range p.pending {
			pending = append(pending, c)
			delete(p.pending, k)
		}
		p.mu.Unlock()
		if err == nil {
			err = ErrProcessExited
		}
		// The parent owns the pipe ends now, so nothing closes them under the
		// scanners: they keep draining whatever the process wrote right up to
		// the exit. That trailing stderr is usually the only explanation for
		// an agent that dies before initialize, and callers read StderrLog
		// once their Call fails, so the drain finishes before pending calls
		// and done are released.
		deadline := time.After(2 * time.Second)
		select {
		case <-p.stderrDone:
		case <-deadline:
		}
		select {
		case <-p.stdoutDone:
		case <-deadline:
		}
		p.writeMu.Lock()
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
		p.writeMu.Unlock()
		if p.stdout != nil {
			_ = p.stdout.Close()
		}
		if p.stderr != nil {
			_ = p.stderr.Close()
		}
		for _, c := range pending {
			select {
			case c.ch <- callResult{err: &RPCError{Code: CodeInternalError, Message: "agent process exited: " + err.Error()}}:
			default:
			}
		}
		close(p.done)
		// Give the notification loop a moment to drain.
		select {
		case <-p.notifDone:
		case <-time.After(2 * time.Second):
		}
		p.handler.OnExit(err)
		p.log.Info("agent process exited", "pid", p.pid, "error", err)
	})
}

func (p *Process) Call(ctx context.Context, method string, params any, out any) error {
	if p.isClosed() {
		return ErrProcessExited
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	key := fmt.Sprintf("%d", id)
	call := &pendingCall{method: method, ch: make(chan callResult, 1), ctx: ctx}
	p.pending[key] = call
	p.mu.Unlock()

	rawID, _ := json.Marshal(id)
	payload := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(rawID), "method": method, "params": params}
	if err := p.writeJSON(payload); err != nil {
		p.removePending(key)
		return err
	}
	if sent := requestSent(ctx); sent != nil {
		sent()
	}

	select {
	case res := <-call.ch:
		if res.err != nil {
			return res.err
		}
		if out != nil && len(res.result) > 0 {
			if err := json.Unmarshal(res.result, out); err != nil {
				return fmt.Errorf("decode %s response: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		p.removePending(key)
		if method != "session/prompt" {
			_ = p.Notify("$/cancel_request", map[string]any{"requestId": id})
		}
		return ctx.Err()
	case <-p.done:
		p.removePending(key)
		return ErrProcessExited
	}
}

func (p *Process) removePending(key string) {
	p.mu.Lock()
	delete(p.pending, key)
	p.mu.Unlock()
}

func (p *Process) Notify(method string, params any) error {
	return p.writeJSON(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (p *Process) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdin == nil {
		return ErrClosed
	}
	if p.isClosed() {
		return ErrProcessExited
	}
	if _, err := p.stdin.Write(b); err != nil {
		// A write to the agent's stdin only fails once the far end is gone:
		// either the process died (EPIPE) or shutdown closed the pipe under
		// the writer. Surface the stable exit error, not the raw pipe error.
		if p.isClosed() || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EPIPE) {
			return ErrProcessExited
		}
		return err
	}
	return nil
}

func (p *Process) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *Process) Done() <-chan struct{} { return p.done }
func (p *Process) PID() int              { return p.pid }
func (p *Process) StartedAt() time.Time  { return p.startedAt }
func (p *Process) StderrLog() []string {
	// When the process is gone the exit path may still be draining the last
	// lines the agent wrote; wait for it so callers diagnosing a dead agent
	// never see a truncated log. done is the bounded wait: it closes after
	// the drain or its timeout, even if an OnStderr handler is wedged.
	if p.isClosed() {
		<-p.done
	}
	return p.ring.Lines()
}

func (p *Process) ExitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitErr
}

// Close gracefully stops the agent, escalating to SIGKILL after a timeout.
func (p *Process) Close() error {
	p.mu.Lock()
	already := p.closed
	p.mu.Unlock()
	if already {
		return nil
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		p.signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			p.signal(syscall.SIGKILL)
			select {
			case <-p.done:
			case <-time.After(2 * time.Second):
			}
		}
	}
	return nil
}

func (p *Process) signal(sig syscall.Signal) {
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	procsys.Signal(p.cmd, sig)
}

func idKey(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var out string
		if json.Unmarshal(raw, &out) == nil {
			return out
		}
	}
	return s
}

// Ring is a small, concurrency-safe ring buffer for agent log lines.
type Ring struct {
	mu    sync.Mutex
	max   int
	lines []string
	start int
	full  bool
}

func NewRing(max int) *Ring { return &Ring{max: max} }

func (r *Ring) Add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) < r.max {
		r.lines = append(r.lines, line)
		return
	}
	r.lines[r.start] = line
	r.start = (r.start + 1) % r.max
	r.full = true
}

func (r *Ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]string(nil), r.lines...)
	}
	out := make([]string, 0, len(r.lines))
	out = append(out, r.lines[r.start:]...)
	out = append(out, r.lines[:r.start]...)
	return out
}
