package browseruse

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	for i := 0; i < 2; i++ {
		got, err := readMessage(r)
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if string(got) != body {
			t.Fatalf("message %d = %s", i, got)
		}
	}
	if _, err := readMessage(r); err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestMCPNavigate(t *testing.T) {
	var gotAuth, gotTool string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotTool, _ = body["tool"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"url": "https://ex.test/", "title": "Ex", "width": 800, "height": 600,
			"image": "aGVsbG8=", "mimeType": "image/jpeg",
		})
	}))
	defer srv.Close()

	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	errc := make(chan error, 1)
	go func() { errc <- Serve(serverR, serverW, srv.URL, "tok") }()

	mustWrite(t, clientW, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	initRes := mustRead(t, clientR)
	if initRes["error"] != nil {
		t.Fatalf("initialize: %v", initRes)
	}
	mustWrite(t, clientW, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "navigate", "arguments": map[string]any{"url": "https://ex.test"}},
	})
	call := mustRead(t, clientR)
	result, _ := call["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content = %#v", result)
	}
	if gotAuth != "Bearer tok" || gotTool != "navigate" {
		t.Fatalf("auth=%q tool=%q", gotAuth, gotTool)
	}
	_ = clientW.Close()
	_ = serverW.Close()
	<-errc
}

func mustWrite(t *testing.T, w io.Writer, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, r io.Reader) map[string]any {
	t.Helper()
	br := bufio.NewReader(r)
	body, err := readMessage(br)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "jsonrpc") {
		t.Fatalf("body %s", body)
	}
	return m
}
