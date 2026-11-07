package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// termConn abstracts the child's terminal: a pty on Unix, plain pipes on
// Windows where creack/pty is not implemented.
type termConn struct {
	io.ReadWriteCloser
	resize func(rows, cols uint16)
}

// handleAuthTerminal hosts only the terminal login command advertised by the agent.
func (s *Server) handleAuthTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad_origin", "Request origin is not allowed.")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd, err := s.agents.TerminalAuthCommand(ctx, r.PathValue("id"), r.URL.Query().Get("methodId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "auth_failed", err.Error())
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // sameOrigin was checked above
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "authentication ended")
	sendStatus := func(ok bool, message string) {
		b, _ := json.Marshal(map[string]any{"type": "exit", "ok": ok, "message": message})
		_ = conn.Write(context.Background(), websocket.MessageText, b)
	}

	// coder/websocket allows one concurrent writer; serialize pipe copies.
	var outMu sync.Mutex
	writeOutput := func(b []byte) error {
		outMu.Lock()
		err := conn.Write(context.Background(), websocket.MessageBinary, b)
		outMu.Unlock()
		return err
	}

	var terminal *termConn
	if t, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80}); err == nil {
		terminal = &termConn{ReadWriteCloser: t, resize: func(rows, cols uint16) {
			_ = pty.Setsize(t, &pty.Winsize{Rows: rows, Cols: cols})
		}}
	} else {
		s.log.Warn("pty unavailable, falling back to plain pipes", "error", err)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			sendStatus(false, err.Error())
			return
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			sendStatus(false, err.Error())
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			sendStatus(false, err.Error())
			return
		}
		if err := cmd.Start(); err != nil {
			sendStatus(false, err.Error())
			return
		}
		pr, pw := io.Pipe()
		copy := func(src io.Reader) {
			go func() {
				buf := make([]byte, 32*1024)
				for {
					n, err := src.Read(buf)
					if n > 0 {
						_, _ = pw.Write(buf[:n])
					}
					if err != nil {
						_ = pw.CloseWithError(err)
						return
					}
				}
			}()
		}
		copy(stdout)
		copy(stderr)
		terminal = &termConn{
			ReadWriteCloser: &pipeTerm{in: stdin, out: pr},
			resize:          func(uint16, uint16) {},
		}
	}
	defer terminal.Close()

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
	err = cmd.Wait()
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		sendStatus(false, err.Error())
		return
	}
	s.agents.InvalidateAgent(r.PathValue("id"))
	s.agents.WarmAgent(r.PathValue("id"))
	sendStatus(true, "Authentication finished. Reconnecting the agent…")
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
