package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jahrulnr/nusamation/internal/app"
	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/security"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "-help", "help":
			printUsage()
			return
		case "-v", "-version", "--version", "version":
			fmt.Printf("%s %s\n", config.AppName, config.Version)
			return
		case "reset-password":
			fail("reset-password", resetPassword(args[1:]))
			return
		case "automation", "automations":
			fail("automation", automationCmd(args[1:]))
			return
		case "agent", "agents":
			fail("agent", agentCmd(args[1:]))
			return
		case "config", "settings":
			fail("config", configCmd(args[1:]))
			return
		case "whatsapp", "wa":
			fail("whatsapp", whatsappCmd(args[1:]))
			return
		}
	}
	cfg, err := config.Parse(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage()
			return
		}
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintf(os.Stderr, "run '%s --help' for usage\n", progName())
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`%s %s - ACP-native personal agent mesh

Usage:
  %s [flags]                 Start the server
  %s <command> [flags]       Manage NusaMation without the web UI

Commands:
  automation    Manage scheduled ACP jobs and run history
  agent         Manage ACP agent CLIs (list, detect, get, add, set, remove)
  config        Manage stored settings (list, get, add, set, remove)
  whatsapp      Manage WhatsApp channel settings and send messages
  reset-password  Reset the admin password
  version       Print version

Server flags:
`, config.AppName, config.Version, progName(), progName())
	config.FlagUsage(os.Stdout)
	fmt.Printf(`
Environment variables mirror the flags: NUSAMATION_ADDR, NUSAMATION_DATA_DIR,
NUSAMATION_PUBLIC_URL, NUSAMATION_PASSWORD, NUSAMATION_LOG_LEVEL,
NUSAMATION_NO_WHATSAPP, NUSAMATION_OPEN, NUSAMATION_DEV.

Use "%s <command> -h" for command-specific help.
`, progName())
}

func resetPassword(args []string) error {
	fs, dataDir := subFlagSet("reset-password", "usage: nusamation reset-password [-data-dir DIR] [-password P]\n")
	password := fs.String("password", "", "new password (otherwise generated)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("unexpected arguments: %s", fs.Arg(0))
	}
	if *password == "" {
		*password = "nm_" + security.Token(9)
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	if err := auth.SetPassword(ctx, st, *password); err != nil {
		return err
	}
	// Invalidate all browser sessions so the old password cannot keep working.
	if err := st.DeleteAllSessions(ctx); err != nil {
		return err
	}
	fmt.Println("Password updated.")
	fmt.Println("New password:", *password)
	return nil
}
