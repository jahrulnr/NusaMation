package agent

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

// A session created via session/new that never ran a prompt is deleted on
// drop — not merely closed — so throwaway sessions (agent switches with no
// message sent) do not linger as empty entries in the agent's history. Once
// a prompt ran, drop falls back to session/close so the session stays
// resumable.
func TestDropSessionDeletesUnusedFreshSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
	st := previewTestStore(t, dir)
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(st, logger)
	defer mgr.StopAll()

	tracePath := filepath.Join(dir, "trace.log")
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "fake-drop", Command: bin, Cwd: dir, Enabled: &enabled,
		Env: store.SecretList{{Name: "FAKE_ACP_TRACE", Value: tracePath}},
	})
	if err != nil {
		t.Fatal(err)
	}

	convUnused, err := st.CreateConversation(ctx, ag.ID, "unused", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.EnsureSession(ctx, EnsureRequest{AgentID: ag.ID, ConversationID: convUnused.ID, Cwd: dir}); err != nil {
		t.Fatalf("ensure unused: %v", err)
	}
	mgr.ResetConversation(ctx, convUnused.ID)

	convUsed, err := st.CreateConversation(ctx, ag.ID, "used", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := mgr.EnsureSession(ctx, EnsureRequest{AgentID: ag.ID, ConversationID: convUsed.ID, Cwd: dir})
	if err != nil {
		t.Fatalf("ensure used: %v", err)
	}
	if _, err := mgr.Prompt(ctx, sess, []acp.ContentBlock{{Type: "text", Text: "hi"}}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	mgr.ResetConversation(ctx, convUsed.ID)

	lines := traceLines(t, tracePath)
	if n := countTrace(lines, "session_delete"); n != 1 {
		t.Fatalf("unused fresh session must be deleted once, session_delete = %d\n%s", n, lines)
	}
	if n := countTrace(lines, "session_close"); n != 1 {
		t.Fatalf("prompted session must be closed once, session_close = %d\n%s", n, lines)
	}
}
