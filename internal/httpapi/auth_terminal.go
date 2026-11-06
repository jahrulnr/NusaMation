package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

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
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		sendStatus(false, err.Error())
		return
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
					_ = pty.Setsize(terminal, &pty.Winsize{Rows: msg.Rows, Cols: msg.Cols})
				}
			}
		}
	}()
	buf := make([]byte, 32*1024)
	for {
		n, readErr := terminal.Read(buf)
		if n > 0 {
			if err := conn.Write(ctx, websocket.MessageBinary, buf[:n]); err != nil {
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
