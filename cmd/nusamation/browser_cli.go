package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jahrulnr/nusamation/internal/browser"
)

// browserCmd manages the embedded browser's Chromium executable — the one
// dependency the static binary cannot ship. Unlike the other subcommands it
// never touches the store or a running server, so it works before first boot.
func browserCmd(args []string) error {
	fs, dataDir := subFlagSet("browser", `usage: nusamation browser <command> [flags]

Commands:
  status            show which Chromium executable resolves, or how to install one
  install [method]  run an install method (default: the first offered)

Flags:
`)
	execOverride := fs.String("browser-exec", os.Getenv("NUSAMATION_BROWSER_EXEC"), "Chromium/Chrome executable override (same as the server flag)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root := installRoot(*dataDir)
	switch fs.Arg(0) {
	case "status", "":
		return browserStatus(*execOverride, root)
	case "install":
		return browserInstallCmd(*execOverride, root, fs.Arg(1))
	default:
		return usagef("unknown browser command %q (status|install)", fs.Arg(0))
	}
}

// installRoot is the data-dir folder the self-contained download unpacks into —
// keep in sync with the browser.Options.InstallDir app.go wires.
func installRoot(dataDir string) string {
	return filepath.Join(dataDir, "chrome")
}

func browserStatus(execOverride, root string) error {
	offer := browser.Offer(execOverride, root)
	if !offer.Missing {
		fmt.Println("Chromium executable:", offer.ExecPath)
		return nil
	}
	fmt.Println("No Chromium/Chrome executable found.")
	fmt.Println(offer.Searched)
	printOffer(offer)
	return nil
}

func browserInstallCmd(execOverride, root, method string) error {
	offer := browser.Offer(execOverride, root)
	if !offer.Missing {
		fmt.Println("A Chromium executable already resolves:", offer.ExecPath)
		return nil
	}
	if len(offer.Methods) == 0 {
		fmt.Println("No Chromium/Chrome executable found and no install method is available on this host.")
		fmt.Println("Install one manually, then pass -browser-exec / NUSAMATION_BROWSER_EXEC or put it on PATH.")
		return fmt.Errorf("no install method")
	}
	if method == "" {
		method = offer.Methods[0].ID
		fmt.Println("No method given — using", method)
	}
	// The download and package steps are network-bound; without a deadline a
	// stalled mirror hangs the command forever.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	p, err := browser.Install(ctx, execOverride, root, method, os.Stdout)
	if err != nil {
		return err
	}
	fmt.Println("Done — the browser pane will pick it up on the next session.")
	fmt.Println("Executable:", p)
	return nil
}

func printOffer(offer browser.InstallOffer) {
	if len(offer.Methods) == 0 {
		fmt.Println("\nNo automatic install method is available on this host.")
		fmt.Println("Install Chromium/Chrome manually, then pass -browser-exec or put it on PATH.")
		return
	}
	fmt.Println("\nInstall options:")
	for _, m := range offer.Methods {
		root := ""
		if m.Root {
			root = " (needs root)"
		}
		fmt.Printf("  %s — %s%s\n      %s\n", m.ID, m.Label, root, m.Detail)
	}
	fmt.Println("\nRun `nusamation browser install` for the first method, or pick one with `nusamation browser install <id>`.")
	if offer.Proot {
		fmt.Println("proot detected: package-manager installs are the only option (no linux/arm64 Chrome build exists), and Chromium runs with --no-sandbox here.")
	}
}
