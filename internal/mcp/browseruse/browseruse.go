// Package browseruse is the stdio MCP server named browser_use. The desktop
// process issues a token per conversation; the agent spawns this process, and
// every tool call posts back to that process over loopback.
package browseruse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strings"

	"github.com/jahrulnr/nusamation/internal/mcp/loopback"
	"github.com/jahrulnr/nusamation/internal/mcp/stdio"
)

// EnvURL and EnvToken are set on the MCP child by the desktop process.
const (
	EnvURL   = "NUSAMATION_BROWSER_USE_URL"
	EnvToken = "NUSAMATION_BROWSER_USE_TOKEN"
)

// Serve speaks MCP stdio until in closes. baseURL is the NusaMation loopback
// origin, without a trailing slash.
func Serve(in io.Reader, out io.Writer, baseURL, token string) error {
	if baseURL == "" || token == "" {
		return fmt.Errorf("browser_use requires %s and %s", EnvURL, EnvToken)
	}
	client := loopback.New(baseURL, token)
	return stdio.Serve(in, out, stdio.Server{
		Name:    "browser_use",
		Version: "1",
		Tools:   toolList(),
		Call: func(name string, arguments json.RawMessage) (map[string]any, error) {
			return callTool(client, name, arguments)
		},
	})
}

func toolList() []stdio.Tool {
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
	return []stdio.Tool{
		{Name: "browser_navigate", Description: "Navigate to a URL in this conversation's NusaMation browser pane. Returns URL, title, viewport size, and a screenshot.", InputSchema: obj(map[string]any{"url": str}, "url")},
		{Name: "browser_navigate_back", Description: "Go back to the previous page.", InputSchema: obj(map[string]any{})},
		{Name: "browser_navigate_forward", Description: "Go forward to the next page.", InputSchema: obj(map[string]any{})},
		{Name: "browser_snapshot", Description: "Capture an accessibility snapshot of the page: an indented tree of roles, names, and element refs (e.g. `- button \"Search\" [ref=e12]`). Prefer this over screenshots — pass refs to click, type, hover, select_option, fill_form. Refs are re-stamped on every snapshot; stale refs error with 'run browser_snapshot again'. Names resolve `<label>`/`aria-labelledby`; the tree ends with a truncation marker when the line/depth cap hides elements. Enters open shadow roots.", InputSchema: obj(map[string]any{})},
		{Name: "browser_take_screenshot", Description: "Take a JPEG screenshot of the current viewport.", InputSchema: obj(map[string]any{})},
		{Name: "browser_click", Description: "Click an element by its ref from browser_snapshot. Optional button (left, middle, right) and doubleClick. Fails with not_actionable when the element is disabled or covered.", InputSchema: obj(refProps(map[string]any{"button": str, "doubleClick": boolean}), "ref")},
		{Name: "browser_mouse_click_xy", Description: "Click at viewport coordinates: fractions 0-1 (preferred) or CSS pixels.", InputSchema: obj(map[string]any{"x": num, "y": num, "button": str, "doubleClick": boolean, "clickCount": num}, "x", "y")},
		{Name: "browser_mouse_move_xy", Description: "Move the pointer to viewport coordinates (fractions 0-1 or CSS pixels). The user sees this pointer on the pane.", InputSchema: obj(map[string]any{"x": num, "y": num}, "x", "y")},
		{Name: "browser_hover", Description: "Hover over an element by its ref from browser_snapshot. Fails with not_actionable when the element is disabled or covered.", InputSchema: obj(refProps(nil), "ref")},
		{Name: "browser_type", Description: "Type text into an editable element by its ref (or into the focused element when ref is omitted). Set submit to press Enter afterwards. Disabled and read-only fields fail with not_actionable. Text may include [[env:NAME]] references to this conversation agent's saved environment variables; these resolve only in browser input. Unknown or malformed references fail before typing. Prefix an opening tag with a backslash to type it literally.", InputSchema: obj(refProps(map[string]any{"text": str, "submit": boolean}), "text")},
		{Name: "browser_press_key", Description: "Press a key by DOM key name: Enter, Tab, Escape, Backspace, ArrowDown, or a single character.", InputSchema: obj(map[string]any{"key": str}, "key")},
		{Name: "browser_scroll", Description: "Scroll the wheel. x/y are viewport fractions 0-1 or CSS pixels, defaulting to the viewport center. deltaY is positive downward.", InputSchema: obj(map[string]any{"x": num, "y": num, "deltaX": num, "deltaY": num})},
		{Name: "browser_select_option", Description: "Select options in a <select> by ref; values are option values or visible labels. A missing option, a disabled select, or multiple values on a single select is an error.", InputSchema: obj(refProps(map[string]any{"values": map[string]any{"type": "array", "items": str}}), "ref", "values")},
		{Name: "browser_fill_form", Description: "Fill multiple form fields in one call. Each field: {ref, name, type: textbox|checkbox|radio|combobox, value}. Uses native setters so framework-controlled inputs register the change. A missing, disabled, read-only, or non-fillable field fails the call. Textbox values may include [[env:NAME]] references, resolved before any field is changed; unknown references fail the call. Secret-backed text fields are replaced through browser text input.", InputSchema: obj(map[string]any{"fields": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"ref": str, "name": str, "type": str, "value": str}, "required": []string{"ref", "value"}}}}, "fields")},
		{Name: "browser_wait_for", Description: "Wait for: a number of seconds (time), text to appear (text), or text to disappear (textGone). Exactly one argument.", InputSchema: obj(map[string]any{"time": num, "text": str, "textGone": str})},
		{Name: "browser_evaluate", Description: "Evaluate JavaScript in the page (Runtime.evaluate). Pass an expression via `expression` or a function via `function` — functions are invoked with no arguments. Promises are awaited; the JSON result comes back in `result`. BigInt/NaN/Infinity are returned as strings; -0 stays -0.", InputSchema: obj(map[string]any{"expression": str, "function": str})},
		{Name: "browser_console_messages", Description: "Return console messages (log/warn/error) plus page errors captured since the page loaded, newest last, up to 200.", InputSchema: obj(map[string]any{})},
		{Name: "browser_resize", Description: "Resize the browser viewport to width x height CSS pixels.", InputSchema: obj(map[string]any{"width": num, "height": num}, "width", "height")},
		{Name: "browser_emulate", Description: "Set device emulation: a preset (desktop, mobile, tablet) or explicit fields — width, height, dpr, mobile, ua, touch. Mobile/tablet presets enable touch and a mobile user agent; clicks become taps.", InputSchema: obj(map[string]any{"preset": str, "width": num, "height": num, "dpr": num, "mobile": boolean, "ua": str, "touch": boolean})},
		{Name: "browser_vault_fill", Description: "Fill the current page login using a saved credential for this browser profile and exact origin. Optionally specify the exact username to choose an account; otherwise the most recent is used. Returns only whether a login was filled; the password is not returned. Does not submit the form or replace a different password already in the field.", InputSchema: obj(map[string]any{"username": str})},
		{Name: "browser_profile", Description: "Switch the pane to a different persistent browser profile (name). Each profile keeps its own cookies, storage, history, and saved logins across restarts; the pane's current page reloads in the new profile. Omit profile or use 'default' for the default profile.", InputSchema: obj(map[string]any{"profile": str})},
		{Name: "browser_lock", Description: "Take or release the pane's input lease: mode=true mutes input from every human pane viewer so a stray click cannot disturb the run (they keep watching the live view); mode=false hands control back to the current viewer. A human can always preempt with Take over, which ends the lock.", InputSchema: obj(map[string]any{"mode": boolean}, "mode")},
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

func callTool(client *loopback.Client, name string, args json.RawMessage) (map[string]any, error) {
	if strings.TrimSpace(name) == "" {
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
	var parsed apiResult
	if err := client.Post(context.Background(), "/api/browser-use", body, &parsed); err != nil {
		return nil, err
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("%s", parsed.Error)
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
