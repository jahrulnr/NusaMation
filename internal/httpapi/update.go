package httpapi

import (
	"context"
	"net/http"
	"time"
)

// ---------- software update ----------

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Update checks are not available.")
		return
	}
	writeJSON(w, http.StatusOK, s.updater.Status())
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Update checks are not available.")
		return
	}
	// A background context: a client disconnect should not abort the check
	// halfway and poison the shared status with a spurious error.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.updater.Check(ctx))
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Self-update is not available.")
		return
	}
	if err := s.updater.Apply(); err != nil {
		writeError(w, http.StatusConflict, "update", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, s.updater.Status())
}

// ---------- desktop notifications ----------

func (s *Server) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	if s.notifier == nil || !s.notifier.Available() {
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"No desktop notification backend on this host (notify-send on Linux, PowerShell on Windows).")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.notifier.Test(ctx); err != nil {
		writeError(w, http.StatusBadGateway, "notify", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "backend": s.notifier.Backend()})
}

// notificationSettings is the "notifications" object in GET /api/settings.
func (s *Server) notificationSettings(ctx context.Context) map[string]any {
	enabled := true
	if v, err := s.store.GetSetting(ctx, "notify.enabled"); err == nil && v == "false" {
		enabled = false
	}
	backend, available := "", false
	if s.notifier != nil {
		backend, available = s.notifier.Backend(), s.notifier.ProbeAvailable()
	}
	return map[string]any{"enabled": enabled, "backend": backend, "available": available}
}
