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
  send [--addr HOST:PORT]       Send a JSON request read from stdin via the running server
  search [flags] [text]       Search indexed messages (newest first, JSON)
  contacts [flags] [text]     Search locally synced WhatsApp contacts (JSON)
  user [flags] JID            Get user information via the running server
  groups [--addr HOST:PORT]   List joined groups (JID + name) via the running server

Set flags:
  --enabled=B          Enable the WhatsApp channel
  --auto-start=B       Connect automatically when paired
  --group-policy P     disabled | mention | all
  --allow "n1,n2"      Replace the allowlist (comma-separated; empty clears and denies)
  --allow "*"          Allow everyone (must be the only entry, never with numbers)
  --allow-add N        Add a number to the allowlist (repeatable)
  --allow-remove N     Remove a number from the allowlist (repeatable)
  --read-receipts=B    Send read receipts
  --send-typing=B      Send typing indicators
  --retention-days N   Purge indexed messages older than N days (0 = keep)
  --send-pacing N      Minimum seconds between two sends to the same chat (0 = off)
  --queue-cap N        Maximum queued messages per chat

Numbers may include +, spaces or JID suffixes; they are normalized to digits.
An empty allowlist allows no one — the operator may not have filled the form
yet. A single "*" allows everyone; "*" cannot be combined with numbers.

Flags:
  --data-dir DIR       NusaMation data directory (default ~/.nusamation)
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
	case "search":
		return whatsappSearch(args[1:])
	case "contacts":
		return whatsappContacts(args[1:])
	case "user":
		return whatsappUser(args[1:])
	case "groups":
		return whatsappGroups(args[1:])
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
	fs, dataDir := subFlagSet("whatsapp show", "usage: nusamation whatsapp show [--data-dir DIR]\n")
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
	var enabled, autoStart, readReceipts, sendTyping bool
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
	retention := fs.Int("retention-days", 0, "purge indexed messages older than N days (0 = keep)")
	sendPacing := fs.Int("send-pacing", -1, "minimum seconds between two sends to the same chat (0 = off)")
	queueCap := fs.Int("queue-cap", 0, "maximum queued messages per chat")
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
		"allow-add", "allow-remove", "read-receipts", "send-typing", "retention-days", "send-pacing", "queue-cap",
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
	if changed["retention-days"] {
		if *retention < 0 || *retention > 3650 {
			return fmt.Errorf("retention-days must be between 0 and 3650")
		}
		s.RetentionDays = *retention
	}
	if changed["send-pacing"] {
		if *sendPacing < 0 || *sendPacing > 60 {
			return fmt.Errorf("send-pacing must be between 0 and 60 seconds")
		}
		s.SendPacing = *sendPacing
	}
	if changed["queue-cap"] {
		if *queueCap < 1 || *queueCap > 200 {
			return fmt.Errorf("queue-cap must be between 1 and 200 messages")
		}
		s.QueueCap = *queueCap
	}
	if changed["allow"] {
		nums := splitNumbers(allow)
		if strings.TrimSpace(allow) != "" && len(nums) == 0 {
			return fmt.Errorf("allowlist %q has no valid numbers; an empty list allows no one — use * to allow everyone", allow)
		}
		if err := (whatsapp.Settings{AllowedNumbers: nums}).ValidateAllowlist(); err != nil {
			return err
		}
		s.AllowedNumbers = nums
	}
	if changed["allow-add"] || changed["allow-remove"] {
		cur := map[string]bool{}
		for _, n := range s.AllowedNumbers {
			cur[n] = true
		}
		for _, raw := range allowAdd {
			if strings.TrimSpace(raw) == "*" {
				cur["*"] = true
				continue
			}
			if n := whatsapp.NormalizeNumber(raw); n != "" {
				cur[n] = true
			}
		}
		for _, raw := range allowRemove {
			if strings.TrimSpace(raw) == "*" {
				delete(cur, "*")
				continue
			}
			delete(cur, whatsapp.NormalizeNumber(raw))
		}
		list := make([]string, 0, len(cur))
		for n := range cur {
			list = append(list, n)
		}
		if err := (whatsapp.Settings{AllowedNumbers: list}).ValidateAllowlist(); err != nil {
			return err
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
	for p := range strings.SplitSeq(raw, ",") {
		// "*" passes through as its own element so ValidateAllowlist can
		// refuse a list that mixes it with numbers.
		if strings.TrimSpace(p) == "*" {
			out = append(out, "*")
			continue
		}
		if n := whatsapp.NormalizeNumber(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}
