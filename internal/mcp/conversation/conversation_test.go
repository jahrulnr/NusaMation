package conversation

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

func TestFilterTools(t *testing.T) {
	all := toolList()
	subset := filterTools(all, "list, read, wait")
	if len(subset) != 3 || subset[0].Name != "list" || subset[1].Name != "read" || subset[2].Name != "wait" {
		t.Fatalf("subset = %+v", subset)
	}
	if got := filterTools(all, ""); len(got) != len(all) {
		t.Fatalf("empty filter = %d", len(got))
	}
	if got := filterTools(all, "list,nope,wait"); len(got) != 2 {
		t.Fatalf("unknown filter = %+v", got)
	}
}

func TestToolListNames(t *testing.T) {
	want := []string{"list", "projects_list", "search", "export", "read", "wait", "send", "stop", "spawn", "report"}
	seen := map[string]bool{}
	for _, tool := range toolList() {
		if strings.HasPrefix(tool.Name, "conversation_") {
			t.Fatalf("tool %q repeats the server namespace", tool.Name)
		}
		if seen[tool.Name] {
			t.Fatalf("duplicate tool %q", tool.Name)
		}
		seen[tool.Name] = true
		if tool.InputSchema == nil {
			t.Fatalf("tool %q has no input schema", tool.Name)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("tools = %v", seen)
	}
	for _, name := range want {
		if !seen[name] {
			t.Fatalf("missing tool %q", name)
		}
	}
}

func TestMCPList(t *testing.T) {
	var gotAuth, gotPath, gotTool, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotTool, _ = body["tool"].(string)
		gotQuery, _ = body["query"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"caller":        map[string]any{"id": "cv_self"},
			"conversations": []any{map[string]any{"id": "cv_1", "title": "Worker"}},
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
	res, _ := initRes["result"].(map[string]any)
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != "nusamation_conversation" {
		t.Fatalf("serverInfo = %#v", info)
	}

	mustWrite(t, clientW, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	list := mustRead(t, clientR)
	result, _ := list["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 10 {
		t.Fatalf("tools = %#v", result["tools"])
	}

	mustWrite(t, clientW, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "list", "arguments": map[string]any{"query": "worker"}},
	})
	call := mustRead(t, clientR)
	callResult, _ := call["result"].(map[string]any)
	content, _ := callResult["content"].([]any)
	if len(content) != 1 || !strings.Contains(content[0].(map[string]any)["text"].(string), "cv_1") {
		t.Fatalf("content = %#v", callResult)
	}
	if gotAuth != "Bearer tok" || gotPath != "/api/conversation-control" || gotTool != "list" || gotQuery != "worker" {
		t.Fatalf("auth=%q path=%q tool=%q query=%q", gotAuth, gotPath, gotTool, gotQuery)
	}
	_ = clientW.Close()
	_ = serverW.Close()
	<-errc
}

func TestCallToolRejectsNonObjectArguments(t *testing.T) {
	if _, err := callTool(nil, "list", json.RawMessage(`[]`)); err == nil || !strings.Contains(err.Error(), "arguments must be an object") {
		t.Fatalf("err = %v", err)
	}
}

func TestCallToolSurfacesBridgeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "orchestrated turns have no control access", "code": "orchestrated_turn"})
	}))
	defer srv.Close()
	client := loopback.New(srv.URL, "tok")
	if _, err := callTool(client, "list", nil); err == nil || err.Error() != "orchestrated turns have no control access" {
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
	return m
}
