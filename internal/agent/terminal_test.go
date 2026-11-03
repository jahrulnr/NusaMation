package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
)

func TestTerminalStreamsFullOutputAndKeepsACPOutputLimit(t *testing.T) {
	root := t.TempDir()
	tm := NewTerminalManager(nil)
	defer tm.CloseAll()
	updates := make(chan TerminalUpdate, 32)
	limit := int64(4)
	req, _ := json.Marshal(acp.TerminalCreateRequest{SessionID: "s", Command: "/bin/sh", Args: []string{"-c", "printf first; sleep 0.2; printf last; printf err >&2"}, Cwd: root, OutputByteLimit: &limit})
	result, err := tm.Handle(context.Background(), "terminal/create", req, []string{root}, func(u TerminalUpdate) { updates <- u })
	if err != nil {
		t.Fatal(err)
	}
	id := result.(acp.TerminalCreateResponse).TerminalID
	var sawEarly, sawExit bool
	deadline := time.After(5 * time.Second)
	for !sawExit {
		select {
		case u := <-updates:
			if u.ID != id {
				t.Fatalf("wrong terminal: %q", u.ID)
			}
			if strings.Contains(u.Output, "first") && u.ExitStatus == nil {
				sawEarly = true
			}
			if u.ExitStatus != nil {
				sawExit = true
				if !strings.Contains(u.Output, "first") || !strings.Contains(u.Output, "last") || !strings.Contains(u.Output, "err") {
					t.Fatalf("incomplete full output: %q", u.Output)
				}
				if u.ExitStatus.ExitCode == nil || *u.ExitStatus.ExitCode != 0 {
					t.Fatalf("unexpected exit: %#v", u.ExitStatus)
				}
			}
		case <-deadline:
			t.Fatal("terminal did not exit")
		}
	}
	if !sawEarly {
		t.Fatal("no output received before exit")
	}
	ref, _ := json.Marshal(acp.TerminalRef{SessionID: "s", TerminalID: id})
	response, rpcErr := tm.Handle(context.Background(), "terminal/output", ref, []string{root}, nil)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	out := response.(acp.TerminalOutputResponse)
	if !out.Truncated || len(out.Output) > 4 {
		t.Fatalf("ACP output limit not honored: %#v", out)
	}
}
