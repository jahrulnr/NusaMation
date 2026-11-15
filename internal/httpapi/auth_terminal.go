package httpapi

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
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
	s.agents.InvalidateAgent(r.PathValue("id"))
	s.agents.WarmAgent(r.PathValue("id"))
	termSendStatus(conn, true, "Authentication finished. Reconnecting the agent…")
}
