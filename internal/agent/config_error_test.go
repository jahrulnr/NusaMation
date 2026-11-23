package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jahrulnr/nusamation/internal/acp"
)

func TestConfigSelectErrorClassifiesOnlyValueRejections(t *testing.T) {
	rejected := configSelectError("speed", fmt.Errorf("select speed: %w", &acp.RPCError{
		Code: acp.CodeInvalidParams, Message: "Invalid value 'fast' for config option 'speed'",
	}))
	var cfgErr *ConfigUnavailableError
	if !errors.As(rejected, &cfgErr) {
		t.Fatalf("invalid params must be a config rejection, got %v", rejected)
	}
	if cfgErr.ConfigID != "speed" {
		t.Fatalf("config id = %q", cfgErr.ConfigID)
	}

	temporary := []struct {
		name string
		err  error
	}{
		{"timeout", context.DeadlineExceeded},
		{"process exit", acp.ErrProcessExited},
		{"transport", errors.New("write |1: file already closed")},
	}
	for _, tc := range temporary {
		err := configSelectError("speed", fmt.Errorf("select speed: %w", tc.err))
		if errors.As(err, &cfgErr) {
			t.Fatalf("%s must stay a startup error, got config rejection: %v", tc.name, err)
		}
		if !errors.Is(err, tc.err) {
			t.Fatalf("%s: cause was not preserved: %v", tc.name, err)
		}
	}

	if got := (&ConfigUnavailableError{}).Error(); got == "" {
		t.Fatal("a nil cause must still render a message")
	}
	if (&ConfigUnavailableError{}).Unwrap() != nil {
		t.Fatal("a nil cause must unwrap to nil")
	}
}
