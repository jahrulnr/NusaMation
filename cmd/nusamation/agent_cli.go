package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/store"
)

const agentUsage = `usage: nusamation agent <subcommand> [flags]

Manage ACP agent CLIs.

Subcommands:
  list                        List configured agents
  detect [--add]               Detect popular ACP CLIs on PATH (--add registers them)
  get <slug|id>               Show one agent (env values masked; --show-secrets to reveal)
  add --name N --command C      Create an agent (requires --name and --command)
  set <slug|id> [flags]       Update fields of an existing agent
  remove <slug|id>            Delete an agent

Add/set flags:
  --slug S           Agent slug (derived from --name when omitted on add)
  --arg A            Command argument; repeatable. On 'set', replaces all args
  --cwd DIR          Working directory (default: <data-dir>/workspace)
  --env K=V          Environment variable; repeatable. On 'set', upserts by name
  --env-rm K         Remove env var by name; repeatable ('set' only)
  --policy P         Approval policy: ask, auto_safe, auto_all (default ask)
  --model M          Default model ('set': --model "" clears)
  --description T    Description text
  --icon I           sparkles, zap, brain, terminal, globe, star
  --color C          amber, violet, emerald, blue, rose, slate
  --enabled=B        Enable/disable the agent (add default true)
  --auto-start=B     Idle mode: start on boot and keep the agent warm

Other flags:
  --data-dir DIR     NusaMation data directory (default ~/.nusamation)
  --json             JSON output for list/get/detect/add/set
  -h                This help
`

func agentCmd(args []string) error {
	if len(args) == 0 {
		return usagef("missing agent subcommand")
	}
	switch args[0] {
	case "list", "ls":
		return agentList(args[1:])
	case "detect":
		return agentDetect(args[1:])
	case "get", "show":
		return agentGet(args[1:])
	case "add", "create", "new":
		return agentAdd(args[1:])
	case "set", "update":
		return agentSet(args[1:])
	case "remove", "delete", "rm":
		return agentRemove(args[1:])
	case "-h", "--help", "-help", "help":
		fmt.Print(agentUsage)
		return flag.ErrHelp
	default:
		return usagef("unknown agent subcommand %q", args[0])
	}
}

func agentList(args []string) error {
	fs, dataDir := subFlagSet("agent list", "usage: nusamation agent list [--json] [--data-dir DIR]\n")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	list, err := st.ListAgents(context.Background())
	if err != nil {
		return err
	}
	if *jsonOut {
		out := make([]store.Agent, 0, len(list))
		for _, a := range list {
			out = append(out, a.Public())
		}
		printJSON(out)
		return nil
	}
	for _, a := range list {
		fmt.Printf("%s\t%s\tenabled=%t\tstatus=%s\t%s %s\n",
			a.Slug, a.Name, a.Enabled, a.Status, a.Command, strings.Join(a.Args, " "))
	}
	return nil
}

func agentGet(args []string) error {
	fs, dataDir := subFlagSet("agent get", "usage: nusamation agent get <slug|id> [--json] [--show-secrets] [--data-dir DIR]\n")
	jsonOut := fs.Bool("json", false, "JSON output")
	showSecrets := fs.Bool("show-secrets", false, "include decrypted env values")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return perr
	}
	if len(pos) != 1 {
		return usagef("agent get requires exactly one <slug|id> argument")
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	a, err := resolveAgent(context.Background(), st, pos[0])
	if err != nil {
		if store.IsNotFound(err) {
			return fmt.Errorf("agent %q not found", pos[0])
		}
		return err
	}
	if !*showSecrets {
		a = a.Public()
	}
	if *jsonOut {
		printJSON(a)
		return nil
	}
	fmt.Printf("id:        %s\nname:      %s\nslug:      %s\ncommand:   %s %s\ncwd:       %s\nenabled:   %t\nautoStart: %t\npolicy:    %s\nmodel:     %s\nstatus:    %s\n",
		a.ID, a.Name, a.Slug, a.Command, strings.Join(a.Args, " "), a.Cwd,
		a.Enabled, a.AutoStart, a.ApprovalPolicy, a.DefaultModel, a.Status)
	if a.Description != "" {
		fmt.Printf("desc:      %s\n", a.Description)
	}
	for _, e := range a.Env {
		if *showSecrets {
			fmt.Printf("env:       %s=%s\n", e.Name, e.Value)
		} else {
			fmt.Printf("env:       %s (set)\n", e.Name)
		}
	}
	return nil
}

type agentForm struct {
	name, slug, command, cwd, policy, model, desc, icon, color string
	args, envPairs, envRm                                      stringList
	enabled, autoStart                                         bool
}

func agentFormFlags(fs *flag.FlagSet, f *agentForm, add bool) {
	fs.StringVar(&f.name, "name", "", "agent display name")
	fs.StringVar(&f.slug, "slug", "", "agent slug")
	fs.StringVar(&f.command, "command", "", "command to run in ACP mode")
	fs.Var(&f.args, "arg", "command argument (repeatable)")
	fs.StringVar(&f.cwd, "cwd", "", "working directory")
	fs.Var(&f.envPairs, "env", "environment variable KEY=VALUE (repeatable)")
	if !add {
		fs.Var(&f.envRm, "env-rm", "remove environment variable by name (repeatable)")
	}
	fs.StringVar(&f.policy, "policy", "", "approval policy: ask, auto_safe, auto_all")
	fs.StringVar(&f.model, "model", "", "default model")
	fs.StringVar(&f.desc, "description", "", "description")
	fs.StringVar(&f.icon, "icon", "", "icon: sparkles, zap, brain, terminal, globe, star")
	fs.StringVar(&f.color, "color", "", "color: amber, violet, emerald, blue, rose, slate")
	fs.BoolVar(&f.enabled, "enabled", add, "enable the agent")
	fs.BoolVar(&f.autoStart, "auto-start", false, "idle mode: start on server boot and keep the agent warm")
}

func agentAdd(args []string) error {
	fs, dataDir := subFlagSet("agent add",
		"usage: nusamation agent add --name N --command C [--arg A]... [--cwd D] [flags]\n       see 'nusamation agent -h' for all flags\n")
	var f agentForm
	agentFormFlags(fs, &f, true)
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	env, err := envList(f.envPairs)
	if err != nil {
		return err
	}
	cwd := f.cwd
	if cwd == "" {
		cwd = filepath.Join(*dataDir, "workspace")
	}
	slug := f.slug
	if slug == "" {
		slug = f.name
	}
	in := store.AgentInput{
		Name: f.name, Slug: slug, Command: f.command, Cwd: cwd,
		Description: f.desc, Icon: f.icon, Color: f.color,
		ApprovalPolicy: f.policy, DefaultModel: f.model,
		Enabled: &f.enabled, AutoStart: &f.autoStart,
	}
	if f.args != nil {
		in.Args = f.args
	}
	if env != nil {
		in.Env = env
	}
	a, err := st.CreateAgent(context.Background(), in)
	if err != nil {
		return err
	}
	if *jsonOut {
		printJSON(a.Public())
	} else {
		fmt.Printf("created agent %s (id %s): %s %s\n", a.Slug, a.ID, a.Command, strings.Join(a.Args, " "))
	}
	applyNote()
	return nil
}

func agentSet(args []string) error {
	fs, dataDir := subFlagSet("agent set",
		"usage: nusamation agent set <slug|id> [flags]\n       see 'nusamation agent -h' for all flags\n")
	var f agentForm
	agentFormFlags(fs, &f, false)
	jsonOut := fs.Bool("json", false, "JSON output")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return perr
	}
	if len(pos) != 1 {
		return usagef("agent set requires exactly one <slug|id> argument")
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	a, err := resolveAgent(ctx, st, pos[0])
	if err != nil {
		if store.IsNotFound(err) {
			return fmt.Errorf("agent %q not found", pos[0])
		}
		return err
	}
	in := store.AgentInput{}
	changed := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { changed[fl.Name] = true })
	mutating := false
	for _, name := range []string{
		"name", "slug", "command", "arg", "cwd", "env", "env-rm",
		"policy", "model", "description", "icon", "color", "enabled", "auto-start",
	} {
		mutating = mutating || changed[name]
	}
	if !mutating {
		return usagef("agent set requires at least one field flag")
	}
	if changed["name"] {
		in.Name = f.name
	}
	if changed["slug"] {
		in.Slug = f.slug
	}
	if changed["command"] {
		in.Command = f.command
	}
	if changed["arg"] {
		in.Args = f.args
	}
	if changed["cwd"] {
		in.Cwd = f.cwd
	}
	if changed["policy"] {
		in.ApprovalPolicy = f.policy
	}
	if changed["model"] {
		in.DefaultModel = f.model
		in.DefaultModelSet = true
	}
	if changed["description"] {
		in.Description = f.desc
	}
	if changed["icon"] {
		in.Icon = f.icon
	}
	if changed["color"] {
		in.Color = f.color
	}
	if changed["enabled"] {
		in.Enabled = &f.enabled
	}
	if changed["auto-start"] {
		in.AutoStart = &f.autoStart
	}
	if changed["env"] || changed["env-rm"] {
		adds, err := envList(f.envPairs)
		if err != nil {
			return err
		}
		rm := map[string]bool{}
		for _, n := range f.envRm {
			rm[n] = true
		}
		env := make(store.SecretList, 0, len(a.Env)+len(adds))
		for _, e := range a.Env {
			if !rm[e.Name] {
				env = append(env, e)
			}
		}
		for _, e := range adds {
			replaced := false
			for i := range env {
				if env[i].Name == e.Name {
					env[i] = e
					replaced = true
				}
			}
			if !replaced {
				env = append(env, e)
			}
		}
		in.Env = env
	}
	a, err = st.UpdateAgent(ctx, a.ID, in)
	if err != nil {
		return err
	}
	if *jsonOut {
		printJSON(a.Public())
	} else {
		fmt.Printf("updated agent %s (id %s)\n", a.Slug, a.ID)
	}
	applyNote()
	return nil
}

func agentRemove(args []string) error {
	fs, dataDir := subFlagSet("agent remove", "usage: nusamation agent remove <slug|id> [--data-dir DIR]\n")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return perr
	}
	if len(pos) != 1 {
		return usagef("agent remove requires exactly one <slug|id> argument")
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	a, err := resolveAgent(ctx, st, pos[0])
	if err != nil {
		if store.IsNotFound(err) {
			return fmt.Errorf("agent %q not found", pos[0])
		}
		return err
	}
	if err := st.DeleteAgent(ctx, a.ID); err != nil {
		return err
	}
	fmt.Printf("removed agent %s (id %s)\n", a.Slug, a.ID)
	applyNote()
	return nil
}

func agentDetect(args []string) error {
	fs, dataDir := subFlagSet("agent detect", "usage: nusamation agent detect [--add] [--json] [--data-dir DIR]\n\nDetect popular ACP CLIs on PATH. --add registers each missing one.\n")
	add := fs.Bool("add", false, "register detected CLIs as agents")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	found := agent.Detect()
	if len(found) == 0 {
		if !*jsonOut {
			fmt.Println("no known ACP CLI found on PATH")
		} else {
			printJSON([]agent.DetectedCLI{})
		}
		return nil
	}
	if *jsonOut && !*add {
		printJSON(found)
		return nil
	}
	if !*add {
		for _, d := range found {
			fmt.Printf("%s\t%s\t%s %s\n", d.Slug, d.Name, d.Command, strings.Join(d.Args, " "))
		}
		return nil
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	existing, err := st.ListAgents(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, a := range existing {
		have[a.Slug] = true
	}
	workspace := filepath.Join(*dataDir, "workspace")
	enabled := true
	type added struct {
		Slug string `json:"slug"`
		ID   string `json:"id"`
	}
	var created []added
	for _, d := range found {
		if have[d.Slug] {
			fmt.Fprintf(os.Stderr, "skip %s: slug already configured\n", d.Slug)
			continue
		}
		a, err := st.CreateAgent(ctx, store.AgentInput{
			Name: d.Name, Slug: d.Slug, Description: d.Description,
			Command: d.Command, Args: d.Args, Cwd: workspace,
			Icon: d.Icon, Color: d.Color, Enabled: &enabled, ApprovalPolicy: "ask",
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", d.Slug, err)
			continue
		}
		created = append(created, added{Slug: a.Slug, ID: a.ID})
		if !*jsonOut {
			fmt.Printf("added agent %s (id %s): %s %s\n", a.Slug, a.ID, a.Command, strings.Join(a.Args, " "))
		}
	}
	if *jsonOut {
		printJSON(created)
	}
	if len(created) > 0 {
		applyNote()
	}
	return nil
}
