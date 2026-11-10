// Package stdio implements the MCP stdio transport shared by NusaMation's
// embedded MCP servers. Each server supplies a name, tool list and call
// handler; this package owns JSON-RPC framing and the protocol methods.
package stdio

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ProtocolVersion is the MCP revision advertised to agents.
const ProtocolVersion = "2024-11-05"

// Tool is one entry in tools/list.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"inputSchema"`
}

// Server describes one embedded MCP server.
type Server struct {
	Name    string
	Version string
	Tools   []Tool
	// Call runs one tools/call request. The returned map is the MCP result
	// (normally {"content": [...], "isError": false}); an error becomes an
	// isError result so the agent can read it instead of losing the turn.
	Call func(name string, arguments json.RawMessage) (map[string]any, error)
}

type request struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve speaks MCP stdio until in closes. Requests may arrive
// newline-delimited (the MCP spec's stdio transport) or Content-Length framed
// (LSP style); each response goes out in the same framing as its request —
// spec clients cannot parse headers and header clients ignore bare lines.
func Serve(in io.Reader, out io.Writer, s Server) error {
	if s.Name == "" {
		return fmt.Errorf("mcp server name is required")
	}
	if s.Call == nil {
		return fmt.Errorf("mcp server %q has no call handler", s.Name)
	}
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	for {
		body, framed, err := ReadMessage(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue
		}
		if err := WriteMessage(w, handle(s, req), framed); err != nil {
			return err
		}
	}
}

func handle(s Server, req request) response {
	id := json.RawMessage(`null`)
	if req.ID != nil {
		id = *req.ID
	}
	switch req.Method {
	case "initialize":
		return response{JSONRPC: "2.0", ID: id, Result: map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}}
	case "ping":
		return response{JSONRPC: "2.0", ID: id, Result: map[string]any{}}
	case "tools/list":
		return response{JSONRPC: "2.0", ID: id, Result: map[string]any{"tools": s.Tools}}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if strings.TrimSpace(p.Name) == "" {
			return response{JSONRPC: "2.0", ID: id, Result: toolError("missing tool name")}
		}
		result, err := s.Call(p.Name, p.Arguments)
		if err != nil {
			return response{JSONRPC: "2.0", ID: id, Result: toolError(err.Error())}
		}
		if result == nil {
			result = map[string]any{"content": []any{}, "isError": false}
		}
		return response{JSONRPC: "2.0", ID: id, Result: result}
	default:
		return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32601, Message: "method not found"}}
	}
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": msg}},
		"isError": true,
	}
}

// ReadMessage returns the next request body and whether it arrived
// Content-Length framed (true) or as a bare newline-delimited JSON line
// (false), so the response can mirror the client's transport.
func ReadMessage(r *bufio.Reader) ([]byte, bool, error) {
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

// WriteMessage writes one response in the framing requested by the client.
func WriteMessage(w *bufio.Writer, v any, framed bool) error {
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
