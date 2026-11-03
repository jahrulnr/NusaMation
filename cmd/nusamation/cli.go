package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

// errUsage marks errors that should print command usage and exit with code 2.
type errUsage struct{ msg string }

func (e errUsage) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return errUsage{msg: fmt.Sprintf(format, args...)}
}

// fail prints a subcommand error and exits like a well-behaved CLI: usage
// errors exit 2, help requests exit 0, everything else exits 1.
func fail(cmd string, err error) {
	if err == nil {
		return
	}
	var ue errUsage
	switch {
	case errors.Is(err, flag.ErrHelp):
		return
	case errors.As(err, &ue):
		fmt.Fprintln(os.Stderr, "error:", ue.msg)
		fmt.Fprintf(os.Stderr, "run '%s %s -h' for usage\n", progName(), cmd)
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "%s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func progName() string {
	if len(os.Args) > 0 && os.Args[0] != "" {
		return filepath.Base(os.Args[0])
	}
	return "nusamation"
}

// applyNote is printed after mutations: a running instance keeps its cached
// settings, so changes land fully on the next start.
func applyNote() {
	fmt.Fprintln(os.Stderr, "note: restart NusaMation for changes to apply to a running instance")
}

func defaultDataDir() string {
	if v := os.Getenv("NUSAMATION_DATA_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".nusamation")
}

// subFlagSet builds a FlagSet with the shared -data-dir flag. help is printed
// for -h via fs.Usage.
func subFlagSet(name, help string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	// Discard the flag package's own error line; fail() prints a single clean
	// message and Usage still goes to stderr.
	fs.SetOutput(io.Discard)
	dataDir := fs.String("data-dir", defaultDataDir(), "NusaMation data directory")
	fs.Usage = func() { fmt.Fprint(os.Stderr, help) }
	return fs, dataDir
}

// openStore opens the app database with the same sealer the server uses, so
// sealed agent/MCP env vars stay readable and re-sealable from the CLI.
func openStore(dataDir string) (*store.Store, error) {
	st, err := store.Open(filepath.Join(dataDir, "nusamation.db"))
	if err != nil {
		return nil, err
	}
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	st.SetSealer(sealer)
	return st, nil
}

// stringList is a repeatable string flag value (-flag a -flag b).
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// envList parses repeated KEY=VALUE flags into a SecretList.
func envList(pairs []string) (store.SecretList, error) {
	out := make(store.SecretList, 0, len(pairs))
	for _, p := range pairs {
		name, value, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid env %q: expected KEY=VALUE", p)
		}
		out = append(out, store.EnvVar{Name: strings.TrimSpace(name), Value: value})
	}
	return out, nil
}

func printJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Println(v)
		return
	}
	fmt.Println(string(b))
}

// parseInterleaved collects positional args while allowing flags to appear
// anywhere: the flag package stops at the first non-flag token, so it is
// re-invoked on the remainder until args are exhausted.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// resolveAgent looks up an agent by slug first, then by ID.
func resolveAgent(ctx context.Context, st *store.Store, ref string) (store.Agent, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return store.Agent{}, errors.New("agent slug or id is required")
	}
	a, err := st.GetAgentBySlug(ctx, ref)
	if err == nil {
		return a, nil
	}
	return st.GetAgent(ctx, ref)
}
