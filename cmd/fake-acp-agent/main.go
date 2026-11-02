// Command fake-acp-agent is a deterministic ACP agent used by tests and for
// exercising the NusaMation web UI without a network-backed agent. It streams a
// thought, a plan, a tool call and a permission request before answering.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}

type server struct {
	mu      sync.Mutex
	out     *bufio.Writer
	nextID  int
	pending map[string]chan json.RawMessage
	cancel  chan struct{}
}

func main() {
	s := &server{
		out:     bufio.NewWriter(os.Stdout),
		pending: map[string]chan json.RawMessage{},
		cancel:  make(chan struct{}),
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Method == "" && len(msg.ID) > 0 {
			key := string(msg.ID)
			s.mu.Lock()
			ch := s.pending[key]
			delete(s.pending, key)
			s.mu.Unlock()
			if ch != nil {
				select {
				case ch <- msg.Result:
				default:
				}
			}
			continue
		}
		if msg.Method != "" {
			go s.handle(msg)
		}
	}
}

func (s *server) handle(msg message) {
	switch msg.Method {
	case "initialize":
		s.respond(msg.ID, map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"loadSession": true,
				"promptCapabilities": map[string]any{
					"image": true, "audio": false, "embeddedContext": true,
				},
				"mcpCapabilities": map[string]any{"http": true, "sse": true},
				"sessionCapabilities": map[string]any{
					"list":   map[string]any{},
					"delete": map[string]any{},
					"close":  map[string]any{},
				},
				"auth": map[string]any{},
			},
			"authMethods": []any{},
			"agentInfo":   map[string]any{"name": "fake-acp", "title": "Fake ACP Agent", "version": "0.1.0"},
		})
	case "session/new":
		s.mu.Lock()
		s.cancel = make(chan struct{})
		s.mu.Unlock()
		s.notify("session/update", map[string]any{
			"sessionId": "fake-session",
			"update":    map[string]any{"sessionUpdate": "config_option_update", "configOptions": configOptions()},
		})
		s.notify("session/update", map[string]any{
			"sessionId": "fake-session",
			"update": map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{
				map[string]any{"name": "compact", "description": "Compact the fake context"},
			}},
		})
		s.respond(msg.ID, map[string]any{"sessionId": "fake-session", "configOptions": configOptions()})
	case "session/load", "session/resume":
		s.respond(msg.ID, map[string]any{"sessionId": "fake-session", "configOptions": configOptions()})
	case "session/set_config_option":
		s.respond(msg.ID, map[string]any{"configOptions": configOptions()})
	case "session/set_mode":
		s.respond(msg.ID, map[string]any{})
	case "session/close":
		s.respond(msg.ID, map[string]any{})
	case "session/list":
		s.respond(msg.ID, map[string]any{"sessions": []any{map[string]any{"sessionId": "fake-session", "title": "Fake session"}}})
	case "session/delete":
		s.respond(msg.ID, map[string]any{})
	case "session/cancel":
		s.mu.Lock()
		select {
		case <-s.cancel:
		default:
			close(s.cancel)
		}
		s.mu.Unlock()
	case "session/prompt":
		var p struct {
			SessionID string `json:"sessionId"`
			Prompt    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"prompt"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		go s.prompt(msg.ID, p.SessionID, p.Prompt)
	}
}

func configOptions() []any {
	return []any{
		map[string]any{
			"id": "mode", "name": "Session Mode", "category": "mode", "type": "select",
			"currentValue": "ask", "options": []any{
				map[string]any{"value": "ask", "name": "Ask"},
				map[string]any{"value": "accept-edits", "name": "Code"},
			},
		},
		map[string]any{
			"id": "model", "name": "Model", "category": "model", "type": "select",
			"currentValue": "fake-fast", "options": []any{
				map[string]any{"value": "fake-fast", "name": "Fake Fast"},
				map[string]any{"value": "fake-smart", "name": "Fake Smart"},
			},
		},
	}
}

func (s *server) prompt(id json.RawMessage, sessionID string, prompt []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	text := ""
	for _, p := range prompt {
		if p.Text != "" {
			text += p.Text
		}
	}
	s.update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{
		"type": "text", "text": "The fake agent received the prompt and is checking a safe path before acting.",
	}})
	s.update(map[string]any{"sessionUpdate": "plan", "entries": []any{
		map[string]any{"title": "Understand the request", "status": "completed"},
		map[string]any{"title": "Inspect the workspace", "status": "in_progress"},
		map[string]any{"title": "Reply with a summary", "status": "pending"},
	}})
	s.update(map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "fake-tool-1", "title": "Read workspace files",
		"name": "read", "kind": "read", "status": "pending",
		"rawInput":  map[string]any{"path": "."},
		"locations": []any{map[string]any{"path": "workspace/README.md"}},
	})
	select {
	case <-cancel:
		s.respond(id, map[string]any{"stopReason": "cancelled"})
		return
	case <-time.After(120 * time.Millisecond):
	}
	option := s.askPermission(sessionID)
	if option == "" {
		s.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "fake-tool-1", "status": "failed", "rawOutput": map[string]any{"error": "permission denied"}})
		s.chunk("I did not get permission to inspect the workspace, so I stopped there.")
		s.respond(id, map[string]any{"stopReason": "end_turn"})
		return
	}
	s.update(map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "fake-tool-1", "status": "completed",
		"rawOutput": map[string]any{"files": []string{"README.md", "AGENTS.md"}, "note": "read-only inspection"},
		"content":   []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "README.md\nAGENTS.md"}}},
	})
	s.update(map[string]any{"sessionUpdate": "plan", "entries": []any{
		map[string]any{"title": "Understand the request", "status": "completed"},
		map[string]any{"title": "Inspect the workspace", "status": "completed"},
		map[string]any{"title": "Reply with a summary", "status": "in_progress"},
	}})
	answer := "I inspected the workspace safely. The fake agent saw README.md and AGENTS.md. " +
		"If you asked for a demo, this is the demo: NusaMation streams thoughts, tools and approvals to web, while chat channels only receive this final digest."
	if strings.Contains(strings.ToLower(text), "poem") {
		answer = "A quiet tide writes silver on the shore,\nAnd NusaMation answers, asking nothing more."
	}
	for _, word := range strings.Fields(answer) {
		select {
		case <-cancel:
			s.respond(id, map[string]any{"stopReason": "cancelled"})
			return
		case <-time.After(35 * time.Millisecond):
		}
		s.chunk(word + " ")
	}
	s.update(map[string]any{"sessionUpdate": "usage_update", "used": 1280, "size": 200000})
	s.update(map[string]any{"sessionUpdate": "plan", "entries": []any{
		map[string]any{"title": "Understand the request", "status": "completed"},
		map[string]any{"title": "Inspect the workspace", "status": "completed"},
		map[string]any{"title": "Reply with a summary", "status": "completed"},
	}})
	s.respond(id, map[string]any{"stopReason": "end_turn", "usage": map[string]any{
		"totalTokens": 1280, "inputTokens": 1180, "outputTokens": 100,
	}})
}

func (s *server) askPermission(sessionID string) string {
	s.mu.Lock()
	s.nextID++
	reqID := s.nextID
	ch := make(chan json.RawMessage, 1)
	s.pending[fmt.Sprintf("%d", reqID)] = ch
	s.mu.Unlock()
	s.write(map[string]any{
		"jsonrpc": "2.0", "id": reqID, "method": "session/request_permission",
		"params": map[string]any{
			"sessionId": sessionID,
			"toolCall": map[string]any{
				"toolCallId": "fake-tool-1", "title": "Read workspace files",
				"name": "read", "kind": "read", "status": "pending",
				"rawInput": map[string]any{"path": "workspace"},
			},
			"options": []any{
				map[string]any{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"},
				map[string]any{"optionId": "allow-always", "name": "Always allow reads", "kind": "allow_always"},
				map[string]any{"optionId": "reject-once", "name": "Reject", "kind": "reject_once"},
			},
		},
	})
	select {
	case raw := <-ch:
		var res struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}
		if json.Unmarshal(raw, &res) != nil {
			return ""
		}
		if res.Outcome.Outcome != "selected" || strings.HasPrefix(res.Outcome.OptionID, "reject") {
			return ""
		}
		return res.Outcome.OptionID
	case <-time.After(10 * time.Minute):
		return ""
	}
}

func (s *server) update(update map[string]any) {
	s.notify("session/update", map[string]any{"sessionId": "fake-session", "update": update})
}

func (s *server) chunk(text string) {
	s.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
}

func (s *server) notify(method string, params any) {
	s.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *server) respond(id json.RawMessage, result any) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func (s *server) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.out.Write(b)
	_ = s.out.WriteByte('\n')
	_ = s.out.Flush()
}
