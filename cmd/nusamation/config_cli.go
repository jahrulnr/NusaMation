package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusamation/internal/store"
)

const configUsage = `usage: nusamation config <subcommand> [flags]

Manage stored NusaMation settings (key/value). Structured settings such as
channel.whatsapp.settings and routing.defaults take a JSON string value.

Subcommands:
  list                        List all settings (key=value, or --json)
  get <key>                   Print a setting's raw value
  set <key> <value>           Create or update a setting
  add <key> <value>           Create a setting; fails if it exists
  remove <key>                Delete a setting

Flags:
  --data-dir DIR     NusaMation data directory (default ~/.nusamation)
  --json             JSON output for list/get
  --validate-json    For set/add: reject values that are not valid JSON
  -h                This help
`

func configCmd(args []string) error {
	if len(args) == 0 {
		return usagef("missing config subcommand")
	}
	switch args[0] {
	case "list", "ls":
		return configList(args[1:])
	case "get", "show":
		return configGet(args[1:])
	case "set":
		return configSet(args[1:], false)
	case "add":
		return configSet(args[1:], true)
	case "remove", "delete", "rm":
		return configRemove(args[1:])
	case "-h", "--help", "-help", "help":
		fmt.Print(configUsage)
		return flag.ErrHelp
	default:
		return usagef("unknown config subcommand %q", args[0])
	}
}

func configList(args []string) error {
	fs, dataDir := subFlagSet("config list", "usage: nusamation config list [--json] [--data-dir DIR]\n")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	list, err := st.ListSettings(context.Background())
	if err != nil {
		return err
	}
	if *jsonOut {
		printJSON(list)
		return nil
	}
	for _, s := range list {
		fmt.Printf("%s=%s\n", s.Key, s.Value)
	}
	return nil
}

func configGet(args []string) error {
	fs, dataDir := subFlagSet("config get", "usage: nusamation config get <key> [--json] [--data-dir DIR]\n")
	jsonOut := fs.Bool("json", false, "JSON output")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return perr
	}
	if len(pos) != 1 {
		return usagef("config get requires exactly one <key> argument")
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	v, err := st.GetSetting(context.Background(), pos[0])
	if err != nil {
		if store.IsNotFound(err) {
			return fmt.Errorf("setting %q not found", pos[0])
		}
		return err
	}
	if *jsonOut {
		printJSON(map[string]string{"key": pos[0], "value": v})
	} else {
		fmt.Println(v)
	}
	return nil
}

func configSet(args []string, createOnly bool) error {
	name := "set"
	if createOnly {
		name = "add"
	}
	fs, dataDir := subFlagSet("config "+name,
		"usage: nusamation config "+name+" <key> <value> [--validate-json] [--data-dir DIR]\n")
	validateJSON := fs.Bool("validate-json", false, "reject non-JSON values")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return perr
	}
	if len(pos) != 2 {
		return usagef("config %s requires <key> and <value> arguments", name)
	}
	key := strings.TrimSpace(pos[0])
	value := pos[1]
	if key == "" {
		return usagef("setting key must not be empty")
	}
	if *validateJSON && !json.Valid([]byte(value)) {
		return fmt.Errorf("value is not valid JSON")
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	if createOnly {
		if _, err := st.GetSetting(ctx, key); err == nil {
			return fmt.Errorf("setting %q already exists; use 'config set' to update", key)
		} else if !store.IsNotFound(err) {
			return err
		}
	}
	if err := st.SetSetting(ctx, key, value); err != nil {
		return err
	}
	if createOnly {
		fmt.Printf("added setting %s\n", key)
	} else {
		fmt.Printf("set %s\n", key)
	}
	applyNote()
	return nil
}

func configRemove(args []string) error {
	fs, dataDir := subFlagSet("config remove", "usage: nusamation config remove <key> [--data-dir DIR]\n")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return perr
	}
	if len(pos) != 1 {
		return usagef("config remove requires exactly one <key> argument")
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.DeleteSetting(context.Background(), pos[0]); err != nil {
		if store.IsNotFound(err) {
			return fmt.Errorf("setting %q not found", pos[0])
		}
		return err
	}
	fmt.Printf("removed setting %s\n", pos[0])
	applyNote()
	return nil
}
