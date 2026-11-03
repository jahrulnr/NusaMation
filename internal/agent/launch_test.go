package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCodexACPUsesAdapter(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"codex", "codex-acp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir())
	codex := filepath.Join(dir, "codex")
	command, args, env, err := resolveACPLaunch(codex, []string{"acp"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(command) != "codex-acp" || len(args) != 0 {
		t.Fatalf("launch = %s %v", command, args)
	}
	if env["CODEX_PATH"] != codex {
		t.Fatalf("CODEX_PATH = %q", env["CODEX_PATH"])
	}
}

func TestResolveCodexACPKeepsRealSubcommand(t *testing.T) {
	dir := t.TempDir()
	codex := filepath.Join(dir, "codex")
	script := "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then printf '  acp               Agent Client Protocol\\n'; fi\n"
	if err := os.WriteFile(codex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	command, args, env, err := resolveACPLaunch(codex, []string{"acp"})
	if err != nil {
		t.Fatal(err)
	}
	if command != codex || len(args) != 1 || args[0] != "acp" || len(env) != 0 {
		t.Fatalf("launch = %s %v env=%v", command, args, env)
	}
}

func TestResolveCodexACPReportsMissingAdapter(t *testing.T) {
	dir := t.TempDir()
	codex := filepath.Join(dir, "codex")
	if err := os.WriteFile(codex, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	_, _, _, err := resolveACPLaunch(codex, []string{"acp"})
	if err == nil || !strings.Contains(err.Error(), "codex-acp") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveLeavesOtherAgentsAlone(t *testing.T) {
	command, args, env, err := resolveACPLaunch("/usr/bin/devin", []string{"acp"})
	if err != nil || command != "/usr/bin/devin" || len(args) != 1 || args[0] != "acp" || env != nil {
		t.Fatalf("launch = %s %v env=%v err=%v", command, args, env, err)
	}
}

func writeNode(t *testing.T, home, version string) string {
	t.Helper()
	node := filepath.Join(home, ".nvm", "versions", "node", version, "bin", "node")
	if err := os.MkdirAll(filepath.Dir(node), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(node, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return node
}

func TestNodeInHomeUsesNVMAlias(t *testing.T) {
	home := t.TempDir()
	node := writeNode(t, home, "v1.2.3")
	if err := os.MkdirAll(filepath.Join(home, ".nvm", "alias"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".nvm", "alias", "default"), []byte("v1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := nodeInHome(home); got != node {
		t.Fatalf("node = %s", got)
	}
}

func TestNodeInHomeResolvesPartialAlias(t *testing.T) {
	home := t.TempDir()
	writeNode(t, home, "v20.0.1")
	want := writeNode(t, home, "v26.12.1")
	if err := os.MkdirAll(filepath.Join(home, ".nvm", "alias"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".nvm", "alias", "default"), []byte("26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := nodeInHome(home); got != want {
		t.Fatalf("node = %s, want %s", got, want)
	}
}

func TestRuntimeKeepsStderrWithoutProcess(t *testing.T) {
	r := &Runtime{}
	r.appendStderr(`ERROR: TERM is set to "dumb"`)
	_, pid, _, logs := r.Info()
	if pid != 0 || len(logs) != 1 || !strings.Contains(logs[0], "TERM is set") {
		t.Fatalf("pid=%d logs=%#v", pid, logs)
	}
}
