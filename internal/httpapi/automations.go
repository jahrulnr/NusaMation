package httpapi

import (
	"net/http"
	"time"

	"github.com/jahrulnr/nusamation/internal/store"
)

func (s *Server) handleAutomations(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		jobs, err := s.store.ListAutomations(r.Context())
		if err != nil {
			writeError(w, 500, "automations", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"automations": jobs, "timezone": store.LocalTimezone()})
		return
	}
	var job store.Automation
	if err := readJSON(w, r, &job, 1<<20); err != nil {
		writeError(w, 400, "bad_request", err.Error())
		return
	}
	job.ID = ""
	job.Enabled = true
	saved, err := s.store.SaveAutomation(r.Context(), job, time.Now())
	if err != nil {
		writeError(w, 400, "automation", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"automation": saved})
}
func (s *Server) handleAutomation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		job, err := s.store.GetAutomation(r.Context(), id)
		if err != nil {
			writeError(w, 404, "not_found", "Automation not found.")
			return
		}
		runs, err := s.store.ListAutomationRuns(r.Context(), id)
		if err != nil {
			writeError(w, 500, "history", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"automation": job, "runs": runs})
	case http.MethodPut:
		var job store.Automation
		if err := readJSON(w, r, &job, 1<<20); err != nil {
			writeError(w, 400, "bad_request", err.Error())
			return
		}
		job.ID = id
		saved, err := s.store.SaveAutomation(r.Context(), job, time.Now())
		if err != nil {
			writeError(w, 400, "automation", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"automation": saved})
	case http.MethodDelete:
		if err := s.store.DeleteAutomation(r.Context(), id); err != nil {
			writeError(w, 404, "not_found", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}
func (s *Server) handleAutomationEnabled(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(w, r, &in, 1024); err != nil || in.Enabled == nil {
		writeError(w, 400, "bad_request", "enabled is required")
		return
	}
	if err := s.store.SetAutomationEnabled(r.Context(), r.PathValue("id"), *in.Enabled); err != nil {
		writeError(w, 404, "not_found", err.Error())
		return
	}
	job, err := s.store.GetAutomation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, "automation", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"automation": job})
}
