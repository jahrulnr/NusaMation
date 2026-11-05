package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/channels/whatsapp"
)

func (s *Server) handleWhatsAppSend(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.channelByName("whatsapp")
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "WhatsApp channel not found.")
		return
	}
	sender, ok := ch.(interface {
		SendStructured(context.Context, whatsapp.OutboundRequest) (string, error)
	})
	if !ok {
		writeError(w, http.StatusBadRequest, "not_supported", "WhatsApp sending is unavailable.")
		return
	}
	var req whatsapp.OutboundRequest
	if err := readJSON(w, r, &req, 36<<20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := sender.SendStructured(r.Context(), req)
	if err != nil {
		if id != "" {
			writeJSON(w, http.StatusBadGateway, map[string]any{"messageId": id, "code": "send_partial", "error": err.Error()})
			return
		}
		writeError(w, http.StatusBadRequest, "send_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messageId": id})
}

func (s *Server) handleWhatsAppUser(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.channelByName("whatsapp")
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "WhatsApp channel not found.")
		return
	}
	lookup, ok := ch.(interface {
		UserInfo(context.Context, string) (map[string]any, error)
	})
	if !ok {
		writeError(w, http.StatusBadRequest, "not_supported", "WhatsApp user lookup is unavailable.")
		return
	}
	result, err := lookup.UserInfo(r.Context(), r.URL.Query().Get("jid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "lookup_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"channels": s.channelStatuses()})
}

func (s *Server) channelController(w http.ResponseWriter, r *http.Request) (channels.Channel, channels.Controller, bool) {
	name := r.PathValue("name")
	ch, ok := s.channelByName(name)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "Channel not found.")
		return nil, nil, false
	}
	ctl, ok := ch.(channels.Controller)
	if !ok {
		writeError(w, http.StatusBadRequest, "not_controllable", "This channel cannot be controlled from the web UI.")
		return ch, nil, false
	}
	return ch, ctl, true
}

func (s *Server) handleChannelConnect(w http.ResponseWriter, r *http.Request) {
	ch, ctl, ok := s.channelController(w, r)
	if !ok {
		return
	}
	if err := ctl.Connect(r.Context()); err != nil {
		writeError(w, http.StatusBadRequest, "connect_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": ch.Status()})
}

func (s *Server) handleChannelDisconnect(w http.ResponseWriter, r *http.Request) {
	ch, ctl, ok := s.channelController(w, r)
	if !ok {
		return
	}
	if err := ctl.Disconnect(); err != nil {
		writeError(w, http.StatusBadRequest, "disconnect_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": ch.Status()})
}

func (s *Server) handleChannelLogout(w http.ResponseWriter, r *http.Request) {
	ch, ctl, ok := s.channelController(w, r)
	if !ok {
		return
	}
	if err := ctl.Logout(r.Context()); err != nil {
		writeError(w, http.StatusBadRequest, "logout_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": ch.Status()})
}

func (s *Server) handleChannelQR(w http.ResponseWriter, r *http.Request) {
	_, ctl, ok := s.channelController(w, r)
	if !ok {
		return
	}
	png, err := ctl.QR(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, "qr_failed", err.Error())
		return
	}
	if len(png) == 0 {
		writeError(w, http.StatusNotFound, "no_qr", "No pairing QR is available. Start the channel first.")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", "inline; filename=\"whatsapp-qr.png\"")
	_, _ = w.Write(png)
}

func (s *Server) handleChannelSettings(w http.ResponseWriter, r *http.Request) {
	_, ctl, ok := s.channelController(w, r)
	if !ok {
		return
	}
	settings, err := ctl.Settings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "settings", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
}

func (s *Server) handleUpdateChannelSettings(w http.ResponseWriter, r *http.Request) {
	ch, ctl, ok := s.channelController(w, r)
	if !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, 256<<10)
	raw, err := io.ReadAll(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Settings payload is too large.")
		return
	}
	if !json.Valid(raw) || !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		writeError(w, http.StatusBadRequest, "bad_request", "Settings must be a JSON object.")
		return
	}
	if err := ctl.UpdateSettings(r.Context(), raw); err != nil {
		writeError(w, http.StatusBadRequest, "settings_failed", err.Error())
		return
	}
	settings, _ := ctl.Settings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings, "channel": ch.Status()})
}
