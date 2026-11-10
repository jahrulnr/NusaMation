package browseruse

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/mcp/loopback"
	"github.com/jahrulnr/nusamation/internal/mcp/stdio"
)

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

func TestToolListUsesBrowserPrefix(t *testing.T) {
	seen := map[string]bool{}
	for _, tool := range toolList() {
		if !strings.HasPrefix(tool.Name, "browser_") {
			t.Fatalf("tool %q is outside the browser_ namespace", tool.Name)
		}
		if seen[tool.Name] {
			t.Fatalf("duplicate tool %q", tool.Name)
		}
		seen[tool.Name] = true
		if tool.InputSchema == nil {
			t.Fatalf("tool %q has no input schema", tool.Name)
		}
	}
	if len(seen) < 19 {
		t.Fatalf("tool count = %d", len(seen))
	}
}

func TestCallToolRejectsNonObjectArguments(t *testing.T) {
	if _, err := callTool(nil, "navigate", json.RawMessage(`[]`)); err == nil || !strings.Contains(err.Error(), "arguments must be an object") {
		t.Fatalf("err = %v", err)
	}
}

func TestCallToolSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "boom"})
	}))
	defer srv.Close()
	client := newTestClient(srv.URL)
	if _, err := callTool(client, "navigate", json.RawMessage(`{"url":"https://ex.test"}`)); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v", err)
	}
}

func TestServeRequiresConfig(t *testing.T) {
	if err := Serve(nil, nil, "", "tok"); err == nil {
		t.Fatal("expected missing URL error")
	}
	if err := Serve(nil, nil, "http://127.0.0.1", ""); err == nil {
		t.Fatal("expected missing token error")
	}
}

func newTestClient(baseURL string) *loopback.Client {
	return loopback.New(baseURL, "tok")
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
	body, _, err := stdio.ReadMessage(br)
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
