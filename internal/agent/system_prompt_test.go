package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

func TestSessionInstructionsLifecycle(t *testing.T) {
	bin := testutil.BuildFakeACP(t)
	for _, tc := range []struct {
		name           string
		native, resume bool
	}{{"fallback-load", false, false}, {"native-load", true, false}, {"native-resume", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			trace := filepath.Join(dir, "trace")
			st := previewTestStore(t, dir)
			defer st.Close()
			mgr := NewManager(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
			defer mgr.StopAll()
			env := store.SecretList{{Name: "FAKE_ACP_TRACE", Value: trace}, {Name: "FAKE_ACP_TRACE_SESSION_PROMPT", Value: "1"}}
			if tc.native {
				env = append(env, store.EnvVar{Name: "FAKE_ACP_SYSTEM_PROMPT", Value: "1"})
			}
			if tc.resume {
				env = append(env, store.EnvVar{Name: "FAKE_ACP_RESUME", Value: "1"})
			}
			enabled := true
			ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "Fake", Slug: "fake-prompt", Command: bin, Cwd: dir, Enabled: &enabled, Env: env})
			if err != nil {
				t.Fatal(err)
			}
			conv, err := st.CreateConversation(ctx, ag.ID, "room", dir, "web", "", "")
			if err != nil {
				t.Fatal(err)
			}
			request := EnsureRequest{AgentID: ag.ID, ConversationID: conv.ID, Cwd: dir}
			session, err := mgr.EnsureSession(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"/compact", "first input", "second input"} {
				if _, err = mgr.Prompt(ctx, session, []acp.ContentBlock{{Type: "text", Text: text}}); err != nil {
					t.Fatal(err)
				}
			}
			request.ACPSessionID = session.ACPID()
			mgr.ResetConversation(ctx, conv.ID)
			session, err = mgr.EnsureSession(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = mgr.Prompt(ctx, session, []acp.ContentBlock{{Type: "text", Text: "restored input"}}); err != nil {
				t.Fatal(err)
			}
			var prompts []string
			var methods []string
			for _, line := range traceLines(t, trace) {
				if strings.HasPrefix(line, "prompt_start ") {
					prompts = append(prompts, line)
				}
				if !strings.HasPrefix(line, "lifecycle ") {
					continue
				}
				parts := strings.SplitN(line, " ", 3)
				if len(parts) != 3 {
					t.Fatal(line)
				}
				methods = append(methods, parts[1])
				var req struct {
					SystemPrompt *acp.SystemPrompt `json:"systemPrompt"`
				}
				if err := json.Unmarshal([]byte(parts[2]), &req); err != nil {
					t.Fatal(err)
				}
				if tc.native {
					if req.SystemPrompt == nil || req.SystemPrompt.Mode != "append" || len(req.SystemPrompt.Content) != 1 || req.SystemPrompt.Content[0].Text != sessionInstructions {
						t.Fatalf("invalid system prompt: %+v", req.SystemPrompt)
					}
				} else if req.SystemPrompt != nil {
					t.Fatal("sent systemPrompt without capability")
				}
			}
			if len(methods) != 2 || methods[0] != "session/new" || (tc.resume && methods[1] != "session/resume") || (!tc.resume && methods[1] != "session/load") {
				t.Fatalf("lifecycle: %v", methods)
			}
			if len(prompts) != 4 {
				t.Fatalf("prompts: %v", prompts)
			}
			for i, line := range prompts {
				want := !tc.native && (i == 1 || i == 3)
				if strings.Contains(line, "<nusamation-session-instructions>") != want {
					t.Fatalf("prompt %d: %s", i, line)
				}
			}
		})
	}
}

func TestSystemPromptRejectionDoesNotCreateNewSession(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "load", true: "resume"}[resume], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			trace := filepath.Join(dir, "trace")
			st := previewTestStore(t, dir)
			defer st.Close()
			mgr := NewManager(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
			defer mgr.StopAll()
			env := store.SecretList{{Name: "FAKE_ACP_TRACE", Value: trace}, {Name: "FAKE_ACP_SYSTEM_PROMPT", Value: "1"}, {Name: "FAKE_ACP_REJECT_LOAD", Value: "1"}}
			if resume {
				env = append(env, store.EnvVar{Name: "FAKE_ACP_RESUME", Value: "1"})
			}
			enabled := true
			ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "Fake", Slug: "reject-prompt", Command: testutil.BuildFakeACP(t), Cwd: dir, Enabled: &enabled, Env: env})
			if err != nil {
				t.Fatal(err)
			}
			conv, err := st.CreateConversation(ctx, ag.ID, "room", dir, "web", "", "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = mgr.EnsureSession(ctx, EnsureRequest{AgentID: ag.ID, ConversationID: conv.ID, Cwd: dir, ACPSessionID: "existing"})
			if err == nil || !acp.IsInvalidParams(err) {
				t.Fatalf("expected rejection, got %v", err)
			}
			if n := countTrace(traceLines(t, trace), "session_new"); n != 0 {
				t.Fatalf("rejection created %d new sessions", n)
			}
		})
	}
}
