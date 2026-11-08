package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/ids"
	"github.com/jahrulnr/nusamation/internal/procsys"
	"github.com/jahrulnr/nusamation/internal/security"
)

type terminal struct {
	id        string
	sessionID string
	command   string
	cwd       string

	mu        sync.Mutex
	buf       []byte
	full      []byte
	version   uint64
	notify    func(TerminalUpdate)
	limit     int
	truncated bool
	running   bool
	exit      *acp.TerminalExitStatus
	done      chan struct{}
	cmd       *exec.Cmd
	released  bool
	lastUsed  time.Time
}

type TerminalUpdate struct {
	ID         string                  `json:"id"`
	Output     string                  `json:"output"`
	ExitStatus *acp.TerminalExitStatus `json:"exitStatus,omitempty"`
	Version    uint64                  `json:"-"`
}

// maxTerminalFull caps the cumulative stream sent with every update — noisy
// commands can otherwise grow memory and each DB write without bound.
const maxTerminalFull = 256 << 10

func (t *terminal) append(p []byte) {
	t.mu.Lock()
	t.full = append(t.full, p...)
	if len(t.full) > maxTerminalFull {
		cut := len(t.full) - maxTerminalFull
		for cut < len(t.full) && !utf8.RuneStart(t.full[cut]) {
			cut++
		}
		t.full = append([]byte("[earlier output truncated]\n"), t.full[cut:]...)
	}
	t.buf = append(t.buf, p...)
	if t.limit > 0 && len(t.buf) > t.limit {
		extra := len(t.buf) - t.limit
		// Trim at a UTF-8 boundary.
		for extra < len(t.buf) && !utf8.RuneStart(t.buf[extra]) {
			extra++
		}
		t.buf = append([]byte(nil), t.buf[extra:]...)
		t.truncated = true
	}
	t.version++
	update := TerminalUpdate{ID: t.id, Output: string(t.full), Version: t.version}
	t.mu.Unlock()
	if t.notify != nil {
		t.notify(update)
	}
}

func (t *terminal) output() (string, bool, *acp.TerminalExitStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastUsed = time.Now()
	return string(t.buf), t.truncated, t.exit
}

type TerminalManager struct {
	mu    sync.Mutex
	terms map[string]*terminal
	log   *slog.Logger
}

func NewTerminalManager(log *slog.Logger) *TerminalManager {
	if log == nil {
		log = slog.Default()
	}
	tm := &TerminalManager{terms: map[string]*terminal{}, log: log.With("component", "terminals")}
	go tm.cleanupLoop()
	return tm
}

func (tm *TerminalManager) Handle(ctx context.Context, method string, params json.RawMessage, roots []string, notify func(TerminalUpdate)) (any, *acp.RPCError) {
	switch method {
	case "terminal/create":
		return tm.create(ctx, params, roots, notify)
	case "terminal/output":
		return tm.outputReq(params)
	case "terminal/wait_for_exit":
		return tm.waitReq(ctx, params)
	case "terminal/kill":
		return tm.killReq(params)
	case "terminal/release":
		return tm.releaseReq(params)
	default:
		return nil, &acp.RPCError{Code: acp.CodeMethodNotFound, Message: "unsupported terminal method"}
	}
}

func (tm *TerminalManager) create(ctx context.Context, raw json.RawMessage, roots []string, notify func(TerminalUpdate)) (any, *acp.RPCError) {
	var req acp.TerminalCreateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "invalid terminal/create params"}
	}
	if strings.TrimSpace(req.Command) == "" {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "command is required"}
	}
	cwd, err := terminalCwd(roots, req.Cwd)
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: err.Error()}
	}
	limit := 1 << 20
	if req.OutputByteLimit != nil && *req.OutputByteLimit > 0 {
		limit = min(int(*req.OutputByteLimit), 16<<20)
	}
	cmd := terminalCommand(req.Command, req.Args)
	cmd.Dir = cwd
	cmd.Env = terminalEnv(req.Env)
	// On Unix the child gets its own process group so a kill reaches the
	// whole tree; on Windows this is a no-op.
	procsys.StartInOwnGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	t := &terminal{
		id:        ids.New("term"),
		sessionID: req.SessionID,
		command:   strings.Join(append([]string{req.Command}, req.Args...), " "),
		cwd:       cwd,
		limit:     limit,
		running:   true,
		done:      make(chan struct{}),
		cmd:       cmd,
		lastUsed:  time.Now(),
		notify:    notify,
	}
	if err := cmd.Start(); err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	tm.mu.Lock()
	tm.terms[t.id] = t
	tm.mu.Unlock()
	if notify != nil {
		notify(TerminalUpdate{ID: t.id})
	}
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		read := func(r interface{ Read([]byte) (int, error) }) {
			defer wg.Done()
			buf := make([]byte, 32*1024)
			for {
				n, err := r.Read(buf)
				if n > 0 {
					t.append(buf[:n])
				}
				if err != nil {
					return
				}
			}
		}
		go read(stdout)
		go read(stderr)
		wg.Wait()
		err := cmd.Wait()
		status := exitStatus(cmd, err)
		t.mu.Lock()
		t.running = false
		t.exit = status
		t.version++
		update := TerminalUpdate{ID: t.id, Output: string(t.full), ExitStatus: status, Version: t.version}
		close(t.done)
		t.mu.Unlock()
		if t.notify != nil {
			t.notify(update)
		}
		tm.log.Debug("terminal exited", "id", t.id, "command", t.command, "status", status)
	}()
	return acp.TerminalCreateResponse{TerminalID: t.id}, nil
}

// terminalCwd accepts a working directory inside any session root. An empty
// cwd uses the primary root. Paths outside every root are rejected.
func terminalCwd(roots []string, cwd string) (string, error) {
	if len(roots) == 0 {
		return "", errors.New("session has no workspace root")
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = roots[0]
	}
	if !filepath.IsAbs(cwd) {
		return "", errors.New("path must be absolute")
	}
	var last error
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		abs, err := security.SafePath(root, cwd, true)
		if err == nil {
			return abs, nil
		}
		last = err
	}
	if last != nil {
		return "", last
	}
	return "", errors.New("session has no workspace root")
}

// terminalCommand builds the process for terminal/create. ACP sends a program
// and its args separately, which is what exec.Command expects. Some agents
// (e.g. Devin) send a whole shell line as command with no args, so when no
// args are given and the command does not resolve to a program, run it
// through the platform shell.
func terminalCommand(command string, args []string) *exec.Cmd {
	if len(args) == 0 {
		if _, err := exec.LookPath(command); err != nil {
			if runtime.GOOS == "windows" {
				return exec.Command("cmd.exe", "/c", command)
			}
			return exec.Command("/bin/sh", "-c", command)
		}
	}
	return exec.Command(command, args...)
}

func terminalEnv(extra []acp.EnvVar) []string {
	allow := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
		"LANG": true, "LANGUAGE": true, "TERM": true, "TMPDIR": true, "TZ": true,
	}
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if allow[k] || strings.HasPrefix(k, "LC_") {
			env = append(env, e)
		}
	}
	for _, e := range extra {
		if e.Name == "" {
			continue
		}
		env = append(env, e.Name+"="+e.Value)
	}
	return env
}

func exitStatus(cmd *exec.Cmd, err error) *acp.TerminalExitStatus {
	if cmd.ProcessState == nil {
		return nil
	}
	status := &acp.TerminalExitStatus{}
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		status.ExitCode = &code
	}
	if sig, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && sig.Signaled() {
		s := sig.Signal().String()
		status.Signal = &s
	}
	_ = err
	return status
}

func (tm *TerminalManager) get(raw json.RawMessage) (*terminal, *acp.RPCError) {
	var ref acp.TerminalRef
	if err := json.Unmarshal(raw, &ref); err != nil || ref.TerminalID == "" {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "terminaId is required"}
	}
	tm.mu.Lock()
	t := tm.terms[ref.TerminalID]
	tm.mu.Unlock()
	if t == nil {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "unknown terminal"}
	}
	if ref.SessionID != "" && ref.SessionID != t.sessionID {
		return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: "terminal belongs to another session"}
	}
	return t, nil
}

func (tm *TerminalManager) outputReq(raw json.RawMessage) (any, *acp.RPCError) {
	t, rpcErr := tm.get(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	out, truncated, exit := t.output()
	return acp.TerminalOutputResponse{Output: out, Truncated: truncated, ExitStatus: exit}, nil
}

func (tm *TerminalManager) waitReq(ctx context.Context, raw json.RawMessage) (any, *acp.RPCError) {
	t, rpcErr := tm.get(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	select {
	case <-t.done:
	case <-ctx.Done():
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: "wait cancelled"}
	}
	t.mu.Lock()
	status := t.exit
	t.mu.Unlock()
	if status == nil {
		status = &acp.TerminalExitStatus{}
	}
	return status, nil
}

func (tm *TerminalManager) killReq(raw json.RawMessage) (any, *acp.RPCError) {
	t, rpcErr := tm.get(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	tm.kill(t)
	return map[string]any{}, nil
}

func (tm *TerminalManager) releaseReq(raw json.RawMessage) (any, *acp.RPCError) {
	t, rpcErr := tm.get(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	tm.kill(t)
	t.mu.Lock()
	t.released = true
	t.lastUsed = time.Now()
	t.mu.Unlock()
	return map[string]any{}, nil
}

func (tm *TerminalManager) kill(t *terminal) {
	t.mu.Lock()
	running := t.running
	cmd := t.cmd
	t.mu.Unlock()
	if !running || cmd == nil || cmd.Process == nil {
		return
	}
	procsys.Signal(cmd, syscall.SIGKILL)
}

func (tm *TerminalManager) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		tm.mu.Lock()
		for id, t := range tm.terms {
			t.mu.Lock()
			old := time.Since(t.lastUsed) > 30*time.Minute && !t.running
			t.mu.Unlock()
			if old {
				delete(tm.terms, id)
			}
		}
		tm.mu.Unlock()
	}
}

func (tm *TerminalManager) CloseAll() {
	tm.mu.Lock()
	terms := make([]*terminal, 0, len(tm.terms))
	for _, t := range tm.terms {
		terms = append(terms, t)
	}
	tm.mu.Unlock()
	for _, t := range terms {
		tm.kill(t)
	}
}

var _ = errors.New
var _ = fmt.Sprintf
