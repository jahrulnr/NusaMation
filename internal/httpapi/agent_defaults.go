package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/jahrulnr/nusamation/internal/store"
)

func (s *Server) agentDefaultsRequest(w http.ResponseWriter, r *http.Request, save bool) {
	var defaults store.AgentDefaults
	if err := readJSON(w, r, &defaults, 64<<10); err != nil || defaults.AgentID == "" || len(defaults.Selections) > 64 {
		writeError(w, http.StatusBadRequest, "bad_request", "Choose an agent and valid configuration options.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	preview := s.agents.PreviewDefaults
	if save {
		// Persisting defaults must prove the sequence on a live session; the
		// preview endpoint may answer from the approximate cached catalog.
		preview = s.agents.ValidateDefaults
	}
	configs, err := preview(ctx, defaults.AgentID, defaults.Selections)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent_defaults", err.Error())
		return
	}
	if save {
		if err := s.store.SetJSONSetting(ctx, "agent.defaults", defaults); err != nil {
			writeError(w, http.StatusInternalServerError, "settings", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"defaults": defaults, "configs": configs})
}

func (s *Server) handlePreviewAgentDefaults(w http.ResponseWriter, r *http.Request) {
	s.agentDefaultsRequest(w, r, false)
}
func (s *Server) handleSaveAgentDefaults(w http.ResponseWriter, r *http.Request) {
	s.agentDefaultsRequest(w, r, true)
}
