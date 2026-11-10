package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/ids"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

const (
	controlListLimit     = 30
	controlListMax       = 100
	controlMessageLimit  = 30
	controlMessageMax    = 6000
	controlResponseMax   = 48 << 10
	controlWaitDefault   = 10
	controlWaitMax       = 30
	controlWaitPerCaller = 4
	controlWaitPoll      = 750 * time.Millisecond
)

type conversationControlRequest struct {
	Tool              string   `json:"tool"`
	ConversationID    string   `json:"conversationId"`
	Query             string   `json:"query"`
	Agent             string   `json:"agent"`
	Channel           string   `json:"channel"`
	Status            string   `json:"status"`
	Archived          bool     `json:"archived"`
	Limit             int      `json:"limit"`
	Offset            int      `json:"offset"`
	AfterSeq          int64    `json:"afterSeq"`
	BeforeSeq         int64    `json:"beforeSeq"`
	IncludeThoughts   bool     `json:"includeThoughts"`
	IncludeToolOutput bool     `json:"includeToolOutput"`
	Format            string   `json:"format"`
	Kinds             []string `json:"kinds"`
	After             int64    `json:"after"`
	Before            int64    `json:"before"`
	TurnID            string   `json:"turnId"`
	TimeoutSeconds    int      `json:"timeoutSeconds"`
	Text              string   `json:"text"`
	Delivery          string   `json:"delivery"`
	RequestID         string   `json:"requestId"`
	Title             string   `json:"title"`
	Prompt            string   `json:"prompt"`
	ProjectID         string   `json:"projectId"`
	Project           string   `json:"project"`
}

type controlAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type controlTurn struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	StopReason string `json:"stopReason,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  int64  `json:"startedAt,omitempty"`
	EndedAt    int64  `json:"endedAt,omitempty"`
}

type controlTurnSummary struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	StopReason string `json:"stopReason,omitempty"`
	Error      string `json:"error,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
	Answer     string `json:"answer,omitempty"`
	At         int64  `json:"at"`
}

type controlLastMessage struct {
	Role    string `json:"role,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Preview string `json:"preview,omitempty"`
	At      int64  `json:"at,omitempty"`
}

type controlConversation struct {
	ID               string              `json:"id"`
	Title            string              `json:"title"`
	Agent            controlAgent        `json:"agent"`
	Status           string              `json:"status"`
	ActiveTurn       *controlTurn        `json:"activeTurn,omitempty"`
	QueuedTurns      int                 `json:"queuedTurns,omitempty"`
	PendingApprovals int                 `json:"pendingApprovals,omitempty"`
	LastMessage      *controlLastMessage `json:"lastMessage,omitempty"`
	OriginChannel    string              `json:"originChannel,omitempty"`
	ProjectID        string              `json:"projectId,omitempty"`
	ProjectName      string              `json:"projectName,omitempty"`
	Pinned           bool                `json:"pinned,omitempty"`
	Archived         bool                `json:"archived,omitempty"`
	MessageCount     int                 `json:"messageCount"`
	UpdatedAt        int64               `json:"updatedAt"`
	Watchers         int                 `json:"watchers,omitempty"`
	LastTurn         *controlTurnSummary `json:"lastTurn,omitempty"`
	IsSelf           bool                `json:"isSelf,omitempty"`
}

type controlApproval struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	Risk      string `json:"risk,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

type controlProject struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	RootPaths         []string `json:"rootPaths,omitempty"`
	Pinned            bool     `json:"pinned,omitempty"`
	ConversationCount int      `json:"conversationCount"`
	UpdatedAt         int64    `json:"updatedAt"`
}

type controlMessage struct {
	Seq        int64  `json:"seq"`
	ID         string `json:"id"`
	Role       string `json:"role"`
	Kind       string `json:"kind"`
	Status     string `json:"status,omitempty"`
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	At         int64  `json:"at"`
	Truncated  bool   `json:"truncated,omitempty"`
}

var errControlScope = errors.New("target is outside the allowed project scope")

const controlSendTTL = 10 * time.Minute

type controlSendRecord struct {
	MessageID      string
	TurnID         string
	ConversationID string
	Delivery       string
	At             time.Time
}

func (s *Server) lookupControlSend(callerID, requestID string) (controlSendRecord, bool) {
	if requestID == "" {
		return controlSendRecord{}, false
	}
	key := callerID + "\x00" + requestID
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	record, ok := s.sends[key]
	if !ok {
		return controlSendRecord{}, false
	}
	if time.Since(record.At) > controlSendTTL {
		delete(s.sends, key)
		return controlSendRecord{}, false
	}
	return record, true
}

func (s *Server) reserveControlSend(callerID, requestID string) bool {
	if requestID == "" {
		return true
	}
	key := callerID + "\x00" + requestID
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sendInFlight == nil {
		s.sendInFlight = map[string]bool{}
	}
	if s.sendInFlight[key] {
		return false
	}
	s.sendInFlight[key] = true
	return true
}

func (s *Server) releaseControlSend(callerID, requestID string) {
	if requestID == "" {
		return
	}
	key := callerID + "\x00" + requestID
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	delete(s.sendInFlight, key)
}

func (s *Server) rememberControlSend(callerID, requestID, messageID, turnID, conversationID string) {
	if requestID == "" {
		return
	}
	key := callerID + "\x00" + requestID
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sends == nil {
		s.sends = map[string]controlSendRecord{}
	}
	s.sends[key] = controlSendRecord{
		MessageID: messageID, TurnID: turnID, ConversationID: conversationID, At: time.Now(),
	}
}

// handleConversationControl is the loopback callback for the
// nusamation_conversation MCP process. The bearer token selects the caller
// conversation; the body cannot retarget it.
func (s *Server) handleConversationControl(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &controlResponseRecorder{ResponseWriter: w}
	w = rec
	callerID, ok := s.conversationControlCaller(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Unknown conversation control token.")
		return
	}
	var in conversationControlRequest
	if err := readJSONMode(w, r, &in, 1<<20, false); err != nil {
		s.audit.add(controlAuditEntry{
			At: time.Now().UnixMilli(), Caller: callerID, Tool: "invalid",
			Outcome: "error", DurationMS: time.Since(start).Milliseconds(),
		})
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid conversation control request.")
		return
	}
	in.Tool = strings.TrimSpace(in.Tool)
	defer func() {
		outcome := "ok"
		if rec.status >= 400 {
			outcome = "error"
		}
		s.audit.add(controlAuditEntry{
			At: time.Now().UnixMilli(), Caller: callerID, Tool: in.Tool, Target: in.ConversationID,
			Outcome: outcome, DurationMS: time.Since(start).Milliseconds(),
		})
		if isControlWrite(in.Tool) && s.hub != nil {
			target := in.ConversationID
			if target == "" {
				target = callerID
			}
			s.hub.Broadcast(events.New("conversation.control", target, map[string]any{
				"caller": callerID, "tool": in.Tool, "outcome": outcome,
			}))
		}
	}()
	switch in.Tool {
	case "list":
		s.controlList(w, r, callerID, in)
	case "projects_list":
		s.controlProjects(w, r, callerID, in)
	case "search":
		s.controlSearch(w, r, callerID, in)
	case "export":
		s.controlExport(w, r, callerID, in)
	case "read":
		s.controlRead(w, r, callerID, in)
	case "wait":
		s.controlWait(w, r, callerID, in)
	case "send":
		s.controlSend(w, r, callerID, in)
	case "stop":
		s.controlStop(w, r, callerID, in)
	case "spawn":
		s.controlSpawn(w, r, callerID, in)
	case "report":
		s.controlReport(w, r, callerID, in)
	default:
		writeError(w, http.StatusBadRequest, "unknown_tool", "Unknown conversation control tool.")
	}
}

func (s *Server) conversationControlCaller(r *http.Request) (string, bool) {
	if s.conversation == nil {
		return "", false
	}
	h := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || token == "" || token == h {
		return "", false
	}
	convID, ok := s.conversation.Conversation(token)
	if !ok {
		return "", false
	}
	if s.store != nil {
		if _, err := s.store.GetConversation(r.Context(), convID); err != nil {
			return "", false
		}
	}
	return convID, true
}

type controlPolicy struct {
	Caller       store.Conversation
	Global       bool
	Enabled      bool
	ReadOnly     bool
	Orchestrated bool
	Scope        string
}

// conversationPolicy resolves the kill switch, the per-conversation opt-in,
// and whether this turn was itself started through orchestration. Every
// failure path leaves control disabled.
func (s *Server) conversationPolicy(ctx context.Context, callerID string) (controlPolicy, error) {
	caller, err := s.store.GetConversation(ctx, callerID)
	if err != nil {
		return controlPolicy{}, err
	}
	p := controlPolicy{Caller: caller, Scope: "all"}
	if !s.orchestrationEnabled(ctx) {
		return p, nil
	}
	p.Global = true
	policy := chat.OrchestrationFrom(caller.Metadata)
	p.Scope = policy.Scope
	if !policy.Enabled {
		return p, nil
	}
	turn, err := s.store.ActiveTurn(ctx, callerID)
	if err != nil {
		return p, nil
	}
	if turn.OriginChannel == "agent" {
		p.Orchestrated = true
		return p, nil
	}
	p.Enabled = true
	p.ReadOnly = policy.ReadOnly
	return p, nil
}

func controlDenied(w http.ResponseWriter, p controlPolicy) bool {
	switch {
	case !p.Global:
		writeError(w, http.StatusForbidden, "orchestration_disabled", "Orchestration is disabled globally.")
		return true
	case p.Orchestrated:
		writeError(w, http.StatusForbidden, "orchestrated_turn", "This turn was started by another conversation and cannot control others.")
		return true
	case !p.Enabled:
		writeError(w, http.StatusForbidden, "policy_denied", "Orchestration is not enabled for this conversation.")
		return true
	}
	return false
}

func controlCaller(p controlPolicy) map[string]any {
	return map[string]any{
		"id": p.Caller.ID, "title": p.Caller.Title,
		"readOnly": p.ReadOnly, "canManage": p.Enabled && !p.ReadOnly,
		"scope": p.Scope,
	}
}

func controlTargetAllowed(p controlPolicy, target store.Conversation) error {
	if target.ID == p.Caller.ID {
		return nil
	}
	if p.Scope == "project" {
		if p.Caller.ProjectID == "" || target.ProjectID != p.Caller.ProjectID {
			return errControlScope
		}
	}
	return nil
}

func (s *Server) resolveControlProject(ctx context.Context, ref string) (store.Project, bool, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return store.Project{}, false, false
	}
	if project, err := s.store.GetProject(ctx, ref); err == nil {
		return project, true, false
	}
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return store.Project{}, false, false
	}
	matches := []store.Project{}
	seen := map[string]bool{}
	for _, project := range projects {
		matched := strings.EqualFold(strings.TrimSpace(project.Name), ref)
		if !matched {
			for _, root := range project.RootPaths {
				if strings.EqualFold(filepath.Base(root), ref) {
					matched = true
					break
				}
			}
		}
		if matched && !seen[project.ID] {
			seen[project.ID] = true
			matches = append(matches, project)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], true, false
	case 0:
		return store.Project{}, false, false
	default:
		return store.Project{}, false, true
	}
}

func (s *Server) controlProjects(w http.ResponseWriter, r *http.Request, callerID string, in conversationControlRequest) {
	p, err := s.conversationPolicy(r.Context(), callerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	if controlDenied(w, p) {
		return
	}
	projects, err := s.store.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if p.Scope == "project" {
		filtered := make([]store.Project, 0, 1)
		for _, project := range projects {
			if project.ID == p.Caller.ProjectID {
				filtered = append(filtered, project)
			}
		}
		projects = filtered
	}
	query := strings.ToLower(strings.TrimSpace(in.Query))
	out := make([]controlProject, 0, len(projects))
	for _, project := range projects {
		if query != "" {
			matched := strings.Contains(strings.ToLower(project.Name), query)
			for _, root := range project.RootPaths {
				if strings.Contains(strings.ToLower(root), query) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		count, _ := s.store.CountProjectConversations(r.Context(), project.ID)
		out = append(out, controlProject{
			ID: project.ID, Name: project.Name, RootPaths: project.RootPaths,
			Pinned: project.Pinned, ConversationCount: count, UpdatedAt: project.UpdatedAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"caller": controlCaller(p), "projects": out})
}

func (s *Server) controlList(w http.ResponseWriter, r *http.Request, callerID string, in conversationControlRequest) {
	p, err := s.conversationPolicy(r.Context(), callerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	if controlDenied(w, p) {
		return
	}
	limit := in.Limit
	if limit <= 0 {
		limit = controlListLimit
	}
	if limit > controlListMax {
		limit = controlListMax
	}
	offset := max(in.Offset, 0)

	requestedProjectID := ""
	if strings.TrimSpace(in.Project) != "" {
		project, found, ambiguous := s.resolveControlProject(r.Context(), in.Project)
		switch {
		case ambiguous:
			writeError(w, http.StatusBadRequest, "ambiguous_project", "Project name matches more than one project.")
			return
		case !found:
			writeError(w, http.StatusBadRequest, "invalid_project", "Unknown project.")
			return
		default:
			requestedProjectID = project.ID
		}
	}
	if p.Scope == "project" && p.Caller.ProjectID == "" && requestedProjectID != "" {
		writeError(w, http.StatusForbidden, "policy_denied", "This conversation has no project scope.")
		return
	}
	if p.Scope == "project" && p.Caller.ProjectID != "" && requestedProjectID != "" && requestedProjectID != p.Caller.ProjectID {
		writeError(w, http.StatusForbidden, "policy_denied", "The project is outside the allowed scope.")
		return
	}

	list := []store.Conversation{}
	if p.Scope == "project" && p.Caller.ProjectID == "" {
		list = append(list, p.Caller)
	} else {
		filter := store.ConversationFilter{
			Query: in.Query, Channel: in.Channel, Status: in.Status,
			Archived: in.Archived, Limit: limit, Offset: offset,
		}
		if p.Scope == "project" {
			filter.ProjectID = p.Caller.ProjectID
		} else if requestedProjectID != "" {
			filter.ProjectID = requestedProjectID
		}
		if in.Agent != "" {
			agentID, ok := s.resolveControlAgent(r.Context(), in.Agent)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_agent", "Unknown agent.")
				return
			}
			filter.AgentID = agentID
		}
		list, err = s.store.ListConversations(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}
	}
	projectNames := map[string]string{}
	if projects, err := s.store.ListProjects(r.Context()); err == nil {
		for _, project := range projects {
			projectNames[project.ID] = project.Name
		}
	}
	ids := make([]string, 0, len(list))
	for _, conv := range list {
		ids = append(ids, conv.ID)
	}
	lastTurns, _ := s.store.ListLastTurnSummaries(r.Context(), ids)
	out := make([]controlConversation, 0, len(list))
	for _, conv := range list {
		out = append(out, s.controlConversationView(r.Context(), conv, p, false, projectNames, lastTurns))
	}
	next := 0
	if len(list) == limit && (p.Scope != "project" || p.Caller.ProjectID != "") {
		next = offset + len(list)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"caller": controlCaller(p), "conversations": out, "nextOffset": next,
	})
}

func (s *Server) controlSearch(w http.ResponseWriter, r *http.Request, callerID string, in conversationControlRequest) {
	p, err := s.conversationPolicy(r.Context(), callerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	if controlDenied(w, p) {
		return
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query is required.")
		return
	}
	allowedKinds := map[string]bool{"text": true, "thought": true, "tool": true, "system": true, "error": true, "notice": true}
	kinds := in.Kinds
	if len(kinds) == 0 {
		kinds = []string{"text", "error"}
	}
	for _, kind := range kinds {
		if !allowedKinds[kind] {
			writeError(w, http.StatusBadRequest, "invalid_kind", "Unknown message kind: "+kind)
			return
		}
	}
	requestedProjectID := ""
	if strings.TrimSpace(in.Project) != "" {
		project, found, ambiguous := s.resolveControlProject(r.Context(), in.Project)
		switch {
		case ambiguous:
			writeError(w, http.StatusBadRequest, "ambiguous_project", "Project name matches more than one project.")
			return
		case !found:
			writeError(w, http.StatusBadRequest, "invalid_project", "Unknown project.")
			return
		default:
			requestedProjectID = project.ID
		}
	}
	agentID := ""
	if in.Agent != "" {
		agentID, _ = s.resolveControlAgent(r.Context(), in.Agent)
		if agentID == "" {
			writeError(w, http.StatusBadRequest, "invalid_agent", "Unknown agent.")
			return
		}
	}
	filter := store.MessageSearchFilter{
		Query: query, ConversationID: in.ConversationID, AgentID: agentID,
		Channel: in.Channel, Kinds: kinds, After: in.After, Before: in.Before,
		Archived: in.Archived, Limit: in.Limit, Offset: max(in.Offset, 0),
	}
	if filter.Limit <= 0 {
		filter.Limit = 20
	}
	if filter.Limit > 50 {
		filter.Limit = 50
	}
	if p.Scope == "project" {
		if p.Caller.ProjectID == "" {
			if requestedProjectID != "" {
				writeError(w, http.StatusForbidden, "policy_denied", "This conversation has no project scope.")
				return
			}
			filter.ConversationID = p.Caller.ID
		} else {
			if requestedProjectID != "" && requestedProjectID != p.Caller.ProjectID {
				writeError(w, http.StatusForbidden, "policy_denied", "The project is outside the allowed scope.")
				return
			}
			filter.ProjectID = p.Caller.ProjectID
		}
	} else if requestedProjectID != "" {
		filter.ProjectID = requestedProjectID
	}
	if in.ConversationID != "" {
		target, err := s.store.GetConversation(r.Context(), in.ConversationID)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "Target conversation not found.")
			return
		}
		if err := controlTargetAllowed(p, target); err != nil {
			writeError(w, http.StatusForbidden, "policy_denied", err.Error())
			return
		}
		filter.ConversationID = target.ID
	}
	hits, err := s.store.SearchMessages(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search_failed", err.Error())
		return
	}
	projectNames := map[string]string{}
	if projects, err := s.store.ListProjects(r.Context()); err == nil {
		for _, project := range projects {
			projectNames[project.ID] = project.Name
		}
	}
	out := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		out = append(out, map[string]any{
			"conversationId": hit.ConversationID, "title": hit.ConversationTitle,
			"projectId": hit.ProjectID, "projectName": projectNames[hit.ProjectID],
			"messageId": hit.MessageID, "seq": hit.Seq, "role": hit.Role, "kind": hit.Kind,
			"snippet": hit.Snippet, "score": hit.Score, "at": hit.At,
		})
	}
	hasMore := len(hits) == filter.Limit
	writeJSON(w, http.StatusOK, map[string]any{
		"caller": controlCaller(p), "query": query, "hits": out,
		"hasMore": hasMore, "nextOffset": filter.Offset + len(hits),
	})
}

func (s *Server) controlExport(w http.ResponseWriter, r *http.Request, callerID string, in conversationControlRequest) {
	p, err := s.conversationPolicy(r.Context(), callerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	if controlDenied(w, p) {
		return
	}
	format := strings.ToLower(strings.TrimSpace(in.Format))
	if format == "" {
		format = "jsonl"
	}
	if format != "jsonl" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Only jsonl export is supported.")
		return
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10000
	}
	if limit > 100000 {
		limit = 100000
	}
	targets := []store.Conversation{}
	switch {
	case in.ConversationID != "":
		target, err := s.store.GetConversation(r.Context(), in.ConversationID)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "Target conversation not found.")
			return
		}
		if err := controlTargetAllowed(p, target); err != nil {
			writeError(w, http.StatusForbidden, "policy_denied", err.Error())
			return
		}
		targets = append(targets, target)
	case in.Project != "":
		project, found, ambiguous := s.resolveControlProject(r.Context(), in.Project)
		switch {
		case ambiguous:
			writeError(w, http.StatusBadRequest, "ambiguous_project", "Project name matches more than one project.")
			return
		case !found:
			writeError(w, http.StatusBadRequest, "invalid_project", "Unknown project.")
			return
		}
		if p.Scope == "project" && project.ID != p.Caller.ProjectID {
			writeError(w, http.StatusForbidden, "policy_denied", "The project is outside the allowed scope.")
			return
		}
		list, err := s.store.ListConversations(r.Context(), store.ConversationFilter{
			ProjectID: project.ID, Archived: in.Archived, Limit: 500,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
			return
		}
		targets = append(targets, list...)
	default:
		targets = append(targets, p.Caller)
	}
	if len(targets) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "No conversations matched the export.")
		return
	}
	dir := s.controlExportDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	s.cleanupControlExports(dir)
	tmp, err := os.CreateTemp(dir, ".export-*.jsonl")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	bw := bufio.NewWriterSize(tmp, 64<<10)
	count, size := 0, 0
	truncated := false
	const maxBytes = 64 << 20
outer:
	for _, target := range targets {
		after := int64(0)
		for {
			msgs, err := s.store.ListMessagesAscending(r.Context(), target.ID, after, 1000)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
				return
			}
			if len(msgs) == 0 {
				break
			}
			for _, m := range msgs {
				if m.Kind == "thought" && !in.IncludeThoughts {
					continue
				}
				if count >= limit {
					truncated = true
					break outer
				}
				line := map[string]any{
					"conversationId": target.ID, "title": target.Title,
					"seq": m.Seq, "id": m.ID, "turnId": m.TurnID,
					"role": m.Role, "kind": m.Kind, "status": m.Status,
					"text": m.Text, "toolCallId": m.ToolCallID, "at": m.CreatedAt,
				}
				if m.Kind == "tool" && in.IncludeToolOutput && len(m.Content) > 0 {
					line["content"] = m.Content
				}
				raw, err := json.Marshal(line)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
					return
				}
				if size+len(raw)+1 > maxBytes {
					truncated = true
					break outer
				}
				if _, err := bw.Write(raw); err != nil {
					writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
					return
				}
				if err := bw.WriteByte('\n'); err != nil {
					writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
					return
				}
				size += len(raw) + 1
				count++
			}
			after = msgs[len(msgs)-1].Seq
			if len(msgs) < 1000 {
				break
			}
		}
	}
	if err := bw.Flush(); err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	if err := tmp.Sync(); err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	path := filepath.Join(dir, "conversation-"+ids.Token(10)+".jsonl")
	if err := os.Rename(tmpName, path); err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	_ = os.Chmod(path, 0o600)
	expires := time.Now().Add(10 * time.Minute)
	time.AfterFunc(10*time.Minute, func() { _ = os.Remove(path) })
	writeJSON(w, http.StatusOK, map[string]any{
		"path": path, "format": "jsonl", "messages": count, "bytes": size,
		"conversations": len(targets), "truncated": truncated, "expiresAt": expires.UnixMilli(),
	})
}

func (s *Server) controlExportDir() string {
	if s.cfg.DataDir != "" {
		return filepath.Join(s.cfg.DataDir, "exports")
	}
	return filepath.Join(os.TempDir(), "nusamation-exports")
}

func (s *Server) cleanupControlExports(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

func (s *Server) controlRead(w http.ResponseWriter, r *http.Request, callerID string, in conversationControlRequest) {
	p, err := s.conversationPolicy(r.Context(), callerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	if controlDenied(w, p) {
		return
	}
	target, err := s.store.GetConversation(r.Context(), in.ConversationID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Target conversation not found.")
		return
	}
	if err := controlTargetAllowed(p, target); err != nil {
		writeError(w, http.StatusForbidden, "policy_denied", err.Error())
		return
	}
	limit := in.Limit
	if limit <= 0 {
		limit = controlMessageLimit
	}
	if limit > controlListMax {
		limit = controlListMax
	}
	msgs, err := s.store.ListMessages(r.Context(), target.ID, in.AfterSeq, in.BeforeSeq, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	views, hasMore, lastSeq := controlMessageViews(msgs, in.IncludeThoughts)
	if len(msgs) == limit {
		hasMore = true
	}
	prog, _ := s.store.ConversationProgress(r.Context(), target.ID)
	next := lastSeq
	if next == 0 {
		next = in.AfterSeq
	}
	approvals, _ := s.store.ListApprovalsForConversation(r.Context(), target.ID, 50)
	queued, _ := s.store.ListQueuedTurnsForConversation(r.Context(), target.ID)
	queuedViews := make([]*controlTurn, 0, len(queued))
	for i := range queued {
		queuedViews = append(queuedViews, controlTurnView(&queued[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"caller": controlCaller(p), "conversation": s.controlConversationView(r.Context(), target, p, true, nil, nil),
		"activeTurn": controlTurnView(prog.Active), "queuedTurns": queuedViews,
		"pendingApprovals": controlApprovalViews(approvals), "messages": views,
		"nextAfterSeq": next, "hasMore": hasMore, "headSeq": prog.HeadSeq,
	})
}

func (s *Server) controlWait(w http.ResponseWriter, r *http.Request, callerID string, in conversationControlRequest) {
	p, err := s.conversationPolicy(r.Context(), callerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	if controlDenied(w, p) {
		return
	}
	target, err := s.store.GetConversation(r.Context(), in.ConversationID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Target conversation not found.")
		return
	}
	if err := controlTargetAllowed(p, target); err != nil {
		writeError(w, http.StatusForbidden, "policy_denied", err.Error())
		return
	}
	if !s.acquireControlWait(callerID) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many concurrent waits for this conversation.")
		return
	}
	defer s.releaseControlWait(callerID)
	s.registerControlWatch(target.ID, callerID)
	defer s.unregisterControlWatch(target.ID, callerID)

	timeout := in.TimeoutSeconds
	if timeout <= 0 {
		timeout = controlWaitDefault
	}
	if timeout > controlWaitMax {
		timeout = controlWaitMax
	}
	prog, err := s.store.ConversationProgress(r.Context(), target.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "wait_failed", err.Error())
		return
	}
	after := in.AfterSeq
	if after <= 0 {
		after = prog.HeadSeq
	}
	var watched *store.Turn
	if in.TurnID != "" {
		turn, err := s.store.GetTurn(r.Context(), in.TurnID)
		if err != nil || turn.ConversationID != target.ID {
			writeError(w, http.StatusNotFound, "not_found", "Turn not found in this conversation.")
			return
		}
		watched = &turn
	} else if prog.Active != nil {
		watched = prog.Active
	}

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for {
		if _, err := s.store.GetConversation(r.Context(), target.ID); err != nil {
			writeError(w, http.StatusNotFound, "not_found", "Target conversation not found.")
			return
		}
		current, err := s.store.ConversationProgress(r.Context(), target.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "wait_failed", err.Error())
			return
		}
		changed := current.HeadSeq > after
		turnDone := false
		if watched != nil {
			if turn, err := s.store.GetTurn(r.Context(), watched.ID); err == nil {
				watched = &turn
				turnDone = controlTurnTerminal(turn.Status)
			}
		}
		if changed || turnDone {
			s.writeControlWait(w, r, p, target.ID, current, watched, after, false)
			return
		}
		if time.Now().After(deadline) {
			s.writeControlWait(w, r, p, target.ID, current, watched, after, true)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(controlWaitPoll):
		}
	}
}

func (s *Server) writeControlWait(w http.ResponseWriter, r *http.Request, p controlPolicy, id string, prog store.ConversationProgress, turn *store.Turn, after int64, timedOut bool) {
	target, err := s.store.GetConversation(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Target conversation not found.")
		return
	}
	msgs, _ := s.store.ListMessages(r.Context(), id, after, 0, 20)
	views, hasMore, _ := controlMessageViews(msgs, false)
	hasMore = hasMore || len(msgs) == 20
	approvals, _ := s.store.ListApprovalsForConversation(r.Context(), id, 50)
	writeJSON(w, http.StatusOK, map[string]any{
		"caller": controlCaller(p), "conversation": s.controlConversationView(r.Context(), target, p, true, nil, nil),
		"changed": !timedOut, "timedOut": timedOut, "headSeq": prog.HeadSeq,
		"activeTurn": controlTurnView(prog.Active), "turn": controlTurnView(turn),
		"pendingApprovals": controlApprovalViews(approvals), "messages": views, "hasMore": hasMore,
	})
}

func (s *Server) controlTarget(w http.ResponseWriter, r *http.Request, callerID, targetID string) (controlPolicy, store.Conversation, bool) {
	if strings.TrimSpace(targetID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "conversationId is required.")
		return controlPolicy{}, store.Conversation{}, false
	}
	p, err := s.conversationPolicy(r.Context(), callerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return controlPolicy{}, store.Conversation{}, false
	}
	if controlDenied(w, p) {
		return controlPolicy{}, store.Conversation{}, false
	}
	target, err := s.store.GetConversation(r.Context(), targetID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Target conversation not found.")
		return controlPolicy{}, store.Conversation{}, false
	}
	if err := controlTargetAllowed(p, target); err != nil {
		writeError(w, http.StatusForbidden, "policy_denied", err.Error())
		return controlPolicy{}, store.Conversation{}, false
	}
	return p, target, true
}

func isControlWrite(tool string) bool {
	switch tool {
	case "send", "stop", "spawn", "report":
		return true
	default:
		return false
	}
}

func controlNote(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, " ")
}

func (s *Server) controlSend(w http.ResponseWriter, r *http.Request, callerID string, in conversationControlRequest) {
	p, target, ok := s.controlTarget(w, r, callerID, in.ConversationID)
	if !ok {
		return
	}
	if p.ReadOnly {
		writeError(w, http.StatusForbidden, "policy_denied", "This conversation is read-only.")
		return
	}
	if target.ID == p.Caller.ID {
		writeError(w, http.StatusForbidden, "target_self", "A conversation cannot send to itself.")
		return
	}
	if target.Archived {
		writeError(w, http.StatusConflict, "target_archived", "The target conversation is archived.")
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "text is required.")
		return
	}
	if len(text) > 32000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "text exceeds 32000 bytes.")
		return
	}
	if len(in.RequestID) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_request", "requestId exceeds 100 characters.")
		return
	}
	if !s.reserveControlSend(callerID, in.RequestID) {
		writeError(w, http.StatusConflict, "duplicate_pending", "A send with this requestId is already in progress.")
		return
	}
	defer s.releaseControlSend(callerID, in.RequestID)
	if record, ok := s.lookupControlSend(callerID, in.RequestID); ok {
		targetView := map[string]any{"id": record.ConversationID}
		if conv, err := s.store.GetConversation(r.Context(), record.ConversationID); err == nil {
			targetView["title"] = conv.Title
			targetView["status"] = conv.Status
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"duplicate": true,
			"message":   map[string]any{"id": record.MessageID},
			"turn":      map[string]any{"id": record.TurnID},
			"delivery":  record.Delivery,
			"target":    targetView,
			"note":      "Duplicate requestId; the original message was not resent.",
		})
		return
	}
	if s.chat == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Conversation service is not available.")
		return
	}
	agent, err := s.store.GetAgent(r.Context(), target.AgentID)
	if err != nil {
		writeError(w, http.StatusConflict, "target_agent_missing", "The target agent no longer exists.")
		return
	}
	if !agent.Enabled {
		writeError(w, http.StatusConflict, "target_agent_disabled", "The target agent is disabled.")
		return
	}
	prog, err := s.store.ConversationProgress(r.Context(), target.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "progress_failed", err.Error())
		return
	}
	if prog.PendingApprovals > 0 {
		approvals, _ := s.store.ListApprovalsForConversation(r.Context(), target.ID, 20)
		ids := make([]string, 0, len(approvals))
		for _, ap := range approvals {
			if ap.Status == "pending" {
				ids = append(ids, ap.ID)
			}
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "The target is waiting for approval; answer it before sending another prompt.", "code": "target_waiting_for_approval",
			"approvalIds": ids,
		})
		return
	}

	delivery := strings.TrimSpace(in.Delivery)
	if delivery == "" {
		delivery = "queue"
	}
	switch delivery {
	case "queue", "steer", "after_tools":
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "delivery must be queue, steer, or after_tools.")
		return
	}
	var notes []string
	if delivery == "steer" {
		if prog.Active != nil {
			delivery = "after_tools"
			notes = append(notes, "Cross-conversation steer is delivered as after_tools; the target turn is cancelled after its tools finish.")
		} else {
			delivery = "queue"
			notes = append(notes, "Cross-conversation steer on an idle target is queued as a new turn.")
		}
	}
	if p.Caller.AgentID == target.AgentID && delivery != "queue" {
		delivery = "queue"
		notes = append(notes, "Same agent process: queued instead of a synchronous steer.")
	}
	if allowed, retry := s.writes.allow(callerID, 20.0/60, 5); !allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": "Too many writes from this conversation.", "code": "rate_limited",
			"retryAfterMs": retry.Milliseconds(),
		})
		return
	}

	orch := &chat.Orchestrator{ConversationID: p.Caller.ID, Title: p.Caller.Title, AgentID: p.Caller.AgentID}
	origin, _ := json.Marshal(orch)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	msg, turn, err := s.chat.Submit(ctx, chat.SubmitInput{
		ConversationID: target.ID, Text: text, Channel: "agent", OriginRef: string(origin),
		Delivery: delivery, Orchestrator: orch,
	})
	if err != nil {
		if msg.ID != "" || turn.ID != "" {
			s.rememberControlSend(callerID, in.RequestID, msg.ID, turn.ID, target.ID)
			s.rememberControlSendDelivery(callerID, in.RequestID, delivery)
		}
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusGatewayTimeout, "send_timeout", "The send timed out; state is unknown. Read the target before retrying.")
		case errors.Is(err, context.Canceled):
			writeError(w, http.StatusRequestTimeout, "send_cancelled", "The send was cancelled; state is unknown.")
		case errors.Is(err, chat.ErrOrchestratedSteer):
			writeError(w, http.StatusConflict, "steer_not_supported", err.Error())
		case store.IsNotFound(err):
			writeError(w, http.StatusNotFound, "not_found", "Target conversation not found.")
		default:
			writeError(w, http.StatusBadGateway, "send_failed", err.Error())
		}
		return
	}
	s.rememberControlSend(callerID, in.RequestID, msg.ID, turn.ID, target.ID)
	s.rememberControlSendDelivery(callerID, in.RequestID, delivery)
	writeJSON(w, http.StatusOK, map[string]any{
		"duplicate": false,
		"message":   map[string]any{"id": msg.ID, "seq": msg.Seq},
		"turn":      map[string]any{"id": turn.ID, "status": turn.Status},
		"delivery":  delivery,
		"target":    map[string]any{"id": target.ID, "title": target.Title, "status": target.Status},
		"note":      controlNote(notes...),
	})
}

func (s *Server) rememberControlSendDelivery(callerID, requestID, delivery string) {
	if requestID == "" {
		return
	}
	key := callerID + "\x00" + requestID
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	record, ok := s.sends[key]
	if !ok {
		return
	}
	record.Delivery = delivery
	s.sends[key] = record
}

func (s *Server) controlStop(w http.ResponseWriter, r *http.Request, callerID string, in conversationControlRequest) {
	p, target, ok := s.controlTarget(w, r, callerID, in.ConversationID)
	if !ok {
		return
	}
	if p.ReadOnly {
		writeError(w, http.StatusForbidden, "policy_denied", "This conversation is read-only.")
		return
	}
	if target.ID == p.Caller.ID {
		writeError(w, http.StatusForbidden, "target_self", "A conversation cannot stop itself through the bridge.")
		return
	}
	if s.chat == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Conversation service is not available.")
		return
	}
	if allowed, retry := s.writes.allow(callerID, 20.0/60, 5); !allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": "Too many writes from this conversation.", "code": "rate_limited",
			"retryAfterMs": retry.Milliseconds(),
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	targetView := map[string]any{"id": target.ID, "title": target.Title, "status": target.Status}
	if in.TurnID != "" {
		turn, err := s.store.GetTurn(ctx, in.TurnID)
		if err != nil || turn.ConversationID != target.ID {
			writeError(w, http.StatusNotFound, "turn_not_found", "Turn not found in this conversation.")
			return
		}
		switch turn.Status {
		case "queued":
			updated, err := s.chat.CancelQueued(ctx, target.ID, turn.ID, false)
			if err != nil {
				writeError(w, http.StatusConflict, "turn_not_queued", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"stopped": true, "turn": controlTurnView(&updated), "target": targetView})
		case "running", "waiting_approval":
			active, err := s.store.ActiveTurn(ctx, target.ID)
			if err != nil || active.ID != turn.ID {
				writeError(w, http.StatusConflict, "turn_not_running", "That turn is no longer running.")
				return
			}
			if err := s.chat.Cancel(ctx, target.ID); err != nil {
				writeError(w, http.StatusBadGateway, "stop_failed", err.Error())
				return
			}
			updated, _ := s.store.GetTurn(ctx, turn.ID)
			writeJSON(w, http.StatusOK, map[string]any{"stopped": true, "turn": controlTurnView(&updated), "target": targetView})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"stopped": false, "reason": "turn_finished", "turn": controlTurnView(&turn), "target": targetView})
		}
		return
	}
	if active, err := s.store.ActiveTurn(ctx, target.ID); err == nil {
		if err := s.chat.Cancel(ctx, target.ID); err != nil {
			if errors.Is(err, chat.ErrNoActiveTurn) {
				writeJSON(w, http.StatusOK, map[string]any{"stopped": false, "reason": "idle", "target": targetView})
				return
			}
			writeError(w, http.StatusBadGateway, "stop_failed", err.Error())
			return
		}
		updated, _ := s.store.GetTurn(ctx, active.ID)
		writeJSON(w, http.StatusOK, map[string]any{"stopped": true, "turn": controlTurnView(&updated), "target": targetView})
		return
	}
	if queued, err := s.store.NextQueuedTurn(ctx, target.ID); err == nil {
		if err := s.chat.Cancel(ctx, target.ID); err != nil {
			if errors.Is(err, chat.ErrNoActiveTurn) {
				writeJSON(w, http.StatusOK, map[string]any{"stopped": false, "reason": "idle", "target": targetView})
				return
			}
			writeError(w, http.StatusBadGateway, "stop_failed", err.Error())
			return
		}
		updated, _ := s.store.GetTurn(ctx, queued.ID)
		writeJSON(w, http.StatusOK, map[string]any{"stopped": true, "turn": controlTurnView(&updated), "target": targetView})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": false, "reason": "idle", "target": targetView})
}

func (s *Server) controlSpawn(w http.ResponseWriter, r *http.Request, callerID string, in conversationControlRequest) {
	p, err := s.conversationPolicy(r.Context(), callerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	if controlDenied(w, p) {
		return
	}
	if p.ReadOnly {
		writeError(w, http.StatusForbidden, "policy_denied", "This conversation is read-only.")
		return
	}
	policy := chat.OrchestrationFrom(p.Caller.Metadata)
	if !policy.Spawn {
		writeError(w, http.StatusForbidden, "spawn_not_allowed", "Spawning is not enabled for this conversation.")
		return
	}
	if s.chat == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Conversation service is not available.")
		return
	}
	if allowed, retry := s.spawns.allow(callerID, 3.0/60, 2); !allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": "Too many spawned conversations recently.", "code": "rate_limited",
			"retryAfterMs": retry.Milliseconds(),
		})
		return
	}
	children, err := s.store.CountSpawnedConversations(r.Context(), p.Caller.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "spawn_failed", err.Error())
		return
	}
	if children >= 10 {
		writeError(w, http.StatusConflict, "spawn_limit", "This conversation already has 10 active delegated chats; archive one first.")
		return
	}
	agentID, ok := s.resolveControlAgent(r.Context(), in.Agent)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_agent", "Unknown agent.")
		return
	}
	agent, err := s.store.GetAgent(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Agent not found.")
		return
	}
	if !agent.Enabled {
		writeError(w, http.StatusConflict, "target_agent_disabled", "The selected agent is disabled.")
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Delegated task"
	}
	if len(title) > 180 {
		title = title[:180]
	}
	prompt := strings.TrimSpace(in.Prompt)
	if len(prompt) > 32000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "prompt exceeds 32000 bytes.")
		return
	}
	projectRef := strings.TrimSpace(in.ProjectID)
	if projectRef == "" {
		projectRef = strings.TrimSpace(in.Project)
	}
	projectID := ""
	if projectRef != "" {
		project, found, ambiguous := s.resolveControlProject(r.Context(), projectRef)
		switch {
		case ambiguous:
			writeError(w, http.StatusBadRequest, "ambiguous_project", "Project name matches more than one project.")
			return
		case !found:
			writeError(w, http.StatusBadRequest, "invalid_project", "Unknown project.")
			return
		default:
			projectID = project.ID
		}
	}
	if projectID == "" && p.Scope == "project" {
		projectID = p.Caller.ProjectID
	}
	if projectID != "" && p.Scope == "project" && projectID != p.Caller.ProjectID {
		writeError(w, http.StatusForbidden, "policy_denied", "The project is outside the allowed scope.")
		return
	}
	orch := &chat.Orchestrator{ConversationID: p.Caller.ID, Title: p.Caller.Title, AgentID: p.Caller.AgentID}
	origin, _ := json.Marshal(orch)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var conv store.Conversation
	if projectID != "" {
		conv, err = s.chat.CreateProjectConversation(ctx, agentID, title, "agent", string(origin), projectID)
	} else {
		conv, err = s.chat.CreateConversation(ctx, agentID, title, "", "agent", string(origin))
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "spawn_failed", err.Error())
		return
	}
	warning := ""
	if _, err := s.chat.MarkSpawned(ctx, conv.ID, p.Caller.ID); err != nil {
		warning = "The child was created but its parent link could not be recorded."
	}
	response := map[string]any{
		"conversation": s.controlConversationView(r.Context(), conv, p, true, nil, nil),
		"promptQueued": false,
	}
	if prompt != "" {
		msg, turn, err := s.chat.Submit(ctx, chat.SubmitInput{
			ConversationID: conv.ID, Text: prompt, Channel: "agent", OriginRef: string(origin),
			Delivery: "queue", Orchestrator: orch,
		})
		if err != nil {
			response["error"] = "The child was created but the first prompt failed: " + err.Error()
		} else {
			response["promptQueued"] = true
			response["message"] = map[string]any{"id": msg.ID, "seq": msg.Seq}
			response["turn"] = map[string]any{"id": turn.ID, "status": turn.Status}
		}
	}
	if warning != "" {
		response["warning"] = warning
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) controlReport(w http.ResponseWriter, r *http.Request, callerID string, in conversationControlRequest) {
	p, err := s.conversationPolicy(r.Context(), callerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	if controlDenied(w, p) {
		return
	}
	policy := chat.OrchestrationFrom(p.Caller.Metadata)
	if !policy.ProgressPings {
		writeError(w, http.StatusForbidden, "pings_not_allowed", "Progress pings are not enabled for this conversation.")
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "text is required.")
		return
	}
	if len(text) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_request", "text exceeds 500 bytes; keep progress pings short.")
		return
	}
	if allowed, retry := s.reports.allow(callerID, 1.0/60, 1); !allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": "Progress pings are limited to one per minute.", "code": "rate_limited",
			"retryAfterMs": retry.Milliseconds(),
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.chat.NotifyActive(ctx, callerID, text); err != nil {
		switch {
		case errors.Is(err, chat.ErrNoChannel):
			writeError(w, http.StatusConflict, "no_channel", err.Error())
		case errors.Is(err, chat.ErrNoActiveTurn):
			writeError(w, http.StatusConflict, "no_active_turn", err.Error())
		default:
			writeError(w, http.StatusBadGateway, "report_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivered": true})
}

func (s *Server) handleControlActivity(w http.ResponseWriter, r *http.Request) {
	limit := parseInt(r.URL.Query().Get("limit"), 50)
	writeJSON(w, http.StatusOK, map[string]any{
		"activity": s.audit.list(r.URL.Query().Get("caller"), r.URL.Query().Get("target"), limit),
	})
}

func (s *Server) registerControlWatch(targetID, callerID string) {
	if targetID == "" || callerID == "" {
		return
	}
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.watchers == nil {
		s.watchers = map[string]map[string]time.Time{}
	}
	byCaller := s.watchers[targetID]
	if byCaller == nil {
		byCaller = map[string]time.Time{}
		s.watchers[targetID] = byCaller
	}
	byCaller[callerID] = time.Now()
}

func (s *Server) unregisterControlWatch(targetID, callerID string) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	byCaller := s.watchers[targetID]
	if byCaller == nil {
		return
	}
	delete(byCaller, callerID)
	if len(byCaller) == 0 {
		delete(s.watchers, targetID)
	}
}

func (s *Server) watchingCount(targetID string) int {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	return len(s.watchers[targetID])
}

func (s *Server) acquireControlWait(callerID string) bool {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	if s.waits == nil {
		s.waits = map[string]int{}
	}
	if s.waits[callerID] >= controlWaitPerCaller {
		return false
	}
	s.waits[callerID]++
	return true
}

func (s *Server) releaseControlWait(callerID string) {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	if s.waits[callerID] <= 1 {
		delete(s.waits, callerID)
		return
	}
	s.waits[callerID]--
}

func (s *Server) resolveControlAgent(ctx context.Context, name string) (string, bool) {
	if agent, err := s.store.GetAgent(ctx, name); err == nil {
		return agent.ID, true
	}
	if agent, err := s.store.GetAgentBySlug(ctx, name); err == nil {
		return agent.ID, true
	}
	return "", false
}

func (s *Server) controlConversationView(ctx context.Context, c store.Conversation, p controlPolicy, detailed bool, projectNames map[string]string, lastTurns map[string]store.TurnSummary) controlConversation {
	view := controlConversation{
		ID: c.ID, Title: c.Title, Agent: controlAgent{ID: c.AgentID, Name: c.AgentName},
		Status: c.Status, PendingApprovals: c.PendingCount, OriginChannel: c.OriginChannel,
		ProjectID: c.ProjectID, Pinned: c.Pinned, Archived: c.Archived,
		MessageCount: c.MessageCount, UpdatedAt: c.UpdatedAt, IsSelf: c.ID == p.Caller.ID,
		Watchers: s.watchingCount(c.ID),
	}
	if c.ProjectID != "" {
		if name, ok := projectNames[c.ProjectID]; ok {
			view.ProjectName = name
		} else if project, err := s.store.GetProject(ctx, c.ProjectID); err == nil {
			view.ProjectName = project.Name
		}
	}
	if lastTurns == nil {
		if summaries, err := s.store.ListLastTurnSummaries(ctx, []string{c.ID}); err == nil {
			lastTurns = summaries
		}
	}
	if summary, ok := lastTurns[c.ID]; ok {
		view.LastTurn = &controlTurnSummary{
			ID: summary.ID, Status: summary.Status, StopReason: summary.StopReason, Error: summary.Error,
			Prompt: textfmt.Truncate(summary.Prompt, 240), Answer: textfmt.Truncate(summary.Answer, 600), At: summary.CreatedAt,
		}
	}
	if turn, err := s.store.ActiveTurn(ctx, c.ID); err == nil {
		view.ActiveTurn = controlTurnView(&turn)
	}
	if c.LastMessage != "" || c.LastMessageRole != "" {
		view.LastMessage = &controlLastMessage{
			Role: c.LastMessageRole, Kind: "text",
			Preview: textfmt.Truncate(c.LastMessage, 240), At: c.LastMessageAt,
		}
	}
	if detailed {
		if queued, err := s.store.ListQueuedTurnsForConversation(ctx, c.ID); err == nil {
			view.QueuedTurns = len(queued)
		}
	}
	return view
}

func controlTurnView(t *store.Turn) *controlTurn {
	if t == nil || t.ID == "" {
		return nil
	}
	return &controlTurn{
		ID: t.ID, Status: t.Status, StopReason: t.StopReason, Error: t.Error,
		StartedAt: t.StartedAt, EndedAt: t.EndedAt,
	}
}

func controlTurnTerminal(status string) bool {
	switch status {
	case "completed", "error", "cancelled":
		return true
	default:
		return false
	}
}

func controlApprovalViews(list []store.Approval) []controlApproval {
	out := make([]controlApproval, 0, len(list))
	for _, ap := range list {
		if ap.Status != "pending" {
			continue
		}
		out = append(out, controlApproval{ID: ap.ID, Title: ap.Title, Kind: ap.Kind, Risk: ap.Risk, CreatedAt: ap.CreatedAt})
	}
	return out
}

func controlMessageViews(msgs []store.Message, includeThoughts bool) ([]controlMessage, bool, int64) {
	out := make([]controlMessage, 0, len(msgs))
	total := 0
	hasMore := false
	var lastSeq int64
	for _, m := range msgs {
		if m.Kind == "thought" && !includeThoughts {
			lastSeq = m.Seq
			continue
		}
		text := m.Text
		truncated := false
		if len(text) > controlMessageMax {
			text = textfmt.Truncate(text, controlMessageMax)
			truncated = true
		}
		if total+len(text) > controlResponseMax && len(out) > 0 {
			hasMore = true
			break
		}
		total += len(text) + 160
		out = append(out, controlMessage{
			Seq: m.Seq, ID: m.ID, Role: m.Role, Kind: m.Kind, Status: m.Status,
			Text: text, ToolCallID: m.ToolCallID, At: m.CreatedAt, Truncated: truncated,
		})
		lastSeq = m.Seq
	}
	return out, hasMore, lastSeq
}
