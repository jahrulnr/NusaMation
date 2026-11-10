package stdio

import (
	"bufio"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
)

// Transports that separate framed messages with blank lines must not kill the
// server: previously a blank line between bodies was read as a missing
// Content-Length and the process exited, so every later request failed.
func TestReadMessageSkipsBlankLines(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	frame := "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	stream := frame + "\r\n" + frame + "\r\n"
	r := bufio.NewReader(strings.NewReader(stream))
	for i := range 2 {
		got, framed, err := ReadMessage(r)
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if !framed {
			t.Fatalf("message %d not flagged framed", i)
		}
		if string(got) != body {
			t.Fatalf("message %d = %s", i, got)
		}
	}
	if _, _, err := ReadMessage(r); err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

// MCP spec clients speak newline-delimited JSON; the reply must come back in
// the same framing or the client waits forever on a line it can't parse.
func TestResponseMirrorsRequestFraming(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":1,"method":"ping"}`

	var ndOut strings.Builder
	ndW := bufio.NewWriter(&ndOut)
	if err := WriteMessage(ndW, response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: map[string]any{}}, false); err != nil {
		t.Fatal(err)
	}
	if got := ndOut.String(); !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "\n") || strings.Contains(got, "Content-Length") {
		t.Fatalf("ndjson reply malformed: %q", got)
	}

	var frOut strings.Builder
	frW := bufio.NewWriter(&frOut)
	if err := WriteMessage(frW, response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: map[string]any{}}, true); err != nil {
		t.Fatal(err)
	}
	if got := frOut.String(); !strings.HasPrefix(got, "Content-Length:") {
		t.Fatalf("framed reply missing header: %q", got)
	}

	// Round trip: NDJSON in, NDJSON back.
	serverR, clientW := io.Pipe()
	clientR, serverW := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(serverR, serverW, Server{Name: "test", Version: "1", Call: func(string, json.RawMessage) (map[string]any, error) {
			return map[string]any{"content": []any{}, "isError": false}, nil
		}})
	}()
	if _, err := clientW.Write([]byte(req + "\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(clientR).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "{") {
		t.Fatalf("ndjson request got framed reply: %q", line)
	}
	_ = clientW.Close()
	_ = serverW.Close()
	<-errc
}

func TestServeProtocolMethods(t *testing.T) {
	call := func(name string, args json.RawMessage) (map[string]any, error) {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": name + ":" + string(args)}}}, nil
	}
	s := Server{
		Name: "test_server", Version: "7",
		Tools: []Tool{{Name: "read", Description: "Read", InputSchema: map[string]any{"type": "object"}}},
		Call:  call,
	}

	init := handle(s, request{ID: raw(`1`), Method: "initialize"})
	res, _ := init.Result.(map[string]any)
	if res["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion = %v", res["protocolVersion"])
	}
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != "test_server" || info["version"] != "7" {
		t.Fatalf("serverInfo = %#v", info)
	}

	list := handle(s, request{ID: raw(`2`), Method: "tools/list"})
	lr, _ := list.Result.(map[string]any)
	tools, _ := lr["tools"].([]Tool)
	if len(tools) != 1 || tools[0].Name != "read" {
		t.Fatalf("tools = %#v", lr["tools"])
	}

	called := handle(s, request{ID: raw(`3`), Method: "tools/call", Params: rawBody(`{"name":"read","arguments":{"conversationId":"cv_1"}}`)})
	cr, _ := called.Result.(map[string]any)
	content, _ := cr["content"].([]any)
	if len(content) != 1 || !strings.Contains(content[0].(map[string]any)["text"].(string), `"cv_1"`) {
		t.Fatalf("call result = %#v", cr)
	}

	unknown := handle(s, request{ID: raw(`4`), Method: "nope"})
	if unknown.Error == nil || unknown.Error.Code != -32601 {
		t.Fatalf("unknown method = %#v", unknown)
	}
}

func TestServeToolErrorIsAResult(t *testing.T) {
	s := Server{Name: "test", Version: "1", Call: func(string, json.RawMessage) (map[string]any, error) {
		return nil, io.ErrUnexpectedEOF
	}}
	resp := handle(s, request{ID: raw(`1`), Method: "tools/call", Params: rawBody(`{"name":"read"}`)})
	res, _ := resp.Result.(map[string]any)
	if res["isError"] != true {
		t.Fatalf("result = %#v", res)
	}
	content, _ := res["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != io.ErrUnexpectedEOF.Error() {
		t.Fatalf("content = %#v", content)
	}
}

func TestServeRequiresCallHandler(t *testing.T) {
	if err := Serve(strings.NewReader(""), io.Discard, Server{Name: "test"}); err == nil {
		t.Fatal("expected missing handler error")
	}
}

func raw(s string) *json.RawMessage { m := json.RawMessage(s); return &m }

func rawBody(s string) json.RawMessage { return json.RawMessage(s) }
