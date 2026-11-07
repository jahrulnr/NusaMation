package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusamation"
)

const (
	DefaultAddr = "127.0.0.1:8787"
	AppName     = "NusaMation"
)

var Version = nusamation.Version

type Config struct {
	Addr            string
	DataDir         string
	PublicURL       string
	BootstrapPass   string
	LogLevel        string
	DisableWhatsApp bool
	OpenBrowser     bool
	Dev             bool
	BrowserExec     string
}

func newFlagSet(cfg *Config) *flag.FlagSet {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	fs := flag.NewFlagSet(AppName, flag.ContinueOnError)
	fs.StringVar(&cfg.Addr, "addr", env("NUSAMATION_ADDR", DefaultAddr), "HTTP listen address")
	fs.StringVar(&cfg.DataDir, "data-dir", env("NUSAMATION_DATA_DIR", filepath.Join(home, ".nusamation")), "data directory")
	fs.StringVar(&cfg.PublicURL, "public-url", env("NUSAMATION_PUBLIC_URL", ""), "public base URL used in links (e.g. https://agent.example.com)")
	fs.StringVar(&cfg.BootstrapPass, "password", env("NUSAMATION_PASSWORD", ""), "admin password for first login (otherwise generated)")
	fs.StringVar(&cfg.LogLevel, "log-level", env("NUSAMATION_LOG_LEVEL", "info"), "debug, info, warn, error")
	fs.BoolVar(&cfg.DisableWhatsApp, "no-whatsapp", envBool("NUSAMATION_NO_WHATSAPP", false), "disable the WhatsApp channel")
	fs.BoolVar(&cfg.OpenBrowser, "open", envBool("NUSAMATION_OPEN", false), "open the web UI in a browser after starting")
	fs.BoolVar(&cfg.Dev, "dev", envBool("NUSAMATION_DEV", false), "development mode (no asset caching)")
	fs.StringVar(&cfg.BrowserExec, "browser-exec", env("NUSAMATION_BROWSER_EXEC", ""), "Chromium/Chrome executable for the embedded browser pane")
	return fs
}

// FlagUsage writes the server flag list to w for help output.
func FlagUsage(w io.Writer) {
	fs := newFlagSet(&Config{})
	fs.SetOutput(w)
	fs.PrintDefaults()
}

func Parse(args []string) (Config, error) {
	cfg := Config{}
	fs := newFlagSet(&cfg)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() > 0 {
		return cfg, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	if cfg.DataDir == "" {
		return cfg, errors.New("data directory is required")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return cfg, fmt.Errorf("create data dir: %w", err)
	}
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return cfg, err
	}
	cfg.DataDir = abs
	if cfg.PublicURL != "" {
		cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	}
	return cfg, nil
}

func (c Config) DatabasePath() string   { return filepath.Join(c.DataDir, "nusamation.db") }
func (c Config) WhatsAppDBPath() string { return filepath.Join(c.DataDir, "whatsapp.db") }
func (c Config) SecretPath() string     { return filepath.Join(c.DataDir, "secret.key") }
func (c Config) UploadDir() string      { return filepath.Join(c.DataDir, "uploads") }
func (c Config) WorkspaceDir() string   { return filepath.Join(c.DataDir, "workspace") }
func (c Config) DocsDir() string        { return filepath.Join(c.DataDir, "docs") }
func (c Config) LogDir() string         { return filepath.Join(c.DataDir, "logs") }

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on", "y":
		return true
	case "0", "false", "no", "off", "n":
		return false
	default:
		return def
	}
}
