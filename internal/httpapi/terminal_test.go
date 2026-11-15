package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

// The conversation terminal speaks the same WebSocket protocol as the auth
// terminal: binary frames out, {type:input|resize} JSON in, a final
// {type:exit} status. A fake $SHELL marks startup and echoes input back.
func TestConvTerminalWebSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pty/fake shell not portable")
	}
	dir := t.TempDir()
	shell := filepath.Join(dir, "fakeshell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\necho term-ready\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	if _, _, err = auth.EnsureBootstrap(context.Background(), st, "test-password"); err != nil {
		t.Fatal(err)
	}
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{Name: "Term Agent", Slug: "term-agent", Command: "/bin/echo", Cwd: dir, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(dir, "work")
	if err := os.Mkdir(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(context.Background(), ag.ID, "term", workdir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Config: config.Config{DataDir: dir}, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	res, err := client.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"test-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, ts.URL+"/api/conversations/"+conv.ID+"/terminal", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	readUntil := func(want string) {
		t.Helper()
		var buf strings.Builder
		for !strings.Contains(buf.String(), want) {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				t.Fatalf("read: %v (got %q)", err, buf.String())
			}
			if typ == websocket.MessageBinary {
				buf.Write(data)
			}
		}
	}
	readUntil("term-ready")
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"ping\n"}`)); err != nil {
		t.Fatal(err)
	}
	readUntil("ping")

	// A nonexistent cwd is refused before upgrade.
	bad, err := client.Get(ts.URL + "/api/conversations/" + conv.ID + "/terminal?cwd=" + url.QueryEscape(filepath.Join(dir, "nope")))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing cwd: status=%d", bad.StatusCode)
	}

	// A cwd outside every conversation root still connects — the pane can
	// point the shell at any existing directory.
	other, err := os.MkdirTemp("", "other-proj")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(other)
	ws2, resp, err := websocket.Dial(ctx, ts.URL+"/api/conversations/"+conv.ID+"/terminal?cwd="+url.QueryEscape(other), &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatalf("dial outside-root cwd: %v (%v)", err, resp)
	}
	ws2.Close(websocket.StatusNormalClosure, "")

	// Unauthenticated requests never reach the handler.
	anon := &http.Client{}
	res, err = anon.Get(ts.URL + "/api/conversations/" + conv.ID + "/terminal")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth: status=%d", res.StatusCode)
	}
}

// ?cwd= may pick any directory inside the conversation roots, including the
// metadata additionalDirectories set.
func TestConvTerminalCwdAdditionalRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pty/fake shell not portable")
	}
	dir := t.TempDir()
	shell := filepath.Join(dir, "fakeshell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\npwd\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	if _, _, err = auth.EnsureBootstrap(context.Background(), st, "test-password"); err != nil {
		t.Fatal(err)
	}
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{Name: "Term Agent", Slug: "term-agent", Command: "/bin/echo", Cwd: dir, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	extra, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(context.Background(), ag.ID, "term", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	meta := `{"additionalDirectories":[` + strconv.Quote(extra) + `]}`
	if err := st.UpdateConversationMetadata(context.Background(), conv.ID, json.RawMessage(meta)); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Config: config.Config{DataDir: dir}, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	res, err := client.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"test-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, ts.URL+"/api/conversations/"+conv.ID+"/terminal?cwd="+url.QueryEscape(extra), &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatalf("dial extra root: %v (%v)", err, resp)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	deadline := time.Now().Add(5 * time.Second)
	var buf strings.Builder
	for !strings.Contains(buf.String(), extra) && time.Now().Before(deadline) {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, buf.String())
		}
		if typ == websocket.MessageBinary {
			buf.Write(data)
		}
	}
	if !strings.Contains(buf.String(), extra) {
		t.Fatalf("shell did not start in additional root; got %q", buf.String())
	}
}

func TestConvTerminalRoots(t *testing.T) {
	ctx := context.Background()
	s := &Server{cfg: config.Config{DataDir: "/data"}}
	workspace := s.cfg.WorkspaceDir()
	roots := s.convTerminalRoots(ctx, store.Conversation{Cwd: "/proj", Metadata: []byte(`{"additionalDirectories":["/extra"]}`)})
	if len(roots) != 3 || roots[0] != "/proj" || roots[1] != workspace || roots[2] != "/extra" {
		t.Fatalf("roots: %v", roots)
	}
	roots = s.convTerminalRoots(ctx, store.Conversation{})
	if len(roots) != 1 || roots[0] != workspace {
		t.Fatalf("fallback roots: %v", roots)
	}
}

// The roots endpoint lists only directories that exist, deduped and in
// preference order — a dead cwd drops out instead of poisoning the picker.
func TestConvRootsEndpoint(t *testing.T) {
	dir := t.TempDir()
	extra, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(dir, "gone")
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	// The roots endpoint returns EvalSymlinks, which expands Windows 8.3 names.
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	if _, _, err = auth.EnsureBootstrap(context.Background(), st, "test-password"); err != nil {
		t.Fatal(err)
	}
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{Name: "Term Agent", Slug: "term-agent", Command: "/bin/echo", Cwd: dir, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(context.Background(), ag.ID, "term", dead, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	meta := `{"additionalDirectories":[` + strconv.Quote(extra) + `]}`
	if err := st.UpdateConversationMetadata(context.Background(), conv.ID, json.RawMessage(meta)); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Config: config.Config{DataDir: dir}, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	res, err := client.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"test-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	res, err = client.Get(ts.URL + "/api/conversations/" + conv.ID + "/roots")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var body struct {
		Roots []string `json:"roots"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	want := []string{workspace, extra}
	if len(body.Roots) != len(want) {
		t.Fatalf("roots=%v want %v", body.Roots, want)
	}
	for i := range want {
		if body.Roots[i] != want[i] {
			t.Fatalf("roots=%v want %v", body.Roots, want)
		}
	}
}

// A project member's roots are that project's working dirs — not every
// registered project's, and not the shared workspace.
func TestConvRootsProjectMember(t *testing.T) {
	dir := t.TempDir()
	live1, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	live2, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(dir, "gone")
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	if _, _, err = auth.EnsureBootstrap(context.Background(), st, "test-password"); err != nil {
		t.Fatal(err)
	}
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{Name: "Term Agent", Slug: "term-agent", Command: "/bin/echo", Cwd: dir, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(context.Background(), ag.ID, "term", live1, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.RestoreProject(context.Background(), "proj1", "Alpha", []string{live1, live2, dead}, false, []string{conv.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertProject(context.Background(), "Beta", []string{other}); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Config: config.Config{DataDir: dir}, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	res, err := client.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"test-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	res, err = client.Get(ts.URL + "/api/conversations/" + conv.ID + "/roots")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var body struct {
		Roots []string `json:"roots"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	want := []string{live1, live2}
	if len(body.Roots) != len(want) {
		t.Fatalf("roots=%v want %v", body.Roots, want)
	}
	for i := range want {
		if body.Roots[i] != want[i] {
			t.Fatalf("roots=%v want %v", body.Roots, want)
		}
	}
}

func TestListChildDirsWithFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	listed, err := listChildDirs(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	dirs, _ := listed["dirs"].([]dirEntry)
	if len(dirs) != 1 || dirs[0].Name != "sub" {
		t.Fatalf("dirs: %v", dirs)
	}
	files, _ := listed["files"].([]fileEntry)
	if len(files) != 1 || files[0].Name != "note.txt" || files[0].Size != 2 {
		t.Fatalf("files: %v", files)
	}
	listed, err = listChildDirs(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := listed["files"]; ok {
		t.Fatal("files key present without ?files=1")
	}
}

// The draft-room terminal is conversation-less: ?cwd= picks any existing
// directory, unauthenticated requests never reach the handler.
func TestBareTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pty/fake shell not portable")
	}
	dir := t.TempDir()
	shell := filepath.Join(dir, "fakeshell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\npwd\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	if _, _, err = auth.EnsureBootstrap(context.Background(), st, "test-password"); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Config: config.Config{DataDir: dir}, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	res, err := client.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"test-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	workdir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, ts.URL+"/api/terminal?cwd="+url.QueryEscape(workdir), &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(5 * time.Second)
	var buf strings.Builder
	for !strings.Contains(buf.String(), workdir) && time.Now().Before(deadline) {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, buf.String())
		}
		if typ == websocket.MessageBinary {
			buf.Write(data)
		}
	}
	if !strings.Contains(buf.String(), workdir) {
		t.Fatalf("shell did not start in cwd; got %q", buf.String())
	}

	bad, err := client.Get(ts.URL + "/api/terminal?cwd=" + url.QueryEscape(filepath.Join(dir, "nope")))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing cwd: status=%d", bad.StatusCode)
	}
	anon := &http.Client{}
	res, err = anon.Get(ts.URL + "/api/terminal")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth: status=%d", res.StatusCode)
	}
}

// /api/projects/{id}/roots feeds the draft room's pick list — only existing
// directories, in order.
func TestProjectRootsByID(t *testing.T) {
	dir := t.TempDir()
	live1, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	live2, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(dir, "gone")
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	if _, _, err = auth.EnsureBootstrap(context.Background(), st, "test-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertProject(context.Background(), "Alpha", []string{live1, live2, dead}); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Config: config.Config{DataDir: dir}, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	res, err := client.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"test-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	p, err := st.ListProjects(context.Background())
	if err != nil || len(p) != 1 {
		t.Fatalf("projects: %v %v", p, err)
	}
	res, err = client.Get(ts.URL + "/api/projects/" + p[0].ID + "/roots")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var body struct {
		Roots []string `json:"roots"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	want := []string{live1, live2}
	if len(body.Roots) != len(want) {
		t.Fatalf("roots=%v want %v", body.Roots, want)
	}
	for i := range want {
		if body.Roots[i] != want[i] {
			t.Fatalf("roots=%v want %v", body.Roots, want)
		}
	}
	res, err = client.Get(ts.URL + "/api/projects/prj_missing/roots")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing project: status=%d", res.StatusCode)
	}
}

// /api/fs/file serves a local path without a conversation — the draft file
// tab's preview and download path.
func TestFsFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("hello fs"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	if _, _, err = auth.EnsureBootstrap(context.Background(), st, "test-password"); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Config: config.Config{DataDir: dir}, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	res, err := client.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"test-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	res, err = client.Get(ts.URL + "/api/fs/file?path=" + url.QueryEscape(target))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "hello fs" {
		t.Fatalf("status=%d body=%q", res.StatusCode, body)
	}
	res, err = client.Get(ts.URL + "/api/fs/file?path=" + url.QueryEscape(filepath.Join(dir, "nope")))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing file: status=%d", res.StatusCode)
	}
}
