package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jahrulnr/nusamation/internal/app"
	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func main() {
	args := os.Args[1:]
	if len(args) == 1 && (args[0] == "-version" || args[0] == "--version") {
		fmt.Printf("%s %s\n", config.AppName, config.Version)
		return
	}
	if len(args) > 0 && args[0] == "reset-password" {
		if err := resetPassword(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "reset-password:", err)
			os.Exit(1)
		}
		return
	}
	cfg, err := config.Parse(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func resetPassword(args []string) error {
	home, _ := os.UserHomeDir()
	defaultDir := filepath.Join(home, ".nusamation")
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	dataDir := fs.String("data-dir", defaultDir, "NusaMation data directory")
	password := fs.String("password", "", "new password (otherwise generated)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *password == "" {
		*password = "nm_" + security.Token(9)
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(*dataDir, "nusamation.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	if err := auth.SetPassword(context.Background(), st, *password); err != nil {
		return err
	}
	fmt.Println("Password updated.")
	fmt.Println("New password:", *password)
	return nil
}
