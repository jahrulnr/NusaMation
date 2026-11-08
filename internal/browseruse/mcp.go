package browseruse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
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
	boolean := map[string]any{"type": "boolean"}
	refProps := func(extra map[string]any) map[string]any {
		props := map[string]any{
			"element": map[string]any{"type": "string", "description": "Human-readable element description used for clarity"},
			"ref":     map[string]any{"type": "string", "description": "Exact element ref from browser_snapshot, e.g. e12"},
		}
		maps.Copy(props, extra)
		return props
	}
	return []map[string]any{
		{"name": "browser_navigate", "description": "Navigate to a URL in this conversation's NusaMation browser pane. Returns URL, title, viewport size, and a screenshot.", "inputSchema": obj(map[string]any{"url": str}, "url")},
		{"name": "browser_navigate_back", "description": "Go back to the previous page.", "inputSchema": obj(map[string]any{})},
		{"name": "browser_navigate_forward", "description": "Go forward to the next page.", "inputSchema": obj(map[string]any{})},
		{"name": "browser_snapshot", "description": "Capture an accessibility snapshot of the page: an indented tree of roles, names, and element refs (e.g. `- button \"Search\" [ref=e12]`). Prefer this over screenshots — pass refs to click, type, hover, select_option, fill_form. Refs are re-stamped on every snapshot; stale refs error with 'run browser_snapshot again'. Names resolve `<label>`/`aria-labelledby`; the tree ends with a truncation marker when the line/depth cap hides elements. Enters open shadow roots.", "inputSchema": obj(map[string]any{})},
		{"name": "browser_take_screenshot", "description": "Take a JPEG screenshot of the current viewport.", "inputSchema": obj(map[string]any{})},
		{"name": "browser_click", "description": "Click an element by its ref from browser_snapshot. Optional button (left, middle, right) and doubleClick. Fails with not_actionable when the element is disabled or covered.", "inputSchema": obj(refProps(map[string]any{"button": str, "doubleClick": boolean}), "ref")},
		{"name": "browser_mouse_click_xy", "description": "Click at viewport coordinates: fractions 0-1 (preferred) or CSS pixels.", "inputSchema": obj(map[string]any{"x": num, "y": num, "button": str, "doubleClick": boolean, "clickCount": num}, "x", "y")},
		{"name": "browser_mouse_move_xy", "description": "Move the pointer to viewport coordinates (fractions 0-1 or CSS pixels). The user sees this pointer on the pane.", "inputSchema": obj(map[string]any{"x": num, "y": num}, "x", "y")},
		{"name": "browser_hover", "description": "Hover over an element by its ref from browser_snapshot. Fails with not_actionable when the element is disabled or covered.", "inputSchema": obj(refProps(nil), "ref")},
		{"name": "browser_type", "description": "Type text into an editable element by its ref (or into the focused element when ref is omitted). Set submit to press Enter afterwards. Disabled and read-only fields fail with not_actionable.", "inputSchema": obj(refProps(map[string]any{"text": str, "submit": boolean}), "text")},
		{"name": "browser_press_key", "description": "Press a key by DOM key name: Enter, Tab, Escape, Backspace, ArrowDown, or a single character.", "inputSchema": obj(map[string]any{"key": str}, "key")},
		{"name": "browser_scroll", "description": "Scroll the wheel. x/y are viewport fractions 0-1 or CSS pixels, defaulting to the viewport center. deltaY is positive downward.", "inputSchema": obj(map[string]any{"x": num, "y": num, "deltaX": num, "deltaY": num})},
		{"name": "browser_select_option", "description": "Select options in a <select> by ref; values are option values or visible labels. A missing option, a disabled select, or multiple values on a single select is an error.", "inputSchema": obj(refProps(map[string]any{"values": map[string]any{"type": "array", "items": str}}), "ref", "values")},
		{"name": "browser_fill_form", "description": "Fill multiple form fields in one call. Each field: {ref, name, type: textbox|checkbox|radio|combobox, value}. Uses native setters so framework-controlled inputs register the change. A missing, disabled, read-only, or non-fillable field fails the call.", "inputSchema": obj(map[string]any{"fields": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"ref": str, "name": str, "type": str, "value": str}, "required": []string{"ref", "value"}}}}, "fields")},
		{"name": "browser_wait_for", "description": "Wait for: a number of seconds (time), text to appear (text), or text to disappear (textGone). Exactly one argument.", "inputSchema": obj(map[string]any{"time": num, "text": str, "textGone": str})},
		{"name": "browser_evaluate", "description": "Evaluate JavaScript in the page (Runtime.evaluate). Pass an expression via `expression` or a function via `function` — functions are invoked with no arguments. Promises are awaited; the JSON result comes back in `result`. BigInt/NaN/Infinity are returned as strings; -0 stays -0.", "inputSchema": obj(map[string]any{"expression": str, "function": str})},
		{"name": "browser_console_messages", "description": "Return console messages (log/warn/error) plus page errors captured since the page loaded, newest last, up to 200.", "inputSchema": obj(map[string]any{})},
		{"name": "browser_resize", "description": "Resize the browser viewport to width x height CSS pixels.", "inputSchema": obj(map[string]any{"width": num, "height": num}, "width", "height")},
		{"name": "browser_emulate", "description": "Set device emulation: a preset (desktop, mobile, tablet) or explicit fields — width, height, dpr, mobile, ua, touch. Mobile/tablet presets enable touch and a mobile user agent; clicks become taps.", "inputSchema": obj(map[string]any{"preset": str, "width": num, "height": num, "dpr": num, "mobile": boolean, "ua": str, "touch": boolean})},
	}
}

type apiResult struct {
	URL       string          `json:"url"`
	Title     string          `json:"title"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	SessionID string          `json:"sessionId"`
	Image     string          `json:"image"`
	MimeType  string          `json:"mimeType"`
	Result    json.RawMessage `json:"result"`
	Snapshot  string          `json:"snapshot"`
	Detail    string          `json:"detail"`
	Error     string          `json:"error"`
	Code      string          `json:"code"`
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
	if parsed.Snapshot != "" {
		text += fmt.Sprintf("\nsnapshot:\n%s", parsed.Snapshot)
	}
	if parsed.Detail != "" {
		text += fmt.Sprintf("\ndetail: %s", parsed.Detail)
	}
	if len(parsed.Result) > 0 {
		if name == "browser_console_messages" {
			text += fmt.Sprintf("\nmessages: %s", parsed.Result)
		} else {
			text += fmt.Sprintf("\nresult: %s", parsed.Result)
		}
	}
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
