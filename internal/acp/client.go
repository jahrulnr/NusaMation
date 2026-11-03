package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type ClientConfig struct {
	ProcessConfig
	ClientInfo   Implementation
	Capabilities *ClientCapabilities
}

type Client struct {
	proc  *Process
	init  InitializeResult
	cfg   ClientConfig
	log   *slog.Logger
	start time.Time
}

func Start(ctx context.Context, cfg ClientConfig, handler Handler) (*Client, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.ClientInfo.Name == "" {
		cfg.ClientInfo = Implementation{Name: "nusamation", Title: "NusaMation", Version: "0.1.0"}
	}
	c := &Client{cfg: cfg, log: cfg.Log.With("acp", "client"), start: time.Now()}
	c.proc = NewProcess(cfg.ProcessConfig, handler)
	if err := c.proc.Start(ctx); err != nil {
		return nil, err
	}
	initCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	res, err := c.Initialize(initCtx)
	if err != nil {
		_ = c.proc.Close()
		return nil, err
	}
	c.init = res
	return c, nil
}

func (c *Client) Initialize(ctx context.Context) (InitializeResult, error) {
	caps := DefaultClientCapabilities()
	if c.cfg.Capabilities != nil {
		caps = *c.cfg.Capabilities
	}
	params := map[string]any{
		"protocolVersion":    ProtocolVersion,
		"clientCapabilities": caps,
		"clientInfo":         c.cfg.ClientInfo,
	}
	var res InitializeResult
	if err := c.proc.Call(ctx, "initialize", params, &res); err != nil {
		return res, err
	}
	if res.ProtocolVersion != ProtocolVersion {
		return res, fmt.Errorf("agent selected unsupported ACP protocol version %d (client supports %d)", res.ProtocolVersion, ProtocolVersion)
	}
	c.log.Info("ACP initialized",
		"agent", implementationName(res.AgentInfo),
		"loadSession", res.AgentCapabilities.LoadSession,
		"mcpHttp", res.AgentCapabilities.MCPCapabilities.HTTP,
		"mcpSse", res.AgentCapabilities.MCPCapabilities.SSE)
	return res, nil
}

func implementationName(i *Implementation) string {
	if i == nil {
		return "unknown"
	}
	if i.Title != "" {
		return i.Title
	}
	return i.Name
}

func (c *Client) NewSession(ctx context.Context, req NewSessionRequest) (SessionState, error) {
	if req.MCPServers == nil {
		req.MCPServers = []MCPServerConfig{}
	}
	var res SessionState
	err := c.proc.Call(ctx, "session/new", req, &res)
	return res, err
}

func (c *Client) LoadSession(ctx context.Context, req LoadSessionRequest) (SessionState, error) {
	if !c.init.AgentCapabilities.LoadSession {
		return SessionState{}, errors.New("agent does not support session/load")
	}
	if req.MCPServers == nil {
		req.MCPServers = []MCPServerConfig{}
	}
	var res SessionState
	err := c.proc.Call(ctx, "session/load", req, &res)
	return res, err
}

func (c *Client) ResumeSession(ctx context.Context, req ResumeSessionRequest) (SessionState, error) {
	if !c.init.SupportsResume() {
		return SessionState{}, errors.New("agent does not support session/resume")
	}
	if req.MCPServers == nil {
		req.MCPServers = []MCPServerConfig{}
	}
	var res SessionState
	err := c.proc.Call(ctx, "session/resume", req, &res)
	return res, err
}

func (c *Client) Prompt(ctx context.Context, sessionID string, prompt []ContentBlock) (PromptResult, error) {
	var res PromptResult
	req := PromptRequest{SessionID: sessionID, Prompt: prompt}
	err := c.proc.Call(ctx, "session/prompt", req, &res)
	return res, err
}

func (c *Client) Cancel(sessionID string) error {
	return c.proc.Notify("session/cancel", map[string]any{"sessionId": sessionID})
}

func (c *Client) SetConfig(ctx context.Context, sessionID, configID string, value any) (SessionState, error) {
	req := map[string]any{"sessionId": sessionID, "configId": configID, "value": value}
	if _, ok := value.(bool); ok {
		req["type"] = "boolean"
	}
	var res SetConfigResult
	err := c.proc.Call(ctx, "session/set_config_option", req, &res)
	return SessionState{ConfigOptions: res.ConfigOptions}, err
}

func (c *Client) SetMode(ctx context.Context, sessionID, modeID string) error {
	return c.proc.Call(ctx, "session/set_mode", SetModeRequest{SessionID: sessionID, ModeID: modeID}, nil)
}

func (c *Client) CloseSession(ctx context.Context, sessionID string) error {
	if !c.init.SupportsClose() {
		return nil
	}
	err := c.proc.Call(ctx, "session/close", map[string]any{"sessionId": sessionID}, nil)
	if IsUnknownSession(err) {
		return nil
	}
	return err
}

func (c *Client) ListSessions(ctx context.Context, cursor string) (ListSessionsResult, error) {
	params := map[string]any{}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var res ListSessionsResult
	err := c.proc.Call(ctx, "session/list", params, &res)
	return res, err
}

func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	return c.proc.Call(ctx, "session/delete", map[string]any{"sessionId": sessionID}, nil)
}

func (c *Client) Authenticate(ctx context.Context, methodID string) error {
	return c.proc.Call(ctx, "authenticate", map[string]any{"methodId": methodID}, nil)
}

func (c *Client) Logout(ctx context.Context) error {
	if !c.init.SupportsLogout() {
		return errors.New("agent does not support logout")
	}
	return c.proc.Call(ctx, "logout", map[string]any{}, nil)
}

func (c *Client) Capabilities() InitializeResult { return c.init }
func (c *Client) AgentInfo() *Implementation     { return c.init.AgentInfo }
func (c *Client) AuthMethods() []AuthMethod      { return c.init.AuthMethods }
func (c *Client) SupportsLoad() bool             { return c.init.SupportsLoad() }
func (c *Client) SupportsResume() bool           { return c.init.SupportsResume() }
func (c *Client) SupportsClose() bool            { return c.init.SupportsClose() }
func (c *Client) SupportsMCPHTTP() bool          { return c.init.AgentCapabilities.MCPCapabilities.HTTP }
func (c *Client) SupportsMCPSSE() bool           { return c.init.AgentCapabilities.MCPCapabilities.SSE }
func (c *Client) PromptCapabilities() PromptCapabilities {
	return c.init.AgentCapabilities.PromptCapabilities
}
func (c *Client) PID() int              { return c.proc.PID() }
func (c *Client) Stderr() []string      { return c.proc.StderrLog() }
func (c *Client) Alive() bool           { return !c.proc.isClosed() }
func (c *Client) Uptime() time.Duration { return time.Since(c.start) }
func (c *Client) Done() <-chan struct{} { return c.proc.Done() }
func (c *Client) Close() error          { return c.proc.Close() }
func (c *Client) Kill() error {
	return c.proc.Close()
}

func (c *Client) InitializeResult() InitializeResult { return c.init }

func MarshalConfigOptions(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "[]"
	}
	return string(raw)
}
