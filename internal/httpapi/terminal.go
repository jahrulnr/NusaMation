package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

// termConn abstracts the child's terminal: a pty where the platform provides
// one, else plain pipes when pty setup is unavailable or fails.
type termConn struct {
	io.ReadWriteCloser
	resize func(rows, cols uint16)
}

// startTermConn launches cmd on a pty when the platform supports it, else
// merges stdout/stderr into a pipe-backed terminal.
func (s *Server) startTermConn(cmd *exec.Cmd) (*termConn, error) {
	origSys := cmd.SysProcAttr
	if t, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80}); err == nil {
		return &termConn{ReadWriteCloser: t, resize: func(rows, cols uint16) {
			_ = pty.Setsize(t, &pty.Winsize{Rows: rows, Cols: cols})
		}}, nil
	} else {
		s.log.Warn("pty unavailable, falling back to plain pipes", "error", err)
		// StartWithSize wires cmd's std streams and SysProcAttr to the tty
		// before c.Start — reset them or StdinPipe reports "already set" and
		// the real cause is lost.
		cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.SysProcAttr = nil, nil, nil, origSys
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	var wg sync.WaitGroup
	copy := func(src io.Reader) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 32*1024)
			for {
				n, err := src.Read(buf)
				if n > 0 {
					if _, werr := pw.Write(buf[:n]); werr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
	}
	copy(stdout)
	copy(stderr)
	// The pipe closes only after BOTH streams end — a first finisher must not
	// drop the other's trailing output.
	go func() { wg.Wait(); _ = pw.Close() }()
	return &termConn{
		ReadWriteCloser: &pipeTerm{in: stdin, out: pr},
		resize:          func(uint16, uint16) {},
	}, nil
}

// runTerminal pumps terminal output to conn and conn input to the terminal
// until the process exits or the socket/context ends. Returns cmd.Wait().
func runTerminal(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, terminal *termConn, cmd *exec.Cmd) error {
	defer terminal.Close()

	// coder/websocket allows one concurrent writer; serialize pipe copies.
	// Writes use ctx: a dead peer is reaped by the ping ticker below, and a
	// read error's cancel() also aborts a blocked write.
	var outMu sync.Mutex
	writeOutput := func(b []byte) error {
		outMu.Lock()
		err := conn.Write(ctx, websocket.MessageBinary, b)
		outMu.Unlock()
		return err
	}

	// Liveness: a peer that dies without FIN/RST would otherwise leave
	// conn.Read blocked and wedge the pump on write backpressure forever —
	// a periodic Ping detects it and tears the session down.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if conn.Ping(ctx) != nil {
					cancel()
					return
				}
			}
		}
	}()

	go func() {
		defer cancel()
		defer terminal.Close()
		for {
			_, payload, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
				Data string `json:"data"`
				Rows uint16 `json:"rows"`
				Cols uint16 `json:"cols"`
			}
			if json.Unmarshal(payload, &msg) != nil {
				continue
			}
			switch msg.Type {
			case "input":
				_, _ = terminal.Write([]byte(msg.Data))
			case "resize":
				if msg.Rows > 0 && msg.Cols > 0 {
					terminal.resize(msg.Rows, msg.Cols)
				}
			}
		}
	}()
	buf := make([]byte, 32*1024)
	for {
		n, readErr := terminal.Read(buf)
		if n > 0 {
			if err := writeOutput(buf[:n]); err != nil {
				cancel()
				break
			}
		}
		if readErr != nil {
			break
		}
	}
	return cmd.Wait()
}

func termSendStatus(conn *websocket.Conn, ok bool, message string) {
	b, _ := json.Marshal(map[string]any{"type": "exit", "ok": ok, "message": message})
	// The pump ctx may already be cancelled here; bound the write so a
	// read-stalled client cannot hold the handler goroutine.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, b)
}

// handleConvTerminal upgrades to a WebSocket and runs an interactive shell.
// ?cwd= picks any existing directory (the pane offers the conversation's
// roots; a shell can cd anywhere anyway). Empty falls back to the first
// existing conversation root.
func (s *Server) handleConvTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad_origin", "Request origin is not allowed.")
		return
	}
	conv, err := s.store.GetConversation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	var cwd string
	if cwdParam := strings.TrimSpace(r.URL.Query().Get("cwd")); cwdParam == "" {
		cwd, err = agent.TerminalCwd(s.convTerminalRoots(r.Context(), conv), "")
	} else {
		cwd, err = existingDir(cwdParam)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cwd", err.Error())
		return
	}
	s.runTerminalWS(w, r, cwd)
}

// handleTerminal is the conversation-less twin of handleConvTerminal for draft
// rooms: ?cwd= may be any existing directory; empty falls back to the home
// directory since a draft has no conversation roots yet.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad_origin", "Request origin is not allowed.")
		return
	}
	var cwd string
	var err error
	if param := strings.TrimSpace(r.URL.Query().Get("cwd")); param == "" {
		var home string
		if home, err = os.UserHomeDir(); err == nil {
			cwd, err = existingDir(home)
		}
	} else {
		cwd, err = existingDir(param)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cwd", err.Error())
		return
	}
	s.runTerminalWS(w, r, cwd)
}

// runTerminalWS upgrades the request and runs a shell in cwd — the tail shared
// by conversation and draft terminals.
func (s *Server) runTerminalWS(w http.ResponseWriter, r *http.Request, cwd string) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := shellCommand(ctx, cwd)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // sameOrigin was checked above
	if err != nil {
		return
	}
	conn.SetReadLimit(1 << 20) // a paste must not kill the shell (default is 32KB)
	defer conn.Close(websocket.StatusNormalClosure, "terminal ended")
	terminal, err := s.startTermConn(cmd)
	if err != nil {
		termSendStatus(conn, false, err.Error())
		return
	}
	waitErr := runTerminal(ctx, cancel, conn, terminal, cmd)
	if ctx.Err() != nil {
		return
	}
	if waitErr != nil {
		termSendStatus(conn, false, waitErr.Error())
		return
	}
	termSendStatus(conn, true, "Terminal session ended.")
}

// handleConvRoots lists the existing directories the pane tools may open:
// the conversation roots that currently exist, in preference order.
func (s *Server) handleConvRoots(w http.ResponseWriter, r *http.Request) {
	conv, err := s.store.GetConversation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roots": existingDirs(s.convTerminalRoots(r.Context(), conv))})
}

// handleProjectRoots lists a project's working dirs that currently exist —
// the draft room's pick list (no conversation owns the roots yet).
func (s *Server) handleProjectRoots(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Project not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roots": existingDirs(p.RootPaths)})
}

// existingDir resolves a terminal cwd request to an existing directory.
func existingDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	abs, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", errors.New("folder does not exist or is not a directory")
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", errors.New("folder does not exist or is not a directory")
	}
	return abs, nil
}

// existingDirs filters paths to existing absolute directories, deduped and in
// order — a dead path drops out instead of poisoning a picker.
func existingDirs(paths []string) []string {
	roots := []string{}
	seen := map[string]bool{}
	for _, root := range paths {
		root = strings.TrimSpace(root)
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		abs, err := security.SafePath(root, root, true)
		if err != nil {
			continue
		}
		if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
			abs = resolved // dedupe aliases; the picker shows where the shell lands
		}
		if seen[abs] {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			continue
		}
		seen[abs] = true
		roots = append(roots, abs)
	}
	return roots
}

// convTerminalRoots lists the directories a terminal may start in. A project
// member gets its own project's working dirs — not every registered
// project's, and not the shared workspace. Standalone conversations fall back
// to the workspace plus any additional roots.
func (s *Server) convTerminalRoots(ctx context.Context, conv store.Conversation) []string {
	roots := []string{}
	if conv.Cwd != "" {
		roots = append(roots, conv.Cwd)
	}
	if conv.ProjectID != "" {
		if p, err := s.store.GetProject(ctx, conv.ProjectID); err == nil {
			roots = append(roots, p.RootPaths...)
		} else {
			s.log.Debug("project roots lookup failed", "project", conv.ProjectID, "error", err)
		}
	} else {
		roots = append(roots, s.cfg.WorkspaceDir())
	}
	var meta map[string]any
	if json.Unmarshal(conv.Metadata, &meta) == nil {
		if dirs, ok := meta["additionalDirectories"].([]any); ok {
			for _, d := range dirs {
				if str, ok := d.(string); ok {
					roots = append(roots, str)
				}
			}
		}
	}
	return roots
}

func shellCommand(ctx context.Context, cwd string) *exec.Cmd {
	shell := os.Getenv("SHELL")
	if runtime.GOOS == "windows" {
		shell = os.Getenv("COMSPEC")
		if shell == "" {
			shell = "cmd.exe"
		}
	}
	if shell == "" {
		shell = "sh"
	}
	cmd := exec.CommandContext(ctx, shell)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	return cmd
}

// pipeTerm merges a command's stdout/stderr pipes into one reader and its
// stdin pipe as the writer.
type pipeTerm struct {
	in  io.WriteCloser
	out io.ReadCloser
}

func (t *pipeTerm) Read(p []byte) (int, error)  { return t.out.Read(p) }
func (t *pipeTerm) Write(p []byte) (int, error) { return t.in.Write(p) }
func (t *pipeTerm) Close() error {
	_ = t.in.Close()
	return t.out.Close()
}
