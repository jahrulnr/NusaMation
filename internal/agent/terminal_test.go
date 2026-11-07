package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

func TestTerminalStreamsFullOutputAndKeepsACPOutputLimit(t *testing.T) {
	testutil.RequirePosixShell(t)
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

func TestTerminalRunsShellLineWithoutArgs(t *testing.T) {
	testutil.RequirePosixShell(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Devin-style: the whole shell line is the command, with no args.
	output := runTerminalToExit(t, acp.TerminalCreateRequest{SessionID: "s", Command: "cat marker.txt && echo done", Cwd: root}, root)
	if !strings.Contains(output, "marker") || !strings.Contains(output, "done") {
		t.Fatalf("shell line output = %q", output)
	}
}

func TestTerminalKeepsArgvForProgramPathWithSpaces(t *testing.T) {
	testutil.RequirePosixShell(t)
	root := t.TempDir()
	dir := filepath.Join(root, "with space")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, "tool")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf argv-ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := runTerminalToExit(t, acp.TerminalCreateRequest{SessionID: "s", Command: program, Cwd: root}, root)
	if output != "argv-ok" {
		t.Fatalf("program output = %q", output)
	}
}

func runTerminalToExit(t *testing.T, req acp.TerminalCreateRequest, root string) string {
	t.Helper()
	tm := NewTerminalManager(nil)
	t.Cleanup(tm.CloseAll)
	params, _ := json.Marshal(req)
	result, rpcErr := tm.Handle(context.Background(), "terminal/create", params, []string{root}, nil)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	ref, _ := json.Marshal(acp.TerminalRef{SessionID: req.SessionID, TerminalID: result.(acp.TerminalCreateResponse).TerminalID})
	if _, rpcErr := tm.Handle(context.Background(), "terminal/wait_for_exit", ref, []string{root}, nil); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	out, rpcErr := tm.Handle(context.Background(), "terminal/output", ref, []string{root}, nil)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	return out.(acp.TerminalOutputResponse).Output
}
