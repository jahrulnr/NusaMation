package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusamation/internal/channels/whatsapp"
	"github.com/jahrulnr/nusamation/internal/store"
)

const whatsappUsage = `usage: nusamation whatsapp <subcommand> [flags]

Manage WhatsApp channel settings (stored under channel.whatsapp.settings).

Subcommands:
  show                        Print current settings (JSON, merged over defaults)
  set [flags]                 Update settings; only given flags are changed
  send [-addr HOST:PORT]       Send a JSON request read from stdin via the running server

Set flags:
  -enabled=B          Enable the WhatsApp channel
  -auto-start=B       Connect automatically when paired
  -group-policy P     disabled | mention | all
  -allow "n1,n2"      Replace the allowlist (comma-separated; empty clears)
  -allow-add N        Add a number to the allowlist (repeatable)
  -allow-remove N     Remove a number from the allowlist (repeatable)
  -read-receipts=B    Send read receipts
  -send-typing=B      Send typing indicators
  -stream-edits=B     Edit the ack message instead of sending a final one

Numbers may include +, spaces or JID suffixes; they are normalized to digits.
An empty allowlist allows anyone who can message the linked account.

Flags:
  -data-dir DIR       NusaMation data directory (default ~/.nusamation)
  -h                  This help
`

func whatsappCmd(args []string) error {
	if len(args) == 0 {
		return usagef("missing whatsapp subcommand")
	}
	switch args[0] {
	case "show", "get":
		return whatsappShow(args[1:])
	case "set":
		return whatsappSet(args[1:])
	case "send":
		return whatsappSend(args[1:])
	case "-h", "--help", "-help", "help":
		fmt.Print(whatsappUsage)
		return flag.ErrHelp
	default:
		return usagef("unknown whatsapp subcommand %q", args[0])
	}
}

func loadWASettings(ctx context.Context, st *store.Store) (whatsapp.Settings, error) {
	s := whatsapp.DefaultSettings()
	if err := st.GetJSONSetting(ctx, whatsapp.SettingsKey, &s); err != nil {
		if store.IsNotFound(err) {
			return s, nil
		}
		return s, err
	}
	s.Normalize()
	return s, nil
}

func whatsappShow(args []string) error {
	fs, dataDir := subFlagSet("whatsapp show", "usage: nusamation whatsapp show [-data-dir DIR]\n")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	s, err := loadWASettings(context.Background(), st)
	if err != nil {
		return err
	}
	printJSON(s)
	return nil
}

func whatsappSet(args []string) error {
	fs, dataDir := subFlagSet("whatsapp set", whatsappUsage)
	var enabled, autoStart, readReceipts, sendTyping, streamEdits bool
	var groupPolicy, allow string
	var allowAdd, allowRemove stringList
	fs.BoolVar(&enabled, "enabled", false, "enable channel")
	fs.BoolVar(&autoStart, "auto-start", false, "connect automatically when paired")
	fs.StringVar(&groupPolicy, "group-policy", "", "disabled|mention|all")
	fs.StringVar(&allow, "allow", "", "replace allowlist (comma-separated)")
	fs.Var(&allowAdd, "allow-add", "add number to allowlist (repeatable)")
	fs.Var(&allowRemove, "allow-remove", "remove number from allowlist (repeatable)")
	fs.BoolVar(&readReceipts, "read-receipts", false, "send read receipts")
	fs.BoolVar(&sendTyping, "send-typing", false, "send typing indicators")
	fs.BoolVar(&streamEdits, "stream-edits", false, "edit ack message for final answer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	changed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { changed[f.Name] = true })
	mutating := false
	for _, name := range []string{
		"enabled", "auto-start", "group-policy", "allow",
		"allow-add", "allow-remove", "read-receipts", "send-typing", "stream-edits",
	} {
		mutating = mutating || changed[name]
	}
	if !mutating {
		return usagef("whatsapp set requires at least one flag")
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	s, err := loadWASettings(ctx, st)
	if err != nil {
		return err
	}
	if changed["enabled"] {
		s.Enabled = enabled
	}
	if changed["auto-start"] {
		s.AutoStart = autoStart
	}
	if changed["group-policy"] {
		if groupPolicy != "disabled" && groupPolicy != "mention" && groupPolicy != "all" {
			return fmt.Errorf("invalid group policy %q: expected disabled, mention or all", groupPolicy)
		}
		s.GroupPolicy = groupPolicy
	}
	if changed["read-receipts"] {
		s.ReadReceipts = readReceipts
	}
	if changed["send-typing"] {
		s.SendTyping = sendTyping
	}
	if changed["stream-edits"] {
		s.StreamEdits = streamEdits
	}
	if changed["allow"] {
		nums := splitNumbers(allow)
		if strings.TrimSpace(allow) != "" && len(nums) == 0 {
			return fmt.Errorf("allowlist %q has no valid numbers; an empty allowlist allows anyone", allow)
		}
		s.AllowedNumbers = nums
	}
	if changed["allow-add"] || changed["allow-remove"] {
		cur := map[string]bool{}
		for _, n := range s.AllowedNumbers {
			cur[n] = true
		}
		for _, raw := range allowAdd {
			if n := whatsapp.NormalizeNumber(raw); n != "" {
				cur[n] = true
			}
		}
		for _, raw := range allowRemove {
			delete(cur, whatsapp.NormalizeNumber(raw))
		}
		list := make([]string, 0, len(cur))
		for n := range cur {
			list = append(list, n)
		}
		s.AllowedNumbers = list
	}
	s.Normalize()
	if err := st.SetJSONSetting(ctx, whatsapp.SettingsKey, s); err != nil {
		return err
	}
	printJSON(s)
	applyNote()
	return nil
}

func splitNumbers(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if n := whatsapp.NormalizeNumber(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}
