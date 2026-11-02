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
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
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

	done     chan struct{}
	exitOnce sync.Once

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
}

func NewProcess(cfg ProcessConfig, handler Handler) *Process {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Process{
		cfg:       cfg,
		handler:   handler,
		log:       cfg.Log.With("acp", "proc"),
		pending:   make(map[string]*pendingCall),
		done:      make(chan struct{}),
		notifCh:   make(chan notifMsg, 2048),
		notifDone: make(chan struct{}),
		ring:      NewRing(400),
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
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ACP agent: %w", err)
	}
	p.cmd = cmd
	p.stdin = stdin
	p.stdout = stdout
	p.stderr = stderr
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
		p.handleResponse(msg)
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
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
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	key := fmt.Sprintf("%d", id)
	call := &pendingCall{method: method, ch: make(chan callResult, 1)}
	p.pending[key] = call
	p.mu.Unlock()

	rawID, _ := json.Marshal(id)
	payload := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(rawID), "method": method, "params": params}
	if err := p.writeJSON(payload); err != nil {
		p.removePending(key)
		return err
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
	_, err = p.stdin.Write(b)
	return err
}

func (p *Process) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *Process) Done() <-chan struct{} { return p.done }
func (p *Process) PID() int              { return p.pid }
func (p *Process) StartedAt() time.Time  { return p.startedAt }
func (p *Process) StderrLog() []string   { return p.ring.Lines() }

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
	if runtime.GOOS == "windows" {
		_ = p.cmd.Process.Kill()
		return
	}
	if p.cmd.SysProcAttr != nil && p.cmd.SysProcAttr.Setpgid {
		_ = syscall.Kill(-p.cmd.Process.Pid, sig)
	} else {
		_ = p.cmd.Process.Signal(sig)
	}
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
