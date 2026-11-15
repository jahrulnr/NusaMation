package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/store"
)

func recordingMeta(t *testing.T, rec *store.MessageRecording) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"recording": rec})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The block names the recording, notes its status, embeds the send-time
// summary and points the agent at the read tools.
func TestRecordingBlockCarriesSummary(t *testing.T) {
	out := recordingBlock(recordingMeta(t, &store.MessageRecording{
		RecordingID: "rec_abc", Status: "completed",
		Summary: "Recording rec_abc\nEnv: headless\nEvents: 42",
	}))
	for _, want := range []string{
		"<recording-context>", "rec_abc", "(status: completed)",
		"Env: headless", "browser_get_timeline", "browser_get_artifact",
		"</recording-context>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("recording block missing %q:\n%s", want, out)
		}
	}
}

// A live/swept recording keeps the context — an explicit note stands in for
// the missing summary.txt instead of failing the send.
func TestRecordingBlockGracefulWithoutSummary(t *testing.T) {
	out := recordingBlock(recordingMeta(t, &store.MessageRecording{
		RecordingID: "rec_live", Status: "recording",
	}))
	if !strings.Contains(out, "rec_live") || !strings.Contains(out, "summary unavailable") {
		t.Fatalf("live recording should still produce context:\n%s", out)
	}
}

// No recording metadata → no block; a present-but-empty id → no block.
func TestRecordingBlockAbsent(t *testing.T) {
	if got := recordingBlock(nil); got != "" {
		t.Fatalf("nil metadata block = %q", got)
	}
	if got := recordingBlock(recordingMeta(t, nil)); got != "" {
		t.Fatalf("null recording block = %q", got)
	}
	if got := recordingBlock(recordingMeta(t, &store.MessageRecording{})); got != "" {
		t.Fatalf("empty-id recording block = %q", got)
	}
	if got := recordingBlock(annotationMeta(t, store.MessageAnnotation{PickID: "pk"})); got != "" {
		t.Fatalf("annotation-only metadata block = %q", got)
	}
}

// A hand-edited metadata blob cannot flood the prompt — summaries are ~60
// lines by construction; the block caps at 80.
func TestRecordingBlockCapsSummary(t *testing.T) {
	var lines []string
	for range 200 {
		lines = append(lines, "step")
	}
	out := recordingBlock(recordingMeta(t, &store.MessageRecording{
		RecordingID: "rec_x", Summary: strings.Join(lines, "\n"),
	}))
	if !strings.Contains(out, "(truncated)") {
		t.Fatalf("oversized summary not capped:\n%s", out)
	}
	if n := strings.Count(out, "\nstep"); n != 80 {
		t.Fatalf("kept %d summary lines, want 80", n)
	}
}

// Submit persists the resolved recording into message metadata — the same
// path annotations take.
func TestSubmitStoresRecording(t *testing.T) {
	e := newAfterToolsEnv(t)
	msg, _, err := e.svc.Submit(context.Background(), SubmitInput{
		ConversationID: e.conv.ID, Text: "watch this", Channel: "web",
		Recording: &store.RecordingInput{
			ID: "rec_1", Status: "completed", Summary: "Recording rec_1",
		},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	var meta struct {
		Recording store.MessageRecording `json:"recording"`
	}
	if err := json.Unmarshal(msg.Metadata, &meta); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if meta.Recording.RecordingID != "rec_1" || meta.Recording.Status != "completed" || meta.Recording.Summary == "" {
		t.Fatalf("metadata.recording = %+v", meta.Recording)
	}
	if block := recordingBlock(msg.Metadata); !strings.Contains(block, "rec_1") {
		t.Fatalf("recording block not derivable from stored metadata:\n%s", block)
	}
}

// A recording alone satisfies the prompt guard — sending "here is a
// recording" needs no text or attachments.
func TestSubmitRecordingOnly(t *testing.T) {
	e := newAfterToolsEnv(t)
	if _, _, err := e.svc.Submit(context.Background(), SubmitInput{
		ConversationID: e.conv.ID, Channel: "web",
		Recording: &store.RecordingInput{ID: "rec_1"},
	}); err != nil {
		t.Fatalf("recording-only Submit: %v", err)
	}
}
