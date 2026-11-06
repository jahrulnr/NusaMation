package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/store"
)

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "agents", err.Error())
		return
	}
	out := make([]store.Agent, 0, len(list))
	for _, a := range list {
		out = append(out, a.Public())
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	ag, err := s.store.GetAgent(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Agent not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": ag.Public()})
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var in store.AgentInput
	if err := readJSON(w, r, &in, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid agent payload: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Cwd) == "" {
		in.Cwd = s.cfg.WorkspaceDir()
	}
	ag, err := s.store.CreateAgent(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, "agent_failed", err.Error())
		return
	}
	if s.agents != nil {
		s.agents.WarmAgent(ag.ID)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent": ag.Public()})
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	var in store.AgentInput
	if err := readJSON(w, r, &in, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid agent payload: "+err.Error())
		return
	}
	in.DefaultModelSet = true
	ag, err := s.store.UpdateAgent(r.Context(), r.PathValue("id"), in)
	if err != nil {
		status := http.StatusBadRequest
		if store.IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, "agent_failed", err.Error())
		return
	}
	if s.agents != nil {
		s.agents.InvalidateAgent(ag.ID)
		s.agents.WarmAgent(ag.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": ag.Public()})
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAgent(r.Context(), r.PathValue("id")); err != nil {
		status := http.StatusBadRequest
		if store.IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, "agent_failed", err.Error())
		return
	}
	if s.agents != nil {
		s.agents.InvalidateAgent(r.PathValue("id"))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleProbeAgent(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := s.agents.Probe(ctx, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": err.Error(), "code": "probe_failed", "result": res,
		})
		return
	}
	ag, _ := s.store.GetAgent(r.Context(), r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "agent": ag.Public()})
}

func (s *Server) handleAuthenticateAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MethodID string `json:"methodId"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Choose an authentication method.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := s.agents.Authenticate(ctx, r.PathValue("id"), in.MethodID); err != nil {
		writeError(w, http.StatusBadRequest, "auth_failed", err.Error())
		return
	}
	if s.agents != nil {
		s.agents.WarmAgent(r.PathValue("id"))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogoutAgent(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.agents.Logout(ctx, r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, "logout_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ag, err := s.store.GetAgent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Agent not found.")
		return
	}
	status, pid, uptime, logs := s.agents.AgentRuntimeInfo(id)
	writeJSON(w, http.StatusOK, map[string]any{
		"agent": ag.Public(), "runtime": map[string]any{
			"status": status, "pid": pid, "uptimeSeconds": int64(uptime.Seconds()), "logs": logs,
		},
	})
}

func (s *Server) handleListMCP(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListMCP(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mcp", err.Error())
		return
	}
	servers := make([]store.MCPServer, 0, len(list))
	for _, m := range list {
		servers = append(servers, m.Public())
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": servers})
}

func (s *Server) handleCreateMCP(w http.ResponseWriter, r *http.Request) {
	var in store.MCPServer
	if err := readJSON(w, r, &in, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid MCP payload: "+err.Error())
		return
	}
	m, err := s.store.CreateMCP(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, "mcp_failed", err.Error())
		return
	}
	s.agents.InvalidateAllAgents()
	s.agents.WarmAutoStart()
	writeJSON(w, http.StatusCreated, map[string]any{"server": m.Public()})
}

func (s *Server) handleUpdateMCP(w http.ResponseWriter, r *http.Request) {
	var in store.MCPServer
	if err := readJSON(w, r, &in, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid MCP payload: "+err.Error())
		return
	}
	in.EnabledSet = true
	cur, err := s.store.GetMCP(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "MCP server not found.")
		return
	}
	m, err := s.store.UpdateMCP(r.Context(), cur.ID, in)
	if err != nil {
		writeError(w, http.StatusBadRequest, "mcp_failed", err.Error())
		return
	}
	s.agents.InvalidateAllAgents()
	s.agents.WarmAutoStart()
	writeJSON(w, http.StatusOK, map[string]any{"server": m.Public()})
}

func (s *Server) handleDeleteMCP(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteMCP(r.Context(), r.PathValue("id")); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, "mcp_failed", err.Error())
		return
	}
	s.agents.InvalidateAllAgents()
	s.agents.WarmAutoStart()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
