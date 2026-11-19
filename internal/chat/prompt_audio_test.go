package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/store"
)

// Voice notes and other audio always arrive as linked files plus a note that
// tells the agent to read or transcribe them with its own tools — no audio
// content block, uniformly across models.
func TestAudioAttachmentsBecomeLinkedFilesWithNote(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := testLogger()
	mgr := agent.NewManager(st, logger)
	svc := &Service{store: st, agents: mgr, log: logger}
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Fake", Slug: "audio-agent", Command: "true", Cwd: dir, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(context.Background(), ag.ID, "t", dir, "whatsapp", "", "")
	if err != nil {
		t.Fatal(err)
	}
	userMsg := &store.Message{ConversationID: conv.ID, Role: "user", Text: "listen to this"}
	if err := st.AppendMessage(context.Background(), userMsg); err != nil {
		t.Fatal(err)
	}
	turn, err := st.CreateTurn(context.Background(), conv.ID, userMsg.ID, "whatsapp", "")
	if err != nil {
		t.Fatal(err)
	}
	voicePath := filepath.Join(dir, "audio-1.ogg")
	if err := os.WriteFile(voicePath, []byte("fake-ogg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUpload(context.Background(), &store.Upload{
		ConversationID: conv.ID, MessageID: userMsg.ID,
		Name: "audio-1.ogg", MIME: "audio/ogg", Size: 8, Path: voicePath,
	}); err != nil {
		t.Fatal(err)
	}
	agRow, err := st.GetAgent(context.Background(), ag.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := &activeTurn{turn: turn, conv: conv, conversationID: conv.ID, user: *userMsg, agent: agRow}
	blocks := svc.attachmentBlocks(context.Background(), a)

	var linked, note, audioBlock bool
	for _, b := range blocks {
		switch b.Type {
		case "audio":
			audioBlock = true
		case "resource_link":
			if strings.Contains(b.URI, "audio-1.ogg") {
				linked = true
			}
		case "text":
			if strings.Contains(b.Text, "audio message (voice note)") && strings.Contains(b.Text, "transcribe") {
				note = true
			}
		}
	}
	if audioBlock {
		t.Fatal("audio content block is still sent; the decision is attachment + note")
	}
	if !linked || !note {
		t.Fatalf("linked=%v note=%v blocks=%#v", linked, note, blocks)
	}
	// The block list is JSON-safe for the ACP prompt payload.
	if _, err := json.Marshal(blocks); err != nil {
		t.Fatal(err)
	}
}
