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
		if result.SupportsClose() != tc.supported || result.SupportsDelete() != tc.supported {
			t.Fatalf("capability %q: close=%v delete=%v", tc.raw, result.SupportsClose(), result.SupportsDelete())
		}
	}
}
