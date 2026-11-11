package chat

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReferenceGitIgnore(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is unavailable")
	}
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "empty-config"))
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	write(".gitignore", "node_modules/\n*.tmp\n!important.tmp\ntracked-dir/\n")
	write("node_modules/big/pkg.js", "ignored")
	write("plain.tmp", "ignored")
	write("important.tmp", "included")
	write("tracked-dir/tracked.tmp", "tracked")
	write("nested/.gitignore", "*.log\n")
	write("nested/debug.log", "ignored")
	write("nested/keep.txt", "included")
	run("add", "-f", "tracked-dir/tracked.tmp")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	g := openReferenceGitIgnore(ctx, root)
	if g == nil {
		t.Fatal("could not open Git ignore matcher")
	}
	defer g.close()
	for _, tc := range []struct {
		path         string
		dir, ignored bool
	}{
		{"node_modules", true, true}, {"plain.tmp", false, true}, {"important.tmp", false, false},
		{"nested/debug.log", false, true}, {"nested/keep.txt", false, false},
		{"tracked-dir", true, false}, {"tracked-dir/tracked.tmp", false, false},
	} {
		got, err := g.ignored(filepath.FromSlash(tc.path), tc.dir)
		if err != nil || got != tc.ignored {
			t.Errorf("%s: ignored=%v err=%v, want %v", tc.path, got, err, tc.ignored)
		}
	}
}

func TestReferenceSearchSkipsIgnoredTreeBeforeLimit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is unavailable")
	}
	svc, _, _, ag := newProjectEnv(t, "")
	root := t.TempDir()
	if out, err := exec.Command(git, "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("aaa-generated/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ignored := filepath.Join(root, "aaa-generated")
	if err := os.MkdirAll(ignored, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if err := os.WriteFile(filepath.Join(ignored, fmt.Sprintf("junk-%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "z-target.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	project, err := svc.CreateProject(context.Background(), "ignore fixture", []string{root}, true)
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := svc.ReferenceCandidates(context.Background(), "", project.ID, ag.ID, "target")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.Name == "z-target.txt" && item.Kind == "file" {
			found = true
		}
	}
	if !found {
		t.Fatalf("target missing: %+v", items)
	}
	ignoredItems, _, err := svc.ReferenceCandidates(context.Background(), "", project.ID, ag.ID, "junk-")
	if err != nil {
		t.Fatal(err)
	}
	if len(ignoredItems) != 0 {
		t.Fatalf("ignored files appeared: %+v", ignoredItems)
	}
	ignoredItems, _, err = svc.ReferenceCandidates(context.Background(), "", project.ID, ag.ID, "aaa-generated")
	if err != nil {
		t.Fatal(err)
	}
	if len(ignoredItems) != 0 {
		t.Fatalf("ignored folder appeared: %+v", ignoredItems)
	}
}
