package chat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestTimelineStreamsAndLargeToolSnapshots(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "timeline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "Timeline", Slug: "timeline", Command: "/bin/true", Cwd: t.TempDir(), Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "Timeline", ag.Cwd, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.CreateTurn(ctx, conv.ID, "", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	a := &activeTurn{conversationID: conv.ID, turn: turn, tools: map[string]*toolState{}}
	s.appendAgentThought(a, "first thought")
	s.rotateStream(a, "thought")
	large := strings.Repeat("output", 100000)
	raw, _ := json.Marshal(large)
	s.applyToolUpdate(a, acp.SessionUpdate{ToolCallID: "tool-1", Title: "First tool", RawOutput: raw})
	s.appendAgentThought(a, "second thought")
	s.rotateStream(a, "thought")
	s.applyToolUpdate(a, acp.SessionUpdate{ToolCallID: "tool-2", Title: "Second tool"})
	// Output can precede the tool's terminal reference and continue afterwards.
	s.applyTerminalUpdate(a, agent.TerminalUpdate{ID: "term-1", Output: "early", Version: 1})
	s.applyToolUpdate(a, acp.SessionUpdate{ToolCallID: "tool-2", Content: json.RawMessage(`[{"type":"terminal","terminalId":"term-1"}]`)})
	code := 1
	s.applyTerminalUpdate(a, agent.TerminalUpdate{ID: "term-1", Output: "early and late", ExitStatus: &acp.TerminalExitStatus{ExitCode: &code}, Version: 3})
	s.applyTerminalUpdate(a, agent.TerminalUpdate{ID: "term-1", Output: "stale", Version: 2})
	s.appendAgentText(a, "", "final answer")
	s.flushStreams(a, true, "completed")
	// Null updates leave previously reported input/output unchanged, per ACP.
	s.applyToolUpdate(a, acp.SessionUpdate{ToolCallID: "tool-1", RawOutput: json.RawMessage("null"), Status: "completed"})
	msgs, err := st.ListMessages(ctx, conv.ID, 0, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Fatalf("got %d messages: %#v", len(msgs), msgs)
	}
	want := []string{"thought", "tool", "thought", "tool", "text"}
	for i, kind := range want {
		if msgs[i].Kind != kind {
			t.Fatalf("message %d kind = %s, want %s", i, msgs[i].Kind, kind)
		}
	}
	var tool struct {
		ToolCallID string `json:"toolCallId"`
		RawOutput  string `json:"rawOutput"`
	}
	if err := json.Unmarshal(msgs[1].Content, &tool); err != nil {
		t.Fatal(err)
	}
	if tool.ToolCallID != "tool-1" || tool.RawOutput != large {
		t.Fatalf("tool id or large output not preserved: id=%q size=%d", tool.ToolCallID, len(tool.RawOutput))
	}
	if msgs[0].Text != "first thought" || msgs[2].Text != "second thought" || msgs[4].Text != "final answer" {
		t.Fatal("stream segments lost or changed")
	}
	var terminalTool struct {
		Terminals map[string]agent.TerminalUpdate `json:"terminals"`
	}
	if err := json.Unmarshal(msgs[3].Content, &terminalTool); err != nil {
		t.Fatal(err)
	}
	terminal := terminalTool.Terminals["term-1"]
	if terminal.Output != "early and late" || terminal.ExitStatus == nil || terminal.ExitStatus.ExitCode == nil || *terminal.ExitStatus.ExitCode != 1 {
		t.Fatalf("terminal stream or exit status lost: %#v", terminal)
	}
}
