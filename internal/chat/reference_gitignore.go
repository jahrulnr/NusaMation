package chat

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// referenceGitIgnore delegates Git's precedence, nested ignore files and index
// semantics to Git. A single process serves the whole walk, not one per path.
type referenceGitIgnore struct {
	cmd         *exec.Cmd
	in          io.WriteCloser
	out         *bufio.Reader
	tracked     map[string]bool
	trackedDirs map[string]bool
}

func openReferenceGitIgnore(ctx context.Context, root string) *referenceGitIgnore {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil
	}
	list := exec.CommandContext(ctx, git, "-C", root, "ls-files", "--cached", "-z", "--", ".")
	hideReferenceProcessWindow(list)
	data, err := list.Output()
	if err != nil {
		return nil
	} // Outside a worktree or Git is unavailable.
	g := &referenceGitIgnore{tracked: map[string]bool{}, trackedDirs: map[string]bool{}}
	for name := range bytes.SplitSeq(data, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		p := filepath.Clean(filepath.FromSlash(string(name)))
		g.tracked[p] = true
		for dir := filepath.Dir(p); dir != "." && dir != ""; dir = filepath.Dir(dir) {
			g.trackedDirs[dir] = true
		}
	}
	g.cmd = exec.CommandContext(ctx, git, "-C", root, "check-ignore", "--stdin", "-z", "--verbose", "--non-matching")
	hideReferenceProcessWindow(g.cmd)
	g.cmd.Env = append(os.Environ(), "GIT_FLUSH=1")
	g.in, err = g.cmd.StdinPipe()
	if err != nil {
		return nil
	}
	stdout, err := g.cmd.StdoutPipe()
	if err != nil {
		_ = g.in.Close()
		return nil
	}
	g.out = bufio.NewReader(stdout)
	if err := g.cmd.Start(); err != nil {
		_ = g.in.Close()
		return nil
	}
	return g
}

func (g *referenceGitIgnore) ignored(rel string, isDir bool) (bool, error) {
	if g.tracked[rel] {
		return false, nil
	}
	path := filepath.ToSlash(rel)
	if isDir {
		path += "/"
	}
	if _, err := io.WriteString(g.in, path+"\x00"); err != nil {
		return false, err
	}
	var fields [4]string
	for i := range fields {
		value, err := g.out.ReadString(0)
		if err != nil {
			return false, err
		}
		fields[i] = strings.TrimSuffix(value, "\x00")
	}
	ignored := fields[0] != "" && !strings.HasPrefix(fields[2], "!")
	if isDir && g.trackedDirs[rel] {
		return false, nil
	}
	return ignored, nil
}

func (g *referenceGitIgnore) close() {
	_ = g.in.Close()
	_ = g.cmd.Wait()
}
