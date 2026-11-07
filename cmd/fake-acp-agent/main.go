// Command fake-acp-agent is a deterministic ACP agent used by tests and for
// exercising the NusaMation web UI without a network-backed agent. It streams a
// thought, a plan, a tool call and a permission request before answering.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
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
	values  map[string]any
	legacy  bool
	busy    bool
}

func main() {
	s := &server{
		out:     bufio.NewWriter(os.Stdout),
		pending: map[string]chan json.RawMessage{},
		cancel:  make(chan struct{}),
		values:  map[string]any{"mode": "ask", "model": "fake-fast"},
		legacy:  os.Getenv("FAKE_ACP_MODES") == "legacy",
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
		// Prompt and cancel state changes happen here, in stdin order, so a
		// cancel always targets the prompt written before it.
		switch msg.Method {
		case "session/prompt":
			if os.Getenv("FAKE_ACP_MIDTURN") == "1" && s.isBusy() {
				go s.steerPrompt(msg)
				break
			}
			s.setBusy(true)
			cancel := s.newPromptCancel()
			go func() {
				defer s.setBusy(false)
				s.handlePrompt(msg, cancel)
			}()
		case "session/cancel":
			s.closeCancel()
		default:
			go s.handle(msg)
		}
	}
}

func (s *server) newPromptCancel() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = make(chan struct{})
	return s.cancel
}

func (s *server) isBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

func (s *server) setBusy(v bool) {
	s.mu.Lock()
	s.busy = v
	s.mu.Unlock()
}

func (s *server) steerPrompt(msg message) {
	var p struct {
		Prompt []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	text := strings.TrimSpace(promptText(p.Prompt))
	trace("steer " + text)
	s.update(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"messageId":     "steer",
		"content":       map[string]any{"type": "text", "text": "saw-steer: " + text},
	})
	s.respond(msg.ID, map[string]any{"stopReason": "end_turn"})
}

func (s *server) closeCancel() {
	trace("cancel")
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.cancel:
	default:
		close(s.cancel)
	}
}

func (s *server) handle(msg message) {
	switch msg.Method {
	case "initialize":
		caps := map[string]any{
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
		}
		if os.Getenv("FAKE_ACP_MIDTURN") == "1" {
			caps["_meta"] = map[string]any{"cognition.ai/midTurnPrompts": true}
		}
		s.respond(msg.ID, map[string]any{
			"protocolVersion":   1,
			"agentCapabilities": caps,
			"authMethods":       []any{},
			"agentInfo":         map[string]any{"name": "fake-acp", "title": "Fake ACP Agent", "version": "0.1.0"},
		})
	case "session/new":
		trace("session_new")
		var request struct {
			MCPServers []struct {
				Name string `json:"name"`
			} `json:"mcpServers"`
		}
		if json.Unmarshal(msg.Params, &request) == nil {
			for _, server := range request.MCPServers {
				trace("session_mcp " + server.Name)
			}
		}
		s.mu.Lock()
		s.cancel = make(chan struct{})
		s.values["mode"] = "ask"
		s.mu.Unlock()
		s.notify("session/update", map[string]any{
			"sessionId": "fake-session",
			"update":    map[string]any{"sessionUpdate": "config_option_update", "configOptions": s.configOptions()},
		})
		s.notify("session/update", map[string]any{
			"sessionId": "fake-session",
			"update": map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{
				map[string]any{"name": "compact", "description": "Compact the fake context"},
			}},
		})
		s.respond(msg.ID, s.sessionResult())
	case "session/load", "session/resume":
		s.respond(msg.ID, s.sessionResult())
	case "session/set_config_option":
		var req struct {
			ConfigID string `json:"configId"`
			Value    any    `json:"value"`
		}
		_ = json.Unmarshal(msg.Params, &req)
		trace(fmt.Sprintf("config %s %v", req.ConfigID, req.Value))
		s.mu.Lock()
		s.values[req.ConfigID] = req.Value
		if req.ConfigID == "model" && os.Getenv("FAKE_ACP_MODEL_RESETS_EFFORT") == "1" {
			// Emulate agents whose model value implies an effort level (like
			// Devin's swe-2-high): picking a model resets effort to its default.
			s.values["effort"] = "low"
		}
		s.mu.Unlock()
		s.respond(msg.ID, map[string]any{"configOptions": s.configOptions()})
	case "session/set_mode":
		var req struct {
			ModeID string `json:"modeId"`
		}
		_ = json.Unmarshal(msg.Params, &req)
		s.mu.Lock()
		s.values["mode"] = req.ModeID
		s.mu.Unlock()
		s.respond(msg.ID, map[string]any{})
		s.update(map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": req.ModeID})
	case "session/close":
		trace("session_close")
		s.respond(msg.ID, map[string]any{})
	case "session/list":
		s.respond(msg.ID, map[string]any{"sessions": []any{map[string]any{"sessionId": "fake-session", "title": "Fake session"}}})
	case "session/delete":
		trace("session_delete")
		s.respond(msg.ID, map[string]any{})

	}
}

func (s *server) configOptions() []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	options := []any{
		map[string]any{
			"id": "model", "name": "Model", "category": "model", "type": "select",
			"currentValue": s.values["model"], "options": []any{
				map[string]any{"value": "fake-fast", "name": "Fake Fast"},
				map[string]any{"value": "fake-smart", "name": "Fake Smart"},
			},
		},
	}
	if os.Getenv("FAKE_ACP_DYNAMIC_CONFIG") == "1" || os.Getenv("FAKE_ACP_MODEL_RESETS_EFFORT") == "1" {
		effort := []any{
			map[string]any{"value": "low", "name": "Low"},
			map[string]any{"value": "high", "name": "High"},
		}
		if os.Getenv("FAKE_ACP_DYNAMIC_CONFIG") == "1" {
			effort = effort[:1]
			if s.values["model"] == "fake-smart" {
				effort = append(effort, map[string]any{"value": "high", "name": "High"})
			}
		}
		current := s.values["effort"]
		if current == nil {
			current = "low"
		}
		options = append(options, map[string]any{"id": "effort", "name": "Effort", "category": "thought_level", "type": "select", "currentValue": current, "options": effort})
	}
	if s.legacy {
		return options
	}
	mode := map[string]any{
		"id": "mode", "name": "Session Mode", "category": "mode", "type": "select",
		"currentValue": s.values["mode"], "options": []any{
			map[string]any{"value": "ask", "name": "Ask"},
			map[string]any{"value": "accept-edits", "name": "Code"},
			map[string]any{"value": "bypass", "name": "Bypass permissions"},
		},
	}
	return append([]any{mode}, options...)
}

func (s *server) handlePrompt(msg message, cancel chan struct{}) {
	var p struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	trace(fmt.Sprintf("prompt_start %s %s", msg.ID, promptText(p.Prompt)))
	s.prompt(msg.ID, p.SessionID, p.Prompt, cancel)
}

func (s *server) prompt(id json.RawMessage, sessionID string, prompt []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}, cancel chan struct{}) {
	var text strings.Builder
	for _, p := range prompt {
		if p.Text != "" {
			text.WriteString(p.Text)
		}
	}
	if strings.Contains(text.String(), "timeline-terminal-test") || strings.Contains(text.String(), "timeline-background-test") {
		s.terminalDemo(id, sessionID, strings.Contains(text.String(), "timeline-background-test"))
		return
	}
	if strings.Contains(text.String(), "mode-switch-test") {
		s.modeSwitchDemo(id, sessionID)
		return
	}
	toolDelay := 400 * time.Millisecond
	if strings.Contains(text.String(), "slow") {
		toolDelay = 4 * time.Second
	}
	if strings.Contains(text.String(), "after-tools-two-tools") {
		s.toolsDemo(id, cancel, false, toolDelay)
		return
	}
	if strings.Contains(text.String(), "after-tools-ignore-cancel") {
		s.toolsDemo(id, cancel, true, toolDelay)
		return
	}
	if strings.Contains(text.String(), "after-tools-no-tools") {
		s.noToolsDemo(id, cancel)
		return
	}
	if strings.Contains(text.String(), "after-tools-permission") {
		s.permissionDemo(id, sessionID, cancel)
		return
	}
	if strings.Contains(text.String(), "send-media-test") {
		s.mediaDemo(id, text.String())
		return
	}
	if strings.Contains(text.String(), "steer-hold") {
		s.chunk("working ")
		select {
		case <-cancel:
			s.respond(id, map[string]any{"stopReason": "cancelled"})
		case <-time.After(4 * time.Second):
			s.chunk("finished ")
			s.respond(id, map[string]any{"stopReason": "end_turn"})
		}
		return
	}
	if strings.Contains(text.String(), "segment-reasons-test") {
		s.reasonsDemo(id)
		return
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
	option := s.askPermission(sessionID, fakeTool{id: "fake-tool-1", title: "Read workspace files", name: "read", kind: "read"})
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
	if strings.Contains(strings.ToLower(text.String()), "poem") {
		answer = "A quiet tide writes silver on the shore,\nAnd NusaMation answers, asking nothing more."
	}
	for word := range strings.FieldsSeq(answer) {
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

// mediaDemo emits a base64 image content block and, if the prompt carried a
// file:// URI, echoes it back inside a markdown link so the bridge can test
// both outbound media paths.
func (s *server) mediaDemo(id json.RawMessage, text string) {
	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	s.chunk("Here is the media you asked for. ")
	s.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{
		"type": "image", "mimeType": "image/png", "data": pngB64, "name": "pixel.png",
	}})
	if i := strings.Index(text, "file://"); i >= 0 {
		ref := strings.TrimRight(strings.Fields(text[i:])[0], ".,;")
		s.chunk(fmt.Sprintf("And a file: [result](%s)", ref))
	}
	s.respond(id, map[string]any{"stopReason": "end_turn"})
}

// toolsDemo announces tools and completes them one at a time. With
// ignoreCancel it never honours session/cancel and answers after a delay.
func (s *server) toolsDemo(id json.RawMessage, cancel chan struct{}, ignoreCancel bool, delay time.Duration) {
	tools := []string{"tool-a", "tool-b"}
	if ignoreCancel {
		tools = tools[:1]
	}
	for _, tid := range tools {
		s.update(map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": tid, "title": "Step " + tid,
			"name": tid, "kind": "read", "status": "pending", "rawInput": map[string]any{},
		})
	}
	for _, tid := range tools {
		time.Sleep(delay)
		s.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": tid, "status": "completed"})
		trace("tool_done " + tid)
	}
	if ignoreCancel {
		time.Sleep(800 * time.Millisecond)
		s.respond(id, map[string]any{"stopReason": "end_turn"})
		return
	}
	for range 300 {
		select {
		case <-cancel:
			s.respond(id, map[string]any{"stopReason": "cancelled"})
			return
		case <-time.After(35 * time.Millisecond):
		}
		s.chunk("lorem ")
	}
	s.respond(id, map[string]any{"stopReason": "end_turn"})
}

// noToolsDemo streams text until cancelled, then answers only after a delay
// and sends one more chunk, which must be attributed to this turn.
func (s *server) noToolsDemo(id json.RawMessage, cancel chan struct{}) {
	s.chunk("early ")
	select {
	case <-cancel:
	case <-time.After(10 * time.Second):
	}
	time.Sleep(400 * time.Millisecond)
	s.chunk("late-chunk ")
	s.respond(id, map[string]any{"stopReason": "cancelled"})
}

// permissionDemo waits on a permission request for one tool and answers with
// the stop reason implied by whether the turn was cancelled.
func (s *server) permissionDemo(id json.RawMessage, sessionID string, cancel chan struct{}) {
	s.update(map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "tool-a", "title": "Step tool-a",
		"name": "tool-a", "kind": "edit", "status": "pending", "rawInput": map[string]any{},
	})
	s.askPermission(sessionID, fakeTool{id: "tool-a", title: "Step tool-a", name: "tool-a", kind: "edit"})
	select {
	case <-cancel:
		s.respond(id, map[string]any{"stopReason": "cancelled"})
	default:
		s.respond(id, map[string]any{"stopReason": "end_turn"})
	}
}

// trace appends one line to $FAKE_ACP_TRACE so tests can assert RPC order.
func trace(line string) {
	path := os.Getenv("FAKE_ACP_TRACE")
	if path == "" {
		return
	}
	traceMu.Lock()
	defer traceMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

var traceMu sync.Mutex

func promptText(prompt []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	var b strings.Builder
	for _, p := range prompt {
		b.WriteString(p.Text)
	}
	return strings.ReplaceAll(b.String(), "\n", " ")
}

// terminalDemo exercises the ACP terminal lifecycle for integration tests.
func (s *server) terminalDemo(id json.RawMessage, sessionID string, background bool) {
	s.update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "Checking the command."}})
	command, args := "sh", []string{"-c", "printf first; sleep 0.5; printf second; exit 3"}
	if runtime.GOOS == "windows" {
		command, args = "powershell", []string{"-NoProfile", "-Command", "[Console]::Write('first'); Start-Sleep -Milliseconds 500; [Console]::Write('second'); exit 3"}
	}
	result := s.callClient("terminal/create", map[string]any{"sessionId": sessionID, "command": command, "args": args, "outputByteLimit": 4})
	var terminal struct {
		TerminalID string `json:"terminalId"`
	}
	if json.Unmarshal(result, &terminal) != nil || terminal.TerminalID == "" {
		s.respond(id, map[string]any{"stopReason": "end_turn"})
		return
	}
	s.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "terminal-demo", "title": "Run demo command", "kind": "execute", "status": "in_progress", "content": []any{map[string]any{"type": "terminal", "terminalId": terminal.TerminalID}}})
	if background {
		s.chunk("Command continues in the background.")
		s.respond(id, map[string]any{"stopReason": "end_turn"})
		go func() {
			s.callClient("terminal/wait_for_exit", map[string]any{"sessionId": sessionID, "terminalId": terminal.TerminalID})
			s.callClient("terminal/release", map[string]any{"sessionId": sessionID, "terminalId": terminal.TerminalID})
		}()
		return
	}
	s.callClient("terminal/wait_for_exit", map[string]any{"sessionId": sessionID, "terminalId": terminal.TerminalID})
	s.callClient("terminal/release", map[string]any{"sessionId": sessionID, "terminalId": terminal.TerminalID})
	s.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "terminal-demo", "status": "failed"})
	s.chunk("Command finished.")
	s.respond(id, map[string]any{"stopReason": "end_turn"})
}

type fakeTool struct {
	id, title, name, kind string
}

func (s *server) sessionResult() map[string]any {
	result := map[string]any{"sessionId": "fake-session", "configOptions": s.configOptions()}
	if s.legacy {
		s.mu.Lock()
		current, _ := s.values["mode"].(string)
		s.mu.Unlock()
		result["modes"] = map[string]any{"currentModeId": current, "availableModes": []any{
			map[string]any{"id": "ask", "name": "Ask", "description": "Request permission before making any changes"},
			map[string]any{"id": "accept-edits", "name": "Code", "description": "Write and modify code with full tool access"},
			map[string]any{"id": "bypass", "name": "Bypass permissions", "description": "Runs tools without asking"},
		}}
	}
	return result
}

func (s *server) currentMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	mode, _ := s.values["mode"].(string)
	return mode
}

func (s *server) modeSwitchDemo(id json.RawMessage, sessionID string) {
	s.update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "Two tool calls ahead; checking permission for each."}})
	for _, tool := range []fakeTool{
		{id: "fake-tool-1", title: "Read workspace files", name: "read", kind: "read"},
		{id: "fake-tool-2", title: "Write notes", name: "write_notes", kind: "edit"},
	} {
		s.update(map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": tool.id, "title": tool.title,
			"name": tool.name, "kind": tool.kind, "status": "pending",
			"rawInput": map[string]any{"path": "workspace"},
		})
		if s.currentMode() != "bypass" && s.askPermission(sessionID, tool) == "" {
			s.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": tool.id, "status": "failed", "rawOutput": map[string]any{"error": "permission denied"}})
			s.chunk("Permission was denied, so I stopped.")
			s.respond(id, map[string]any{"stopReason": "end_turn"})
			return
		}
		s.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": tool.id, "status": "completed", "rawOutput": map[string]any{"mode": s.currentMode()}})
	}
	s.chunk("Both tools finished.")
	s.respond(id, map[string]any{"stopReason": "end_turn"})
}

func (s *server) callClient(method string, params any) json.RawMessage {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan json.RawMessage, 1)
	s.pending[fmt.Sprintf("%d", id)] = ch
	s.mu.Unlock()
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case result := <-ch:
		return result
	case <-time.After(10 * time.Second):
		return nil
	}
}

func (s *server) askPermission(sessionID string, tool fakeTool) string {
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
				"toolCallId": tool.id, "title": tool.title,
				"name": tool.name, "kind": tool.kind, "status": "pending",
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

func (s *server) reasonsDemo(id json.RawMessage) {
	reasons := []string{"reason one", "reason two", "reason three"}
	for i, reason := range reasons {
		s.update(map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"messageId":     fmt.Sprintf("reason-%d", i+1),
			"content":       map[string]any{"type": "text", "text": reason},
		})
		if i < len(reasons)-1 {
			s.update(map[string]any{
				"sessionUpdate": "tool_call",
				"toolCallId":    fmt.Sprintf("seg-tool-%d", i+1),
				"title":         "Step",
				"name":          "read",
				"kind":          "read",
				"status":        "completed",
			})
			if i == 0 {
				time.Sleep(time.Second)
			}
		}
	}
	s.respond(id, map[string]any{"stopReason": "end_turn"})
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
	trace("respond " + string(id))
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
