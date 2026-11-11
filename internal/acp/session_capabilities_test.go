package acp

import (
	"encoding/json"
	"testing"
)

func TestSessionLifecycleCapabilities(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		supported bool
	}{{"{}", true}, {"null", false}, {"", false}} {
		var result InitializeResult
		result.AgentCapabilities.SessionCapabilities.Close = json.RawMessage(tc.raw)
		result.AgentCapabilities.SessionCapabilities.Delete = json.RawMessage(tc.raw)
		result.AgentCapabilities.SessionCapabilities.Resume = json.RawMessage(tc.raw)
		if result.SupportsClose() != tc.supported || result.SupportsDelete() != tc.supported || result.SupportsResume() != tc.supported {
			t.Fatalf("capability %q: close=%v delete=%v", tc.raw, result.SupportsClose(), result.SupportsDelete())
		}
	}
}

func TestSystemPromptCapability(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"", false}, {"null", false}, {"false", false}, {"true", false}, {"[]", false}, {"{}", true}, {`{"mode":{"override":{}}}`, true},
	} {
		var result InitializeResult
		result.AgentCapabilities.SessionCapabilities.SystemPrompt = json.RawMessage(tc.raw)
		if got := result.SupportsSystemPrompt(); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.raw, got, tc.want)
		}
	}
}
