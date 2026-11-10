package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jahrulnr/nusamation/internal/store"
)

const mcpUsage = `usage: nusamation mcp <list|get|add|set|remove|run> [flags]

Manage shared MCP servers without external SQLite tools.
  list [--json]
  get <name|id> [--json] [--show-secrets]
  add --name NAME [--transport stdio|http|sse] [--command CMD] [--arg ARG]... [--url URL] [--env KEY=VALUE]... [--header KEY=VALUE]... [--enabled=true]
  set <name|id> [--name NAME] [--transport TYPE] [--command CMD] [--arg ARG]... [--url URL] [--env KEY=VALUE]... [--env-rm KEY]... [--header KEY=VALUE]... [--header-rm KEY]... [--enabled=BOOL]
  remove <name|id>
  run --browser-use | --conversation   Run an internal stdio MCP server
Management commands accept --data-dir DIR. Existing sessions refresh when their room becomes idle.
`

func mcpCmd(args []string) error {
	if len(args) == 0 {
		return usagef("missing mcp subcommand")
	}
	op := args[0]
	if op == "run" {
		return mcpRun(args[1:])
	}
	if op == "help" || op == "-h" || op == "--help" {
		fmt.Print(mcpUsage)
		return flag.ErrHelp
	}
	if op != "list" && op != "get" && op != "add" && op != "set" && op != "remove" {
		return usagef("unknown mcp subcommand %q", op)
	}
	fs, dir := subFlagSet("mcp "+op, mcpUsage)
	jsonOut := fs.Bool("json", false, "JSON output")
	show := fs.Bool("show-secrets", false, "show decrypted values")
	name := fs.String("name", "", "server name")
	transport := fs.String("transport", "", "stdio, http, or sse")
	command := fs.String("command", "", "command")
	url := fs.String("url", "", "server URL")
	enabled := fs.Bool("enabled", true, "enable server")
	var argv, env, envrm, headers, headersrm stringList
	fs.Var(&argv, "arg", "argument")
	fs.Var(&env, "env", "environment KEY=VALUE")
	fs.Var(&envrm, "env-rm", "remove environment key")
	fs.Var(&headers, "header", "header KEY=VALUE")
	fs.Var(&headersrm, "header-rm", "remove header")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return err
	}
	if (op == "list" || op == "add") && len(pos) != 0 || (op != "list" && op != "add") && len(pos) != 1 {
		return usagef("invalid arguments for mcp %s", op)
	}
	st, err := openStore(*dir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	if op == "list" {
		list, e := st.ListMCP(ctx)
		if e != nil {
			return e
		}
		for i := range list {
			list[i] = list[i].Public()
		}
		if *jsonOut {
			printJSON(list)
		} else {
			for _, m := range list {
				fmt.Printf("%s\t%s\t%s\tenabled=%t\n", m.ID, m.Name, m.Transport, m.Enabled)
			}
		}
		return nil
	}
	var cur store.MCPServer
	if op != "add" {
		list, e := st.ListMCP(ctx)
		if e != nil {
			return e
		}
		matches := 0
		for _, m := range list {
			if m.ID == pos[0] || m.Name == pos[0] {
				cur = m
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("MCP server %q: expected one match, found %d", pos[0], matches)
		}
	}
	if op == "get" {
		if !*show {
			cur = cur.Public()
		}
		if *jsonOut {
			printJSON(cur)
		} else {
			printJSON(cur)
		}
		return nil
	}
	if op == "remove" {
		if err = st.DeleteMCP(ctx, cur.ID); err != nil {
			return err
		}
		fmt.Printf("removed MCP server %s\n", cur.Name)
		fmt.Fprintln(os.Stderr, "note: MCP changes refresh each room when idle")
		return nil
	}
	changes := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { changes[f.Name] = true })
	in := store.MCPServer{Name: *name, Transport: *transport, Command: *command, URL: *url, Enabled: *enabled, EnabledSet: changes["enabled"]}
	if op == "add" {
		in.EnabledSet = true
	} else {
		in.Enabled = cur.Enabled
		if changes["enabled"] {
			in.Enabled = *enabled
		}
		if !changes["name"] && !changes["transport"] && !changes["command"] && !changes["url"] && !changes["arg"] && !changes["env"] && !changes["env-rm"] && !changes["header"] && !changes["header-rm"] && !changes["enabled"] {
			return usagef("mcp set requires a field flag")
		}
	}
	if changes["arg"] {
		in.Args = argv
	}
	merge := func(old store.SecretList, adds, removes []string) (store.SecretList, error) {
		pairs, e := envList(adds)
		if e != nil {
			return nil, e
		}
		out := make(store.SecretList, 0, len(old)+len(pairs))
		for _, v := range old {
			keep := true
			for _, k := range removes {
				if v.Name == k {
					keep = false
				}
			}
			for _, v2 := range pairs {
				if v.Name == v2.Name {
					keep = false
				}
			}
			if keep {
				out = append(out, v)
			}
		}
		return append(out, pairs...), nil
	}
	if op == "add" || changes["env"] || changes["env-rm"] {
		in.Env, err = merge(cur.Env, env, envrm)
		if err != nil {
			return err
		}
	}
	if op == "add" || changes["header"] || changes["header-rm"] {
		in.Headers, err = merge(cur.Headers, headers, headersrm)
		if err != nil {
			return err
		}
	}
	if op == "set" {
		kind, cmd, endpoint := cur.Transport, cur.Command, cur.URL
		if in.Transport != "" {
			kind = in.Transport
		}
		if in.Command != "" {
			cmd = in.Command
		}
		if in.URL != "" {
			endpoint = in.URL
		}
		if kind == "stdio" && strings.TrimSpace(cmd) == "" {
			return fmt.Errorf("MCP stdio server requires a command")
		}
		if (kind == "http" || kind == "sse") && strings.TrimSpace(endpoint) == "" {
			return fmt.Errorf("MCP HTTP/SSE server requires a URL")
		}
	}
	if op == "add" {
		cur, err = st.CreateMCP(ctx, in)
	} else {
		cur, err = st.UpdateMCP(ctx, cur.ID, in)
	}
	if err != nil {
		return err
	}
	if *jsonOut {
		printJSON(cur.Public())
	} else {
		fmt.Printf("saved MCP server %s (%s)\n", strings.TrimSpace(cur.Name), cur.ID)
	}
	fmt.Fprintln(os.Stderr, "note: MCP changes refresh each room when idle")
	return nil
}

func mcpRun(args []string) error {
	if len(args) != 1 {
		return usagef("use mcp run --browser-use or --conversation")
	}
	switch args[0] {
	case "--browser-use":
		return browserUseCmd()
	case "--conversation":
		return conversationMCPCmd()
	case "-h", "--help":
		fmt.Fprintln(os.Stderr, "usage: nusamation mcp run --browser-use | --conversation")
		return flag.ErrHelp
	default:
		return usagef("use mcp run --browser-use or --conversation")
	}
}
