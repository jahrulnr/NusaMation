package store

import (
	"encoding/json"
	"time"
)

type EnvVar struct {
	Name     string `json:"name"`
	Value    string `json:"value,omitempty"`
	HasValue bool   `json:"hasValue,omitempty"`
}

type SecretList []EnvVar

type Agent struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Slug            string          `json:"slug"`
	Description     string          `json:"description"`
	Command         string          `json:"command"`
	Args            []string        `json:"args"`
	Env             SecretList      `json:"env,omitempty"`
	Cwd             string          `json:"cwd"`
	Icon            string          `json:"icon"`
	Color           string          `json:"color"`
	Enabled         bool            `json:"enabled"`
	AutoStart       bool            `json:"autoStart"`
	ApprovalPolicy  string          `json:"approvalPolicy"`
	DefaultModel    string          `json:"defaultModel"`
	Models          json.RawMessage `json:"models"`
	ConfigOptions   json.RawMessage `json:"configOptions"`
	Capabilities    json.RawMessage `json:"capabilities"`
	AuthMethods     json.RawMessage `json:"authMethods"`
	Status          string          `json:"status"`
	LastError       string          `json:"lastError"`
	LastProbeAt     int64           `json:"lastProbeAt"`
	CreatedAt       int64           `json:"createdAt"`
	UpdatedAt       int64           `json:"updatedAt"`
	ConversationCnt int             `json:"conversationCount,omitempty"`
}

// Public returns a copy safe to send to the browser: secrets are never included.
func (a Agent) Public() Agent {
	env := make(SecretList, 0, len(a.Env))
	for _, e := range a.Env {
		env = append(env, EnvVar{Name: e.Name, HasValue: e.Value != "" || e.HasValue})
	}
	a.Env = env
	if len(a.Models) == 0 {
		a.Models = json.RawMessage("[]")
	}
	if len(a.ConfigOptions) == 0 {
		a.ConfigOptions = json.RawMessage("[]")
	}
	if len(a.Capabilities) == 0 {
		a.Capabilities = json.RawMessage("{}")
	}
	if len(a.AuthMethods) == 0 {
		a.AuthMethods = json.RawMessage("[]")
	}
	return a
}

type MCPServer struct {
	ID         string     `json:"id"`
	AgentID    string     `json:"agentId"`
	Name       string     `json:"name"`
	Transport  string     `json:"transport"`
	Command    string     `json:"command,omitempty"`
	Args       []string   `json:"args,omitempty"`
	Env        SecretList `json:"env,omitempty"`
	URL        string     `json:"url,omitempty"`
	Headers    SecretList `json:"headers,omitempty"`
	Enabled    bool       `json:"enabled"`
	EnabledSet bool       `json:"-"`
	CreatedAt  int64      `json:"createdAt"`
	UpdatedAt  int64      `json:"updatedAt"`
}

func (m MCPServer) Public() MCPServer {
	pub := func(in SecretList) SecretList {
		out := make(SecretList, 0, len(in))
		for _, e := range in {
			out = append(out, EnvVar{Name: e.Name, HasValue: e.Value != "" || e.HasValue})
		}
		return out
	}
	m.Env = pub(m.Env)
	m.Headers = pub(m.Headers)
	return m
}

type Setting struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt int64  `json:"updatedAt"`
}

type ConfigSelection struct {
	ID    string `json:"id"`
	Value any    `json:"value"`
}

type AgentDefaults struct {
	AgentID    string            `json:"agentId"`
	Selections []ConfigSelection `json:"selections"`
}

type Routing struct {
	ReplyToOrigin   bool `json:"replyToOrigin"`
	MirrorFinal     bool `json:"mirrorFinal"`
	NotifyApprovals bool `json:"notifyApprovals"`
}

func DefaultRouting() Routing {
	return Routing{ReplyToOrigin: true, MirrorFinal: false, NotifyApprovals: true}
}

type Usage struct {
	Used         int64   `json:"used"`
	Size         int64   `json:"size"`
	CostAmount   float64 `json:"costAmount,omitempty"`
	CostCurrency string  `json:"costCurrency,omitempty"`
	TotalTokens  int64   `json:"totalTokens,omitempty"`
	InputTokens  int64   `json:"inputTokens,omitempty"`
	OutputTokens int64   `json:"outputTokens,omitempty"`
}

func (u Usage) Empty() bool {
	return u.Used == 0 && u.Size == 0 && u.TotalTokens == 0 && u.CostAmount == 0
}

type Conversation struct {
	ID              string          `json:"id"`
	Title           string          `json:"title"`
	AgentID         string          `json:"agentId"`
	AgentName       string          `json:"agentName,omitempty"`
	AgentIcon       string          `json:"agentIcon,omitempty"`
	AgentColor      string          `json:"agentColor,omitempty"`
	ACPSessionID    string          `json:"acpSessionId,omitempty"`
	Cwd             string          `json:"cwd"`
	Status          string          `json:"status"`
	OriginChannel   string          `json:"originChannel"`
	OriginRef       string          `json:"originRef,omitempty"`
	Model           string          `json:"model,omitempty"`
	ConfigOptions   json.RawMessage `json:"configOptions"`
	Routing         Routing         `json:"routing"`
	Pinned          bool            `json:"pinned"`
	Archived        bool            `json:"archived"`
	Plan            json.RawMessage `json:"plan"`
	Usage           Usage           `json:"usage"`
	Metadata        json.RawMessage `json:"metadata"`
	CreatedAt       int64           `json:"createdAt"`
	UpdatedAt       int64           `json:"updatedAt"`
	LastMessageAt   int64           `json:"lastMessageAt"`
	LastMessage     string          `json:"lastMessage,omitempty"`
	LastMessageRole string          `json:"lastMessageRole,omitempty"`
	MessageCount    int             `json:"messageCount"`
	PendingCount    int             `json:"pendingApprovals"`
	Links           []ChannelLink   `json:"links,omitempty"`
}

type ChannelLink struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	Channel        string `json:"channel"`
	ExternalID     string `json:"externalId"`
	ThreadID       string `json:"threadId,omitempty"`
	DisplayName    string `json:"displayName,omitempty"`
	IsGroup        bool   `json:"isGroup"`
	CreatedAt      int64  `json:"createdAt"`
}

type Message struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversationId"`
	TurnID         string          `json:"turnId,omitempty"`
	Seq            int64           `json:"seq"`
	Role           string          `json:"role"`
	Kind           string          `json:"kind"`
	Text           string          `json:"text"`
	Content        json.RawMessage `json:"content,omitempty"`
	Status         string          `json:"status"`
	Channel        string          `json:"channel,omitempty"`
	ExternalID     string          `json:"externalId,omitempty"`
	ACPMessageID   string          `json:"acpMessageId,omitempty"`
	ToolCallID     string          `json:"toolCallId,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	CreatedAt      int64           `json:"createdAt"`
	UpdatedAt      int64           `json:"updatedAt"`
}

type Turn struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	UserMessageID  string `json:"userMessageId,omitempty"`
	Status         string `json:"status"`
	StopReason     string `json:"stopReason,omitempty"`
	Error          string `json:"error,omitempty"`
	OriginChannel  string `json:"originChannel"`
	OriginRef      string `json:"originRef,omitempty"`
	QueuePosition  int    `json:"queuePosition,omitempty"`
	StartedAt      int64  `json:"startedAt,omitempty"`
	EndedAt        int64  `json:"endedAt,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
}

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type Approval struct {
	ID               string             `json:"id"`
	ConversationID   string             `json:"conversationId"`
	TurnID           string             `json:"turnId,omitempty"`
	ToolCallID       string             `json:"toolCallId,omitempty"`
	Title            string             `json:"title"`
	Kind             string             `json:"kind"`
	Details          json.RawMessage    `json:"details"`
	Options          []PermissionOption `json:"options"`
	Status           string             `json:"status"`
	SelectedOption   string             `json:"selectedOption,omitempty"`
	CreatedAt        int64              `json:"createdAt"`
	ResolvedAt       int64              `json:"resolvedAt,omitempty"`
	ExpiresAt        int64              `json:"expiresAt,omitempty"`
	ConversationName string             `json:"conversationTitle,omitempty"`
	AgentName        string             `json:"agentName,omitempty"`
	Risk             string             `json:"risk,omitempty"`
}

type ApprovalRule struct {
	ID             string `json:"id"`
	Scope          string `json:"scope"`
	AgentID        string `json:"agentId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	ToolName       string `json:"toolName,omitempty"`
	ToolKind       string `json:"toolKind,omitempty"`
	Action         string `json:"action"`
	CreatedAt      int64  `json:"createdAt"`
}

type Upload struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
	Name           string `json:"name"`
	MIME           string `json:"mime"`
	Size           int64  `json:"size"`
	Path           string `json:"-"`
	CreatedAt      int64  `json:"createdAt"`
}

type Session struct {
	IDHash     string
	CSRF       string
	CreatedAt  int64
	LastSeenAt int64
	ExpiresAt  int64
	IP         string
	UserAgent  string
}

type AttachmentInput struct {
	UploadID string `json:"uploadId"`
	Name     string `json:"name,omitempty"`
	MIME     string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

type AgentInput struct {
	Name            string     `json:"name"`
	Slug            string     `json:"slug"`
	Description     string     `json:"description"`
	Command         string     `json:"command"`
	Args            []string   `json:"args"`
	Env             SecretList `json:"env"`
	Cwd             string     `json:"cwd"`
	Icon            string     `json:"icon"`
	Color           string     `json:"color"`
	Enabled         *bool      `json:"enabled"`
	AutoStart       *bool      `json:"autoStart"`
	ApprovalPolicy  string     `json:"approvalPolicy"`
	DefaultModel    string     `json:"defaultModel"`
	DefaultModelSet bool       `json:"-"`
}

type ConversationFilter struct {
	Query    string
	AgentID  string
	Channel  string
	Archived bool
	Pinned   *bool
	Limit    int
	Offset   int
}

func js(v any, fallback string) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return fallback
	}
	return string(b)
}

func raw(s string, fallback string) json.RawMessage {
	if s == "" {
		s = fallback
	}
	return json.RawMessage(s)
}

func age(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
