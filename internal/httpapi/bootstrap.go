package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/store"
)

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	agents, _ := s.store.ListAgents(ctx)
	publicAgents := make([]store.Agent, 0, len(agents))
	for _, a := range agents {
		publicAgents = append(publicAgents, a.Public())
	}
	convs, _ := s.store.ListConversations(ctx, store.ConversationFilter{Limit: 250})
	approvals, _ := s.store.ListPendingApprovals(ctx, 100)
	mcp, _ := s.store.ListAllMCP(ctx)
	publicMCP := make(map[string][]store.MCPServer, len(mcp))
	for id, list := range mcp {
		pub := make([]store.MCPServer, 0, len(list))
		for _, m := range list {
			pub = append(pub, m.Public())
		}
		publicMCP[id] = pub
	}
	total, active, pending, _ := s.store.ConversationStats(ctx)
	_, approved, denied, _ := s.store.ApprovalCounts(ctx)
	routing := store.DefaultRouting()
	_ = s.store.GetJSONSetting(ctx, "routing.defaults", &routing)
	installed, _ := s.store.GetSetting(ctx, "app.installed_at")
	var agentDefaults store.AgentDefaults
	_ = s.store.GetJSONSetting(ctx, "agent.defaults", &agentDefaults)

	resp := map[string]any{
		"app": map[string]any{
			"name": config.AppName, "version": config.Version, "publicUrl": s.cfg.PublicURL,
			"installedAt": installed, "dev": s.cfg.Dev, "whatsappDisabled": s.cfg.DisableWhatsApp,
			"dataDir": s.cfg.DataDir,
		},
		"agents":          publicAgents,
		"mcp":             publicMCP,
		"channels":        s.channelStatuses(),
		"conversations":   convs,
		"approvals":       approvals,
		"routingDefaults": routing,
		"agentDefaults":   agentDefaults,
		"stats": map[string]any{
			"conversations": total, "active": active, "pendingApprovals": pending,
			"approvedApprovals": approved, "deniedApprovals": denied,
			"agents": len(agents), "unread": 0,
		},
	}
	if c, ok := s.channelByName("whatsapp"); ok {
		if ctl, ok := c.(channels.Controller); ok {
			if settings, err := ctl.Settings(ctx); err == nil {
				resp["whatsapp"] = settings
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) channelStatuses() []channels.Status {
	if s.channels == nil {
		return []channels.Status{}
	}
	return s.channels.Statuses()
}

func (s *Server) channelByName(name string) (channels.Channel, bool) {
	if s.channels == nil {
		return nil, false
	}
	return s.channels.Get(name)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	total, active, pending, err := s.store.ConversationStats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats", err.Error())
		return
	}
	ap, approved, denied, _ := s.store.ApprovalCounts(ctx)
	_ = ap
	agents, _ := s.store.ListAgents(ctx)
	connected := 0
	for _, c := range s.channelStatuses() {
		if c.Connected {
			connected++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conversations": total, "active": active, "pendingApprovals": pending,
		"approvedApprovals": approved, "deniedApprovals": denied,
		"agents": len(agents), "channelsConnected": connected,
	})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	routing := store.DefaultRouting()
	_ = s.store.GetJSONSetting(r.Context(), "routing.defaults", &routing)
	installed, _ := s.store.GetSetting(r.Context(), "app.installed_at")
	writeJSON(w, http.StatusOK, map[string]any{
		"routing": routing, "installedAt": installed, "publicUrl": s.cfg.PublicURL,
		"version": config.Version,
	})
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Routing *store.Routing `json:"routing"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid settings payload.")
		return
	}
	if in.Routing != nil {
		if err := s.store.SetJSONSetting(r.Context(), "routing.defaults", in.Routing); err != nil {
			writeError(w, http.StatusInternalServerError, "settings", err.Error())
			return
		}
	}
	s.handleGetSettings(w, r)
}

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListPendingApprovals(r.Context(), parseInt(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "approvals", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": list})
}

func (s *Server) handleListApprovalRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListApprovalRules(r.Context(), r.URL.Query().Get("agentId"), r.URL.Query().Get("conversationId"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "rules", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (s *Server) handleDeleteApprovalRule(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteApprovalRule(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, "rules", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) whatsappSettings(ctx context.Context) (any, bool) {
	ch, ok := s.channelByName("whatsapp")
	if !ok {
		return nil, false
	}
	ctl, ok := ch.(channels.Controller)
	if !ok {
		return nil, false
	}
	settings, err := ctl.Settings(ctx)
	return settings, err == nil
}

func decodeJSONObject(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}
