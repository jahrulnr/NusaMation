package agent

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/store"
)

// The OpenAI Codex CLI has no ACP mode. `codex acp` is forwarded to the
// interactive TUI as a prompt, and this client sets TERM=dumb, so the process
// exits before initialize. codex-acp is the adapter that speaks ACP and drives
// that CLI.
func acpProcessConfig(agent store.Agent, log *slog.Logger) (acp.ProcessConfig, error) {
	command, args, extra, err := resolveACPLaunch(agent.Command, agent.Args)
	if err != nil {
		return acp.ProcessConfig{}, err
	}
	return acp.ProcessConfig{
		Command: command,
		Args:    args,
		Env:     mergeEnv(envMap(agent.Env), extra),
		Dir:     agent.Cwd,
		Log:     log,
	}, nil
}

func resolveACPLaunch(command string, args []string) (string, []string, map[string]string, error) {
	if !isCodexCLI(command, args) || codexHasACPSubcommand(command) {
		return command, args, nil, nil
	}
	adapter, err := exec.LookPath("codex-acp")
	if err != nil {
		return "", nil, nil, errCodexNeedsAdapter(command, args)
	}
	env := map[string]string{"CODEX_PATH": command}
	if dir := nodeDir(); dir != "" {
		path := os.Getenv("PATH")
		if path == "" {
			env["PATH"] = dir
		} else {
			env["PATH"] = dir + string(os.PathListSeparator) + path
		}
	}
	return adapter, nil, env, nil
}

func isCodexCLI(command string, args []string) bool {
	base := strings.TrimSuffix(filepath.Base(command), ".exe")
	if base != "codex" {
		return false
	}
	return len(args) == 0 || (len(args) == 1 && args[0] == "acp")
}

func errCodexNeedsAdapter(command string, args []string) error {
	return &launchError{msg: "codex CLI does not speak ACP; `codex acp` starts the interactive TUI, which exits because no terminal is available. Install @agentclientprotocol/codex-acp so codex-acp is on PATH (configured command was " + joinCommand(command, args) + ")"}
}

type launchError struct{ msg string }

func (e *launchError) Error() string { return e.msg }

var codexACPSubcommand sync.Map

func codexHasACPSubcommand(command string) bool {
	if v, ok := codexACPSubcommand.Load(command); ok {
		return v.(bool)
	}
	has := probeCodexHelp(command)
	codexACPSubcommand.Store(command, has)
	return has
}

func probeCodexHelp(command string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "--help").CombinedOutput()
	if err != nil && len(out) == 0 {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "acp" {
			return true
		}
	}
	return false
}

// nodeDir finds a Node binary for adapters installed as `#!/usr/bin/env node`
// scripts. The service PATH often does not include nvm.
func nodeDir() string {
	if p, err := exec.LookPath("node"); err == nil {
		return filepath.Dir(p)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if p := nodeInHome(home); p != "" {
		return filepath.Dir(p)
	}
	return ""
}

func nodeInHome(home string) string {
	root := filepath.Join(home, ".nvm", "versions", "node")
	if b, err := os.ReadFile(filepath.Join(home, ".nvm", "alias", "default")); err == nil {
		if p := nodeMatching(root, strings.TrimSpace(string(b))); p != "" {
			return p
		}
	}
	return newestNode(root, "")
}

// nodeMatching resolves an nvm alias. The alias file may contain a full
// version ("v26.12.1") or a prefix ("26").
func nodeMatching(root, ver string) string {
	if ver == "" {
		return ""
	}
	if p := nodeBinary(filepath.Join(root, ver)); p != "" {
		return p
	}
	prefix := ver
	if !strings.HasPrefix(prefix, "v") {
		prefix = "v" + prefix
	}
	return newestNode(root, prefix)
}

func newestNode(root, prefix string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && (prefix == "" || strings.HasPrefix(e.Name(), prefix)) {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return versionLess(names[i], names[j]) })
	for i := len(names) - 1; i >= 0; i-- {
		if p := nodeBinary(filepath.Join(root, names[i])); p != "" {
			return p
		}
	}
	return ""
}

func nodeBinary(versionDir string) string {
	p := filepath.Join(versionDir, "bin", "node")
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return ""
	}
	return p
}

func versionLess(a, b string) bool {
	pa, pb := splitVersion(a), splitVersion(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

func splitVersion(v string) []int {
	v = strings.TrimPrefix(v, "v")
	var out []int
	for _, part := range strings.Split(v, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

func mergeEnv(base, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return out
}
