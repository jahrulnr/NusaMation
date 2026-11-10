// Package loopback is the HTTP client shared by NusaMation's embedded MCP
// servers. The MCP child runs inside the agent process; every tool call posts
// back to the desktop process over the loopback listener with the token issued
// for that conversation.
package loopback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client posts JSON to one loopback origin with a fixed bearer token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New returns a client with the same 90s budget the browser_use MCP server
// historically used. A caller may replace HTTP for a shorter budget.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 90 * time.Second},
	}
}

// Error is a non-2xx loopback response. Message is already human readable and
// safe to return to the agent.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("loopback request failed with status %d", e.Status)
}

// Post sends body to path and decodes the JSON response into out. A non-2xx
// response becomes *Error, preferring the server's {error, code} envelope.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	if c == nil || c.BaseURL == "" || c.Token == "" {
		return fmt.Errorf("loopback client is not configured")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var envelope struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(payload, &envelope) == nil && (envelope.Error != "" || envelope.Code != "") {
			return &Error{Status: res.StatusCode, Code: envelope.Code, Message: envelope.Error}
		}
		msg := strings.TrimSpace(string(payload))
		if msg == "" {
			msg = res.Status
		}
		return &Error{Status: res.StatusCode, Message: msg}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("loopback response: %w", err)
	}
	return nil
}
