package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

func newProjectEnv(t *testing.T, command string) (*Service, *store.Store, *agent.Manager, store.Agent) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := agent.NewManager(st, logger)
	t.Cleanup(mgr.StopAll)
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{
		Store: st, Agents: mgr, Log: logger,
		UploadDir: filepath.Join(dir, "uploads"), Workspace: workspace,
	})
	mgr.SetSink(svc)
	if command == "" {
		command = "echo"
	}
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Project Agent", Slug: "project-agent", Command: command, Cwd: workspace, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, mgr, ag
}

func sameResolved(t *testing.T, got, want string) {
	t.Helper()
	g, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("eval %q: %v", got, err)
	}
	w, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("eval %q: %v", want, err)
	}
	if g != w {
		t.Fatalf("got %s want %s", g, w)
	}
}

func sameResolvedList(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len %d != %d (%#v vs %#v)", len(got), len(want), got, want)
	}
	for i := range got {
		sameResolved(t, got[i], want[i])
	}
}

func metadataDirs(t *testing.T, conv store.Conversation) []string {
	t.Helper()
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(conv.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	raw, ok := meta["additionalDirectories"]
	if !ok {
		t.Fatalf("additionalDirectories missing: %s", conv.Metadata)
	}
	var dirs []string
	if err := json.Unmarshal(raw, &dirs); err != nil {
		t.Fatal(err)
	}
	if dirs == nil {
		t.Fatal("additionalDirectories is null")
	}
	return dirs
}

func TestProjectFoldersFollowPrimary(t *testing.T) {
	svc, st, _, ag := newProjectEnv(t, "")
	ctx := context.Background()
	primary := t.TempDir()
	extra := t.TempDir()
	project, err := svc.CreateProject(ctx, "", []string{primary, extra}, true)
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != filepath.Base(project.RootPaths[0]) {
		t.Fatalf("name = %q", project.Name)
	}
	sameResolvedList(t, project.RootPaths, []string{primary, extra})

	conv, err := svc.CreateProjectConversation(ctx, ag.ID, "in project", "web", "", project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ProjectID != project.ID {
		t.Fatalf("projectId = %q", conv.ProjectID)
	}
	sameResolved(t, conv.Cwd, primary)
	sameResolvedList(t, metadataDirs(t, conv), []string{extra})
	if _, err := svc.patchMeta(ctx, conv.ID, map[string]any{"titleSource": "user"}); err != nil {
		t.Fatal(err)
	}

	moved, err := svc.UpdateProject(ctx, project.ID, ProjectPatch{
		Sources: []string{extra, primary}, SourcesSet: true,
		UpdatedAt: project.UpdatedAt, UpdatedAtSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sameResolvedList(t, moved.RootPaths, []string{extra, primary})
	got, err := st.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	sameResolved(t, got.Cwd, extra)
	sameResolvedList(t, metadataDirs(t, got), []string{primary})
	var meta map[string]any
	if err := json.Unmarshal(got.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["titleSource"] != "user" {
		t.Fatalf("metadata = %s", got.Metadata)
	}
}

func TestProjectPathRulesAndStaleRevision(t *testing.T) {
	svc, st, _, ag := newProjectEnv(t, "")
	ctx := context.Background()
	primary := t.TempDir()
	extra := t.TempDir()
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CreateProject(ctx, "Nope", nil, true); err == nil {
		t.Fatal("empty sources were accepted")
	}
	if _, err := svc.CreateProject(ctx, "Nope", []string{file}, true); err == nil {
		t.Fatal("file was accepted")
	}
	if projects, err := st.ListProjects(ctx); err != nil || len(projects) != 0 {
		t.Fatalf("projects = %#v, %v", projects, err)
	}

	var declined *ConfirmationRequiredError
	if _, err := svc.CreateProject(ctx, "Nope", []string{primary}, false); !errors.As(err, &declined) || len(declined.Added) != 1 {
		t.Fatalf("confirm = %#v, %v", declined, err)
	}
	if projects, err := st.ListProjects(ctx); err != nil || len(projects) != 0 {
		t.Fatalf("declined create wrote %#v, %v", projects, err)
	}

	project, err := svc.CreateProject(ctx, "Dup", []string{primary, primary}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.RootPaths) != 1 {
		t.Fatalf("roots = %#v", project.RootPaths)
	}
	sameResolved(t, project.RootPaths[0], primary)

	conv, err := svc.CreateProjectConversation(ctx, ag.ID, "member", "web", "", project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var invalid *InvalidError
	if _, err := svc.UpdateProject(ctx, project.ID, ProjectPatch{
		Sources: []string{extra, primary}, SourcesSet: true, FoldersConfirmed: true,
	}); !errors.As(err, &invalid) {
		t.Fatalf("missing updatedAt = %v", err)
	}
	if _, err := svc.UpdateProject(ctx, project.ID, ProjectPatch{
		Sources: []string{extra}, SourcesSet: true,
		UpdatedAt: project.UpdatedAt - 1, UpdatedAtSet: true, FoldersConfirmed: true,
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale updatedAt = %v", err)
	}
	fresh, err := st.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	sameResolvedList(t, fresh.RootPaths, []string{primary})
	if fresh.UpdatedAt != project.UpdatedAt {
		t.Fatalf("updatedAt changed to %d", fresh.UpdatedAt)
	}
	got, err := st.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	sameResolved(t, got.Cwd, primary)
	sameResolvedList(t, metadataDirs(t, got), []string{})

	if _, err := svc.UpdateProject(ctx, project.ID, ProjectPatch{
		Sources: []string{primary, extra}, SourcesSet: true,
		UpdatedAt: project.UpdatedAt, UpdatedAtSet: true,
	}); !errors.As(err, &declined) || len(declined.Added) != 1 {
		t.Fatalf("new folder confirm = %#v, %v", declined, err)
	}
	fresh, err = st.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	sameResolvedList(t, fresh.RootPaths, []string{primary})
	got, err = st.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	sameResolved(t, got.Cwd, primary)
}

func TestProjectDeleteRestoreAndAdoption(t *testing.T) {
	svc, st, _, ag := newProjectEnv(t, "")
	ctx := context.Background()
	primary := t.TempDir()
	otherDir := t.TempDir()
	old, err := svc.CreateConversation(ctx, ag.ID, "old", primary, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	project, err := svc.CreateProject(ctx, "Local", []string{primary}, true)
	if err != nil {
		t.Fatal(err)
	}
	old, err = st.GetConversation(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.ProjectID != "" {
		t.Fatalf("existing chat was adopted: %#v", old)
	}
	sameResolved(t, old.Cwd, primary)

	member, err := svc.CreateProjectConversation(ctx, ag.ID, "member", "web", "", project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateProject(ctx, project.ID, ProjectPatch{Pinned: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	snap, ids, err := svc.RemoveProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(primary); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetProject(ctx, project.ID); !store.IsNotFound(err) {
		t.Fatalf("project still present: %v", err)
	}
	member, err = st.GetConversation(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if member.ProjectID != "" {
		t.Fatalf("projectId = %q", member.ProjectID)
	}
	sameResolved(t, member.Cwd, primary)
	if len(ids) != 1 || ids[0] != member.ID {
		t.Fatalf("member ids = %#v", ids)
	}

	other, err := svc.CreateProject(ctx, "Other", []string{otherDir}, true)
	if err != nil {
		t.Fatal(err)
	}
	taken, err := svc.CreateProjectConversation(ctx, ag.ID, "taken", "web", "", other.ID)
	if err != nil {
		t.Fatal(err)
	}
	restored, reattached, err := svc.RestoreProject(ctx, snap.ID, snap.Name, snap.RootPaths, snap.Pinned, []string{member.ID, taken.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Pinned {
		t.Fatal("restore dropped the pin")
	}
	if len(reattached) != 1 || reattached[0] != member.ID {
		t.Fatalf("reattached = %#v", reattached)
	}
	member, err = st.GetConversation(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if member.ProjectID != snap.ID {
		t.Fatalf("restored projectId = %q", member.ProjectID)
	}
	taken, err = st.GetConversation(ctx, taken.ID)
	if err != nil {
		t.Fatal(err)
	}
	if taken.ProjectID != other.ID {
		t.Fatalf("stolen projectId = %q", taken.ProjectID)
	}
	if _, _, err := svc.RestoreProject(ctx, snap.ID, snap.Name, snap.RootPaths, false, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second restore = %v", err)
	}
}

func TestProjectPinLeavesFoldersAlone(t *testing.T) {
	svc, st, _, _ := newProjectEnv(t, "")
	ctx := context.Background()
	dir := t.TempDir()
	project, err := svc.CreateProject(ctx, "Grouped", []string{dir}, true)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := svc.UpdateProject(ctx, project.ID, ProjectPatch{Pinned: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if !pinned.Pinned {
		t.Fatalf("pinned = %#v", pinned)
	}
	if pinned.UpdatedAt != project.UpdatedAt {
		t.Fatalf("place changed updatedAt from %d to %d", project.UpdatedAt, pinned.UpdatedAt)
	}
	got, err := st.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	sameResolvedList(t, got.RootPaths, []string{dir})
}

func TestProjectRootResetWaitsForTheRunningTurn(t *testing.T) {
	bin := testutil.BuildFakeACP(t)
	svc, st, mgr, ag := newProjectEnv(t, bin)
	ctx := context.Background()
	primary := t.TempDir()
	extra := t.TempDir()
	project, err := svc.CreateProject(ctx, "Live", []string{primary, extra}, true)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := svc.CreateProjectConversation(ctx, ag.ID, "live", "web", "", project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PrepareSession(ctx, conv.ID); err != nil {
		t.Fatal(err)
	}
	if mgr.Session(conv.ID) == nil {
		t.Fatal("session was not loaded")
	}
	conv, err = st.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	acpID := conv.ACPSessionID
	if acpID == "" {
		t.Fatal("acp session id was not stored")
	}

	renamed, err := svc.UpdateProject(ctx, project.ID, ProjectPatch{
		Name: strPtr("Renamed"), UpdatedAt: project.UpdatedAt, UpdatedAtSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mgr.Session(conv.ID) == nil {
		t.Fatal("name change dropped the session")
	}

	moved, err := svc.UpdateProject(ctx, project.ID, ProjectPatch{
		Sources: []string{extra, primary}, SourcesSet: true,
		UpdatedAt: renamed.UpdatedAt, UpdatedAtSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mgr.Session(conv.ID) != nil {
		t.Fatal("idle session was kept after a root change")
	}
	conv, err = st.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ACPSessionID != acpID {
		t.Fatalf("acp session id = %q", conv.ACPSessionID)
	}
	sameResolved(t, conv.Cwd, extra)

	if err := svc.PrepareSession(ctx, conv.ID); err != nil {
		t.Fatal(err)
	}
	live := mgr.Session(conv.ID)
	if live == nil {
		t.Fatal("session did not load again")
	}
	sameResolved(t, live.Cwd(), extra)
	turn := &activeTurn{conversationID: conv.ID}
	svc.setActive(turn)
	if _, err := svc.UpdateProject(ctx, project.ID, ProjectPatch{
		Sources: []string{primary, extra}, SourcesSet: true,
		UpdatedAt: moved.UpdatedAt, UpdatedAtSet: true,
	}); err != nil {
		t.Fatal(err)
	}
	if mgr.Session(conv.ID) != live {
		t.Fatal("running turn dropped the session")
	}
	svc.finishActive(turn)
	if mgr.Session(conv.ID) != nil {
		t.Fatal("session was kept after the turn finished")
	}
	conv, err = st.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ACPSessionID != acpID {
		t.Fatalf("acp session id after turn = %q", conv.ACPSessionID)
	}
	sameResolved(t, conv.Cwd, primary)
}

func TestSwitchAgentKeepsProjectMemberCwd(t *testing.T) {
	svc, st, _, ag := newProjectEnv(t, "")
	ctx := context.Background()
	extra := t.TempDir()
	project, err := svc.CreateProject(ctx, "Atlas", []string{ag.Cwd, extra}, true)
	if err != nil {
		t.Fatal(err)
	}
	member, err := svc.CreateProjectConversation(ctx, ag.ID, "member", "web", "", project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if member.Cwd != ag.Cwd {
		if ag, err = st.UpdateAgent(ctx, ag.ID, store.AgentInput{Cwd: member.Cwd}); err != nil {
			t.Fatal(err)
		}
	}
	if member.Cwd != ag.Cwd {
		t.Fatalf("precondition cwd %q != agent cwd %q", member.Cwd, ag.Cwd)
	}
	dirs := metadataDirs(t, member)
	enabled := true
	otherDir := t.TempDir()
	other, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Other", Slug: "other-agent", Command: "echo", Cwd: otherDir, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	switched, err := svc.SwitchAgent(ctx, member.ID, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if switched.Cwd != member.Cwd {
		t.Fatalf("project cwd = %q, want %q", switched.Cwd, member.Cwd)
	}
	if switched.ProjectID != project.ID {
		t.Fatalf("projectId = %q", switched.ProjectID)
	}
	sameResolvedList(t, metadataDirs(t, switched), dirs)

	same, err := svc.CreateConversation(ctx, ag.ID, "same", ag.Cwd, "web", "same")
	if err != nil {
		t.Fatal(err)
	}
	if same.Cwd != ag.Cwd {
		t.Fatalf("projectless cwd %q != agent cwd %q", same.Cwd, ag.Cwd)
	}
	moved, err := svc.SwitchAgent(ctx, same.ID, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Cwd != other.Cwd {
		t.Fatalf("projectless cwd = %q, want %q", moved.Cwd, other.Cwd)
	}

	blank, err := svc.CreateConversation(ctx, ag.ID, "blank", ag.Cwd, "web", "blank")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetConversationCwd(ctx, blank.ID, ""); err != nil {
		t.Fatal(err)
	}
	filled, err := svc.SwitchAgent(ctx, blank.ID, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if filled.Cwd != other.Cwd {
		t.Fatalf("empty cwd = %q, want %q", filled.Cwd, other.Cwd)
	}

	custom := t.TempDir()
	kept, err := svc.CreateConversation(ctx, ag.ID, "custom", custom, "web", "custom")
	if err != nil {
		t.Fatal(err)
	}
	stayed, err := svc.SwitchAgent(ctx, kept.ID, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stayed.Cwd != kept.Cwd {
		t.Fatalf("custom cwd = %q, want %q", stayed.Cwd, kept.Cwd)
	}
}

func TestTurnStartWaitsUntilRootResetFinishes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := New(Options{})
		type drop struct {
			id     string
			active bool
		}
		var drops []drop
		record := func(id string) drop {
			svc.mu.Lock()
			active := svc.active[id] != nil
			svc.mu.Unlock()
			seen := drop{id: id, active: active}
			drops = append(drops, seen)
			return seen
		}

		idleID := "idle"
		entered := make(chan struct{})
		release := make(chan struct{})
		svc.resetConversation = func(_ context.Context, id string) {
			record(id)
			close(entered)
			<-release
		}
		go svc.markRootReset(context.Background(), []string{idleID})
		<-entered
		turn := &activeTurn{conversationID: idleID}
		started := make(chan struct{})
		go func() {
			svc.setActive(turn)
			close(started)
		}()
		synctest.Wait()
		if svc.activeFor(idleID) != nil {
			t.Fatal("turn was active while the idle drop was running")
		}
		select {
		case <-started:
			t.Fatal("setActive returned during the idle drop")
		default:
		}
		close(release)
		<-started
		if svc.activeFor(idleID) != turn {
			t.Fatal("turn was not active after the idle drop")
		}
		if len(drops) != 1 || drops[0].id != idleID || drops[0].active {
			t.Fatalf("idle drops = %+v", drops)
		}
		svc.finishActive(turn)
		if len(drops) != 1 {
			t.Fatalf("turn was reset after it became active: %+v", drops)
		}

		runningID := "running"
		running := &activeTurn{conversationID: runningID}
		svc.setActive(running)
		entered = make(chan struct{})
		release = make(chan struct{})
		svc.resetConversation = func(_ context.Context, id string) {
			record(id)
			close(entered)
			<-release
		}
		svc.markRootReset(context.Background(), []string{runningID})
		if len(drops) != 1 {
			t.Fatalf("running turn was dropped immediately: %+v", drops)
		}
		if svc.activeFor(runningID) != running {
			t.Fatal("running turn was cleared before it finished")
		}
		go svc.finishActive(running)
		<-entered
		next := &activeTurn{conversationID: runningID}
		nextStarted := make(chan struct{})
		go func() {
			svc.setActive(next)
			close(nextStarted)
		}()
		synctest.Wait()
		if svc.activeFor(runningID) != nil {
			t.Fatal("next turn was active while the deferred drop was running")
		}
		close(release)
		<-nextStarted
		if svc.activeFor(runningID) != next {
			t.Fatal("next turn was not active after the deferred drop")
		}
		if len(drops) != 2 || drops[1].id != runningID || drops[1].active {
			t.Fatalf("drops = %+v", drops)
		}
		svc.finishActive(next)
		if len(drops) != 2 {
			t.Fatalf("next turn was reset after it became active: %+v", drops)
		}
	})
}

func TestRootResetWaitsForSessionPrepare(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := New(Options{})
		var drops []string
		release := make(chan struct{})
		svc.resetConversation = func(_ context.Context, id string) {
			drops = append(drops, id)
			<-release
		}

		// A change landing while a prepare is open defers the drop until it ends.
		preparing := svc.beginPrepare("cv1")
		svc.markRootReset(context.Background(), []string{"cv1"})
		if len(drops) != 0 {
			t.Fatalf("reset ran while a session prepare was open: %v", drops)
		}
		go preparing()
		synctest.Wait()
		if len(drops) != 1 || drops[0] != "cv1" {
			t.Fatalf("deferred drop = %v", drops)
		}
		// A second prepare waits for the in-flight drop to finish.
		started := make(chan struct{})
		go func() {
			done := svc.beginPrepare("cv1")
			close(started)
			done()
		}()
		synctest.Wait()
		select {
		case <-started:
			t.Fatal("beginPrepare returned during the idle drop")
		default:
		}
		close(release)
		<-started
		synctest.Wait()

		// A turn that starts while a reset is pending owns the deferred drop.
		release = make(chan struct{})
		done := svc.beginPrepare("cv2")
		turn := &activeTurn{conversationID: "cv2"}
		svc.setActive(turn)
		svc.markRootReset(context.Background(), []string{"cv2"})
		done()
		synctest.Wait()
		if len(drops) != 1 {
			t.Fatalf("drop ran while a turn owned the pending reset: %v", drops)
		}
		go svc.finishActive(turn)
		synctest.Wait()
		if len(drops) != 2 || drops[1] != "cv2" {
			t.Fatalf("finishActive drop = %v", drops)
		}
		close(release)
	})
}

func boolPtr(v bool) *bool { return &v }

func strPtr(v string) *string { return &v }
