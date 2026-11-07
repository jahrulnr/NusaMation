// Package testutil holds helpers shared by unit tests across packages.
package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// RequirePosixShell skips the test on Windows, where a POSIX shell is not
// guaranteed to exist. Use it for tests that spawn sh-style scripts; the
// process plumbing they exercise is covered on Windows by the fake-acp
// integration tests.
func RequirePosixShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("spawns a POSIX shell script")
	}
}

var (
	fakeACPBuild sync.Once
	fakeACPPath  string
	fakeACPErr   error
)

// BuildFakeACP compiles ./cmd/fake-acp-agent for the host platform and returns
// the path to the binary (with the .exe suffix on Windows, which Go adds when
// building). The binary is built once and shared by all tests.
func BuildFakeACP(t *testing.T) string {
	t.Helper()
	fakeACPBuild.Do(func() {
		dir, err := os.MkdirTemp("", "fake-acp-")
		if err != nil {
			fakeACPErr = err
			return
		}
		// Tests run with the package directory as CWD, so "../.." is the
		// repo root for any internal/ package using this helper.
		repo, err := filepath.Abs("../..")
		if err != nil {
			fakeACPErr = err
			return
		}
		name := "fake-acp"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		bin := filepath.Join(dir, name)
		build := exec.Command("go", "build", "-o", bin, "./cmd/fake-acp-agent")
		build.Dir = repo
		if out, err := build.CombinedOutput(); err != nil {
			fakeACPErr = fmt.Errorf("build fake agent: %v\n%s", err, out)
			return
		}
		fakeACPPath = bin
	})
	if fakeACPErr != nil {
		t.Fatal(fakeACPErr)
	}
	return fakeACPPath
}
