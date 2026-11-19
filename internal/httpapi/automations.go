package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/textfmt"
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

// handleAutomationRun starts one immediate run of a job. The schedule itself
// is untouched — the next cron occurrence still fires when it should.
func (s *Server) handleAutomationRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetAutomation(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Automation not found.")
		return
	}
	if s.automations == nil {
		writeError(w, http.StatusServiceUnavailable, "automation", "The scheduler is not available.")
		return
	}
	if err := s.automations.RunNow(id); err != nil {
		writeError(w, http.StatusInternalServerError, "automation", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAutomationTestNotify sends a short message to the job's result chat
// so a misconfigured channel or target is discovered now, not at 09:00.
func (s *Server) handleAutomationTestNotify(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.GetAutomation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Automation not found.")
		return
	}
	if job.NotifyChannel == "" || job.NotifyTarget == "" {
		writeError(w, http.StatusBadRequest, "not_configured", "This automation has no result chat configured.")
		return
	}
	ch, ok := s.channelByName(job.NotifyChannel)
	if !ok {
		writeError(w, http.StatusBadRequest, "not_supported", "Channel "+job.NotifyChannel+" is not registered.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	text := fmt.Sprintf("NusaMation test for automation %q — if this arrives, the result chat works.", textfmt.OneLine(job.Name, 80))
	target := channels.Target{Channel: job.NotifyChannel, ExternalID: job.NotifyTarget, IsGroup: strings.Contains(job.NotifyTarget, "@g.us")}
	if err := ch.Notify(ctx, target, text); err != nil {
		writeError(w, http.StatusBadGateway, "test_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
