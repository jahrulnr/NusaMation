package acp

import (
	"encoding/json"
	"fmt"
	"strconv"
)

const ProtocolVersion = 1

type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type PromptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

type MCPCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

type SessionCapabilities struct {
	List                  json.RawMessage `json:"list,omitempty"`
	Delete                json.RawMessage `json:"delete,omitempty"`
	Resume                json.RawMessage `json:"resume,omitempty"`
	Close                 json.RawMessage `json:"close,omitempty"`
	AdditionalDirectories json.RawMessage `json:"additionalDirectories,omitempty"`
}

type AuthCapabilities struct {
	Logout json.RawMessage `json:"logout,omitempty"`
}

type AgentCapabilities struct {
	LoadSession         bool                `json:"loadSession"`
	PromptCapabilities  PromptCapabilities  `json:"promptCapabilities"`
	MCPCapabilities     MCPCapabilities     `json:"mcpCapabilities"`
	SessionCapabilities SessionCapabilities `json:"sessionCapabilities"`
	Auth                AuthCapabilities    `json:"auth"`
	Meta                json.RawMessage     `json:"_meta,omitempty"`
}

type AuthMethod struct {
	Type        string            `json:"type,omitempty"`
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []AuthMethod      `json:"authMethods"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
	Meta              json.RawMessage   `json:"_meta,omitempty"`
}

func (r InitializeResult) SupportsLoad() bool { return r.AgentCapabilities.LoadSession }
func (r InitializeResult) SupportsResume() bool {
	return hasRaw(r.AgentCapabilities.SessionCapabilities.Resume)
}
func (r InitializeResult) SupportsClose() bool {
	v := r.AgentCapabilities.SessionCapabilities.Close
	return len(v) > 0 && string(v) != "null" && string(v) != "false"
}
func (r InitializeResult) SupportsDelete() bool {
	v := r.AgentCapabilities.SessionCapabilities.Delete
	return len(v) > 0 && string(v) != "null" && string(v) != "false"
}
func (r InitializeResult) SupportsLogout() bool {
	return hasRaw(r.AgentCapabilities.Auth.Logout)
}

// SupportsMidTurn reports Devin's cognition.ai/midTurnPrompts capability:
// a second session/prompt while a turn is running is injected into that turn.
func (r InitializeResult) SupportsMidTurn() bool {
	return metaBool(r.AgentCapabilities.Meta, "cognition.ai/midTurnPrompts")
}

// SupportsSteering reports the _session/steering extension, advertised either
// on agentCapabilities._meta or the initialize result's top-level _meta.
func (r InitializeResult) SupportsSteering() bool {
	return steeringSupported(r.AgentCapabilities.Meta) || steeringSupported(r.Meta)
}

func metaBool(raw json.RawMessage, key string) bool {
	if len(raw) == 0 {
		return false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	b, _ := m[key].(bool)
	return b
}

func steeringSupported(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	body, ok := m["steering"]
	if !ok {
		return false
	}
	var flag struct {
		Supported bool `json:"supported"`
	}
	if json.Unmarshal(body, &flag) == nil && flag.Supported {
		return true
	}
	var on bool
	return json.Unmarshal(body, &on) == nil && on
}

func hasRaw(v json.RawMessage) bool {
	if len(v) == 0 {
		return false
	}
	s := string(v)
	return s != "null" && s != "{}" && s != `""` && s != "false"
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type MCPServerConfig struct {
	Type    string   `json:"type,omitempty"`
	Name    string   `json:"name"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     []EnvVar `json:"env,omitempty"`
	URL     string   `json:"url,omitempty"`
	Headers []EnvVar `json:"headers,omitempty"`
}

type NewSessionRequest struct {
	Cwd                   string            `json:"cwd"`
	AdditionalDirectories []string          `json:"additionalDirectories,omitempty"`
	MCPServers            []MCPServerConfig `json:"mcpServers"`
}

type LoadSessionRequest struct {
	SessionID             string            `json:"sessionId"`
	Cwd                   string            `json:"cwd"`
	AdditionalDirectories []string          `json:"additionalDirectories,omitempty"`
	MCPServers            []MCPServerConfig `json:"mcpServers"`
}

type ResumeSessionRequest struct {
	SessionID             string            `json:"sessionId"`
	Cwd                   string            `json:"cwd"`
	AdditionalDirectories []string          `json:"additionalDirectories,omitempty"`
	MCPServers            []MCPServerConfig `json:"mcpServers"`
}

type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

type SessionState struct {
	SessionID     string            `json:"sessionId,omitempty"`
	Modes         *SessionModeState `json:"modes,omitempty"`
	ConfigOptions json.RawMessage   `json:"configOptions,omitempty"`
	Meta          json.RawMessage   `json:"_meta,omitempty"`
}

type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// SteerResult is the _session/steering response. Outcome is injected,
// startedNewTurn, promptRequired, or failed.
type SteerResult struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

func (r SteerResult) Accepted() bool {
	return r.Outcome == "injected" || r.Outcome == "startedNewTurn"
}

type PromptResult struct {
	StopReason string          `json:"stopReason"`
	Meta       json.RawMessage `json:"_meta,omitempty"`
	Usage      json.RawMessage `json:"usage,omitempty"`
}

type ContentBlock struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	MIMEType string            `json:"mimeType,omitempty"`
	Data     string            `json:"data,omitempty"`
	URI      string            `json:"uri,omitempty"`
	Name     string            `json:"name,omitempty"`
	Title    string            `json:"title,omitempty"`
	Size     int64             `json:"size,omitempty"`
	Resource *EmbeddedResource `json:"resource,omitempty"`
}

type EmbeddedResource struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type ContentChunk struct {
	MessageID string          `json:"messageId,omitempty"`
	Content   json.RawMessage `json:"content"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func DecodeTextContent(raw json.RawMessage) string {
	var t TextContent
	if err := json.Unmarshal(raw, &t); err == nil {
		return t.Text
	}
	return ""
}

type ToolCall struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title,omitempty"`
	Name       string          `json:"name,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Status     string          `json:"status,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Locations  json.RawMessage `json:"locations,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`
	Meta       json.RawMessage `json:"_meta,omitempty"`
}

type ToolCallUpdate struct {
	ToolCallID string          `json:"toolCallId"`
	Title      *string         `json:"title,omitempty"`
	Name       *string         `json:"name,omitempty"`
	Kind       *string         `json:"kind,omitempty"`
	Status     *string         `json:"status,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Locations  json.RawMessage `json:"locations,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`
	Meta       json.RawMessage `json:"_meta,omitempty"`
}

type PermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  json.RawMessage    `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
	Meta      json.RawMessage    `json:"_meta,omitempty"`
}

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type PermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}

type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type SetConfigRequest struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Type      string `json:"type,omitempty"`
	Value     any    `json:"value"`
}

type SetModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

type SetConfigResult struct {
	ConfigOptions json.RawMessage `json:"configOptions"`
}

type ListSessionsResult struct {
	Sessions   []json.RawMessage `json:"sessions"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

type SessionUpdate struct {
	SessionUpdate     string          `json:"sessionUpdate"`
	MessageID         string          `json:"messageId,omitempty"`
	Content           json.RawMessage `json:"content,omitempty"`
	ToolCallID        string          `json:"toolCallId,omitempty"`
	Title             string          `json:"title,omitempty"`
	Name              string          `json:"name,omitempty"`
	Kind              string          `json:"kind,omitempty"`
	Status            string          `json:"status,omitempty"`
	Locations         json.RawMessage `json:"locations,omitempty"`
	RawInput          json.RawMessage `json:"rawInput,omitempty"`
	RawOutput         json.RawMessage `json:"rawOutput,omitempty"`
	Entries           json.RawMessage `json:"entries,omitempty"`
	AvailableCommands json.RawMessage `json:"availableCommands,omitempty"`
	CurrentModeID     string          `json:"currentModeId,omitempty"`
	ConfigOptions     json.RawMessage `json:"configOptions,omitempty"`
	Used              int64           `json:"used,omitempty"`
	Size              int64           `json:"size,omitempty"`
	Cost              *Cost           `json:"cost,omitempty"`
	Severity          string          `json:"severity,omitempty"`
	Description       string          `json:"description,omitempty"`
	CompactionID      string          `json:"compactionId,omitempty"`
	Meta              json.RawMessage `json:"_meta,omitempty"`
	Raw               json.RawMessage `json:"-"`
}

type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Data) > 0 {
		return fmt.Sprintf("ACP error %d: %s (%s)", e.Code, e.Message, truncate(string(e.Data), 300))
	}
	return fmt.Sprintf("ACP error %d: %s", e.Code, e.Message)
}

func (e *RPCError) ErrorCode() int {
	if e == nil {
		return 0
	}
	return e.Code
}

const (
	CodeAuthRequired     = -32000
	CodeResourceNotFound = -32002
	CodeInvalidParams    = -32602
	CodeInternalError    = -32603
	CodeMethodNotFound   = -32601
)

func IsAuthRequired(err error) bool {
	re, ok := err.(*RPCError)
	return ok && re.Code == CodeAuthRequired
}

func IsUnknownSession(err error) bool {
	re, ok := err.(*RPCError)
	return ok && (re.Code == CodeResourceNotFound || re.Code == CodeInvalidParams)
}

func IsMethodNotFound(err error) bool {
	re, ok := err.(*RPCError)
	return ok && re.Code == CodeMethodNotFound
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type ClientCapabilities struct {
	FS          FileSystemCapabilities    `json:"fs"`
	Terminal    bool                      `json:"terminal"`
	Session     ClientSessionCapabilities `json:"session,omitempty"`
	Auth        AuthClientCapabilities    `json:"auth"`
	Elicitation any                       `json:"elicitation,omitempty"`
	Meta        json.RawMessage           `json:"_meta,omitempty"`
}

type FileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type ClientSessionCapabilities struct {
	ConfigOptions *struct {
		Boolean *struct{} `json:"boolean,omitempty"`
	} `json:"configOptions,omitempty"`
	Notices json.RawMessage `json:"notices,omitempty"`
}

type AuthClientCapabilities struct {
	Terminal bool `json:"terminal"`
}

func DefaultClientCapabilities() ClientCapabilities {
	caps := ClientCapabilities{
		FS:          FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
		Terminal:    true,
		Auth:        AuthClientCapabilities{Terminal: true},
		Elicitation: map[string]any{"form": map[string]any{}, "url": map[string]any{}},
		// Cursor only exposes model parameters (effort, fast, context, ...) to
		// clients opting into its parameterized picker. These use the same
		// config-option updates and setters as our existing dynamic picker.
		Meta: json.RawMessage(`{"parameterizedModelPicker":true}`),
	}
	caps.Session.ConfigOptions = &struct {
		Boolean *struct{} `json:"boolean,omitempty"`
	}{Boolean: &struct{}{}}
	return caps
}

type FSReadRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      *int   `json:"line,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

type FSReadResponse struct {
	Content string `json:"content"`
}

type FSWriteRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

type TerminalCreateRequest struct {
	SessionID       string   `json:"sessionId"`
	Command         string   `json:"command"`
	Args            []string `json:"args,omitempty"`
	Env             []EnvVar `json:"env,omitempty"`
	Cwd             string   `json:"cwd,omitempty"`
	OutputByteLimit *int64   `json:"outputByteLimit,omitempty"`
}

type TerminalCreateResponse struct {
	TerminalID string `json:"terminalId"`
}

type TerminalRef struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

type TerminalOutputResponse struct {
	Output     string              `json:"output"`
	Truncated  bool                `json:"truncated"`
	ExitStatus *TerminalExitStatus `json:"exitStatus,omitempty"`
}

type TerminalExitStatus struct {
	ExitCode *int    `json:"exitCode,omitempty"`
	Signal   *string `json:"signal,omitempty"`
}

type ElicitationCreateRequest struct {
	SessionID       string          `json:"sessionId"`
	ToolCallID      string          `json:"toolCallId,omitempty"`
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requestedSchema"`
}

type ElicitationResponse struct {
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content,omitempty"`
}

func IntPtr(n int) *int { return &n }

func MustRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func ParsePermissionToolCall(raw json.RawMessage) ToolCall {
	var tc ToolCall
	_ = json.Unmarshal(raw, &tc)
	if tc.ToolCallID == "" {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if v, ok := m["toolCallId"].(string); ok {
			tc.ToolCallID = v
		}
	}
	return tc
}

func AnyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}
