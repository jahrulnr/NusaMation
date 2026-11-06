package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusamation/internal/security"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := security.LoadOrCreateSealer(filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func testAgent(t *testing.T, st *Store) Agent {
	t.Helper()
	enabled := true
	ag, err := st.CreateAgent(context.Background(), AgentInput{
		Name: "Test Agent", Slug: "test-agent", Command: "/bin/echo", Cwd: t.TempDir(),
		Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ag
}

func TestAgentSecretsAtRest(t *testing.T) {
	st := testStore(t)
	enabled := true
	ag, err := st.CreateAgent(context.Background(), AgentInput{
		Name: "Secret Agent", Slug: "secret-agent", Command: "/bin/echo", Cwd: t.TempDir(),
		Enabled: &enabled, Env: SecretList{{Name: "TOKEN", Value: "super-secret-value"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAgent(context.Background(), ag.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Env) != 1 || got.Env[0].Value != "super-secret-value" {
		t.Fatalf("decrypted env = %#v", got.Env)
	}
	var enc string
	if err := st.DB().QueryRow(`SELECT env_enc FROM agents WHERE id=?`, ag.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if enc == "" || contains(enc, "super-secret-value") {
		t.Fatalf("secret was stored in plaintext: %q", enc)
	}
	pub := got.Public()
	if len(pub.Env) != 1 || pub.Env[0].Value != "" || !pub.Env[0].HasValue {
		t.Fatalf("public env = %#v", pub.Env)
	}
}

func TestMessagesTurnsAndApprovals(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	ag := testAgent(t, st)
	conv, err := st.CreateConversation(ctx, ag.ID, "Test", ag.Cwd, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	user := Message{ConversationID: conv.ID, Role: "user", Kind: "text", Text: "hello", Status: "complete"}
	if err := st.AppendMessage(ctx, &user); err != nil {
		t.Fatal(err)
	}
	assistant := Message{ConversationID: conv.ID, Role: "assistant", Kind: "text", Text: "hi", Status: "complete"}
	if err := st.AppendMessage(ctx, &assistant); err != nil {
		t.Fatal(err)
	}
	if user.Seq >= assistant.Seq {
		t.Fatalf("sequence not increasing: %d, %d", user.Seq, assistant.Seq)
	}
	msgs, err := st.ListMessages(ctx, conv.ID, 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Text != "hello" || msgs[1].Text != "hi" {
		t.Fatalf("messages = %#v", msgs)
	}

	turn, err := st.CreateTurn(ctx, conv.ID, user.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if started, err := st.StartTurn(ctx, turn.ID); err != nil || !started {
		t.Fatalf("StartTurn = %v, %v", started, err)
	}
	active, err := st.ActiveTurn(ctx, conv.ID)
	if err != nil || active.ID != turn.ID {
		t.Fatalf("active turn = %#v, %v", active, err)
	}
	if err := st.FinishTurn(ctx, turn.ID, "completed", "end_turn", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActiveTurn(ctx, conv.ID); !IsNotFound(err) {
		t.Fatalf("expected no active turn, got %v", err)
	}

	ap := Approval{
		ConversationID: conv.ID, TurnID: turn.ID, ToolCallID: "tool-1", Title: "Read file",
		Kind: "read", Options: []PermissionOption{{OptionID: "allow", Name: "Allow", Kind: "allow_once"}},
	}
	if err := st.CreateApproval(ctx, &ap); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListApprovalsForConversation(ctx, conv.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != ap.ID {
		t.Fatalf("conversation approvals = %#v, err=%v", list, err)
	}
	pending, err := st.PendingApprovalsForTurn(ctx, turn.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("turn approvals = %#v, err=%v", pending, err)
	}
	got, err := st.ResolveApproval(ctx, ap.ID, "allow", "approved")
	if err != nil || got.Status != "approved" {
		t.Fatalf("resolve = %#v, %v", got, err)
	}
	_, approved, denied, err := st.ApprovalCounts(ctx)
	if err != nil || approved != 1 || denied != 0 {
		t.Fatalf("counts = %d/%d, %v", approved, denied, err)
	}
}

func TestChannelLinkAndInboundDedupe(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	ag := testAgent(t, st)
	conv, err := st.CreateConversation(ctx, ag.ID, "Chat", ag.Cwd, "whatsapp", "wa-ref", "")
	if err != nil {
		t.Fatal(err)
	}
	link, err := st.AddChannelLink(ctx, ChannelLink{
		ConversationID: conv.ID, Channel: "whatsapp", ExternalID: "123@s.whatsapp.net",
		DisplayName: "Tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	found, err := st.FindConversationByChannel(ctx, "whatsapp", "123@s.whatsapp.net", "")
	if err != nil || found.ID != conv.ID {
		t.Fatalf("find = %#v, %v", found, err)
	}
	fresh, err := st.ClaimInbound(ctx, "whatsapp", "msg-1", "", conv.ID)
	if err != nil || !fresh {
		t.Fatalf("first claim = %v, %v", fresh, err)
	}
	fresh, err = st.ClaimInbound(ctx, "whatsapp", "msg-1", "", conv.ID)
	if err != nil || fresh {
		t.Fatalf("duplicate claim = %v, %v", fresh, err)
	}
	if err := st.RemoveChannelLink(ctx, conv.ID, "whatsapp", "123@s.whatsapp.net", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FindConversationByChannel(ctx, "whatsapp", "123@s.whatsapp.net", ""); !IsNotFound(err) {
		t.Fatalf("link was not removed: %v", err)
	}
	_ = link
}

func TestMigrateAppliesVersionedUpgrades(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	latest := migrations[len(migrations)-1].version
	var version int
	if err := st.DB().QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != latest {
		t.Fatalf("user_version = %d, want %d", version, latest)
	}
	// Simulate a v1 install: drop the v2 index and rewind the version.
	if _, err := st.DB().Exec(`DROP INDEX idx_uploads_message_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.DB().QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != latest {
		t.Fatalf("user_version after upgrade = %d, want %d", version, latest)
	}
	for _, name := range []string{"idx_uploads_message_id", "idx_uploads_conversation_id"} {
		var got string
		err := st.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&got)
		if err != nil || got != name {
			t.Fatalf("index %s missing after upgrade: %v", name, err)
		}
	}
}

func TestSettingsKV(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, "k.b", "2"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "k.a", "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "k.b", "3"); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Key != "k.a" || list[1].Key != "k.b" || list[1].Value != "3" {
		t.Fatalf("ListSettings = %#v", list)
	}
	if err := st.DeleteSetting(ctx, "k.a"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSetting(ctx, "k.a"); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := st.DeleteSetting(ctx, "k.a"); !IsNotFound(err) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMCPMigrationPreservesConflictingConfigsAndSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	secret, err := st.sealSecrets(SecretList{{Name: "TOKEN", Value: "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB().Exec(`DROP TABLE mcp_servers;
		CREATE TABLE mcp_servers (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, name TEXT NOT NULL,
		transport TEXT NOT NULL, command TEXT NOT NULL, args_json TEXT NOT NULL, env_enc TEXT NOT NULL,
		url TEXT NOT NULL, headers_enc TEXT NOT NULL, enabled INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
		PRAGMA user_version = 2;`)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"mcp-one", "mcp-two"} {
		_, err = st.DB().Exec(`INSERT INTO mcp_servers VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, id, "agent", "tools", "stdio", id, "[]", secret, "", "", 1, i, i)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetSealer(sealer)
	list, err := st.ListMCP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "tools" || list[1].Name != "tools [mcp-two]" || list[0].Command != "mcp-one" || list[1].Command != "mcp-two" || len(list[1].Env) != 1 || list[1].Env[0].Value != "secret" {
		t.Fatalf("migrated MCPs = %#v", list)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
