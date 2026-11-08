package browseruse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const protocolVersion = "2024-11-05"

// EnvURL and EnvToken are set on the MCP child by the desktop process.
const (
	EnvURL   = "NUSAMATION_BROWSER_USE_URL"
	EnvToken = "NUSAMATION_BROWSER_USE_TOKEN"
)

// Serve speaks MCP stdio until in closes. Requests may arrive newline-
// delimited (the MCP spec's stdio transport) or Content-Length framed (LSP
// style); each response goes out in the same framing as its request — spec
// clients cannot parse headers and header clients ignore bare lines.
// baseURL is the NusaMation loopback origin, without a trailing slash.
func Serve(in io.Reader, out io.Writer, baseURL, token string) error {
	if baseURL == "" || token == "" {
		return fmt.Errorf("browser_use requires %s and %s", EnvURL, EnvToken)
	}
	client := &http.Client{Timeout: 90 * time.Second}
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	for {
		body, framed, err := readMessage(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue
		}
		resp := handle(client, baseURL, token, req)
		if err := writeMessage(w, resp, framed); err != nil {
			return err
		}
	}
}

type rpcRequest struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func handle(client *http.Client, baseURL, token string, req rpcRequest) rpcResponse {
	id := json.RawMessage(`null`)
	if req.ID != nil {
		id = *req.ID
	}
	switch req.Method {
	case "initialize":
		return rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "browser_use", "version": "1"},
		}}
	case "ping":
		return rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{}}
	case "tools/list":
		return rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{"tools": toolList()}}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		result, err := callTool(client, baseURL, token, p.Name, p.Arguments)
		if err != nil {
			return rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
				"content": []any{map[string]any{"type": "text", "text": err.Error()}},
				"isError": true,
			}}
		}
		return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
	default:
		return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32601, Message: "method not found"}}
	}
}

func toolList() []map[string]any {
	obj := func(props map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	num := map[string]any{"type": "number"}
	str := map[string]any{"type": "string"}
	return []map[string]any{
		{"name": "navigate", "description": "Open a URL in this conversation's NusaMation browser pane. Returns the page URL, title, viewport size, and a JPEG of the viewport. Coordinates in later calls are viewport fractions 0-1 (preferred) or CSS pixels.", "inputSchema": obj(map[string]any{"url": str}, "url")},
		{"name": "snapshot", "description": "Read the current page without changing it: URL, title, viewport size, and a JPEG of the viewport.", "inputSchema": obj(map[string]any{})},
		{"name": "emulate", "description": "Set device emulation for the pane: a preset name (desktop, mobile, tablet) or explicit fields — width, height, dpr, mobile, ua, touch. Mobile/tablet presets enable touch input and a mobile user agent; clicks become taps.", "inputSchema": obj(map[string]any{"preset": str, "width": num, "height": num, "dpr": num, "mobile": map[string]any{"type": "boolean"}, "ua": str, "touch": map[string]any{"type": "boolean"}})},
		{"name": "move", "description": "Move the pointer. x/y are viewport fractions 0-1 or CSS pixels. The user sees this pointer on the pane.", "inputSchema": obj(map[string]any{"x": num, "y": num}, "x", "y")},
		{"name": "click", "description": "Click (or tap, in a mobile profile). x/y are viewport fractions 0-1 or CSS pixels. button is left, middle, or right. clickCount defaults to 1.", "inputSchema": obj(map[string]any{"x": num, "y": num, "button": str, "clickCount": map[string]any{"type": "integer"}}, "x", "y")},
		{"name": "type", "description": "Insert text into the focused element.", "inputSchema": obj(map[string]any{"text": str}, "text")},
		{"name": "press", "description": "Press a key. Use a DOM key name (Enter, Tab, Escape, Backspace, ArrowDown) or a single character.", "inputSchema": obj(map[string]any{"key": str}, "key")},
		{"name": "scroll", "description": "Scroll the wheel. x/y are viewport fractions 0-1 or CSS pixels, defaulting to the viewport center. deltaY is positive downward.", "inputSchema": obj(map[string]any{"x": num, "y": num, "deltaX": num, "deltaY": num})},
	}
}

type apiResult struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SessionID string `json:"sessionId"`
	Image     string `json:"image"`
	MimeType  string `json:"mimeType"`
	Error     string `json:"error"`
	Code      string `json:"code"`
}

func callTool(client *http.Client, baseURL, token, name string, args json.RawMessage) (map[string]any, error) {
	if name == "" {
		return nil, fmt.Errorf("missing tool name")
	}
	body := map[string]any{"tool": name}
	if len(args) > 0 && string(args) != "null" {
		var extra map[string]any
		if err := json.Unmarshal(args, &extra); err != nil {
			return nil, fmt.Errorf("arguments must be an object")
		}
		for k, v := range extra {
			if k == "tool" {
				continue
			}
			body[k] = v
		}
	}
	body["tool"] = name
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/browser-use", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var parsed apiResult
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("browser_use response: %w", err)
	}
	if res.StatusCode >= 300 {
		msg := parsed.Error
		if msg == "" {
			msg = strings.TrimSpace(string(payload))
		}
		if msg == "" {
			msg = res.Status
		}
		return nil, fmt.Errorf("%s", msg)
	}
	text := fmt.Sprintf("url: %s\ntitle: %s\nviewport: %dx%d", parsed.URL, parsed.Title, parsed.Width, parsed.Height)
	content := []any{map[string]any{"type": "text", "text": text}}
	if parsed.Image != "" {
		mime := parsed.MimeType
		if mime == "" {
			mime = "image/jpeg"
		}
		content = append(content, map[string]any{"type": "image", "data": parsed.Image, "mimeType": mime})
	}
	return map[string]any{"content": content, "isError": false}, nil
}

// readMessage returns the next request body and whether it arrived
// Content-Length framed (true) or as a bare newline-delimited JSON line
// (false), so the response can mirror the client's transport.
func readMessage(r *bufio.Reader) ([]byte, bool, error) {
	// Some transports separate framed messages with blank lines; skip them
	// instead of treating the empty line as a missing Content-Length.
	var trim string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, false, err
		}
		trim = strings.TrimSpace(line)
		if trim != "" {
			break
		}
	}
	if strings.HasPrefix(trim, "{") {
		return []byte(trim), false, nil
	}
	header := trim
	length := 0
	for {
		t := strings.TrimSpace(header)
		if t == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(t), "content-length:") {
			n, err := strconv.Atoi(strings.TrimSpace(t[len("content-length:"):]))
			if err != nil || n < 0 {
				return nil, false, fmt.Errorf("bad Content-Length")
			}
			length = n
		}
		next, err := r.ReadString('\n')
		if err != nil {
			return nil, false, err
		}
		header = next
	}
	if length == 0 {
		return nil, false, fmt.Errorf("missing Content-Length")
	}
	if length > 4<<20 {
		return nil, false, fmt.Errorf("Content-Length too large")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, false, err
	}
	return buf, true, nil
}

func writeMessage(w *bufio.Writer, v any, framed bool) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if framed {
		if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
			return err
		}
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if !framed {
		if _, err := w.WriteString("\n"); err != nil {
			return err
		}
	}
	return w.Flush()
}
