package acp

import (
	"encoding/json"
	"testing"
)

// Strict ACP agents validate mcpServers with a discriminated union: stdio
// entries must carry command/args/env, http/sse entries type/url/headers, and
// the array fields must be present even when empty.
func TestMCPServerConfigMarshalVariants(t *testing.T) {
	stdio, err := json.Marshal(MCPServerConfig{Name: "fs", Command: "mcp-fs"})
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(stdio, &s); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "command", "args", "env"} {
		if _, ok := s[key]; !ok {
			t.Fatalf("stdio entry missing %q: %s", key, stdio)
		}
	}
	for _, key := range []string{"type", "url", "headers"} {
		if _, ok := s[key]; ok {
			t.Fatalf("stdio entry must not carry %q: %s", key, stdio)
		}
	}
	if got, _ := s["args"].([]any); len(got) != 0 {
		t.Fatalf("args = %v, want []", s["args"])
	}

	http, err := json.Marshal(MCPServerConfig{Name: "web", Type: "http", URL: "https://mcp.example"})
	if err != nil {
		t.Fatal(err)
	}
	var h map[string]any
	if err := json.Unmarshal(http, &h); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "type", "url", "headers"} {
		if _, ok := h[key]; !ok {
			t.Fatalf("http entry missing %q: %s", key, http)
		}
	}
	for _, key := range []string{"command", "args", "env"} {
		if _, ok := h[key]; ok {
			t.Fatalf("http entry must not carry %q: %s", key, http)
		}
	}
	if h["type"] != "http" {
		t.Fatalf("type = %v", h["type"])
	}
	if got, _ := h["headers"].([]any); len(got) != 0 {
		t.Fatalf("headers = %v, want []", h["headers"])
	}

	sse, err := json.Marshal(MCPServerConfig{Name: "events", Type: "sse", URL: "https://mcp.example/sse",
		Headers: []EnvVar{{Name: "Authorization", Value: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	var e map[string]any
	if err := json.Unmarshal(sse, &e); err != nil {
		t.Fatal(err)
	}
	if e["type"] != "sse" {
		t.Fatalf("type = %v", e["type"])
	}
	if got, _ := e["headers"].([]any); len(got) != 1 {
		t.Fatalf("headers = %v, want one entry", e["headers"])
	}
}
