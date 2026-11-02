package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/channels"
	webchannel "github.com/jahrulnr/nusamation/internal/channels/web"
	"github.com/jahrulnr/nusamation/internal/channels/whatsapp"
	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/httpapi"
	"github.com/jahrulnr/nusamation/internal/realtime"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

type App struct {
	cfg      config.Config
	log      *slog.Logger
	store    *store.Store
	agents   *agent.Manager
	channels *channels.Manager
	chat     *chat.Service
	hub      *realtime.Hub
	logFile  *os.File
}

func Run(ctx context.Context, cfg config.Config) error {
	a, err := New(cfg)
	if err != nil {
		return err
	}
	defer a.Close()
	return a.Serve(ctx)
}

func New(cfg config.Config) (*App, error) {
	for _, dir := range []string{cfg.DataDir, cfg.UploadDir(), cfg.WorkspaceDir(), cfg.LogDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	logger, logFile, err := newLogger(cfg)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.DatabasePath())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	sealer, err := security.LoadOrCreateSealer(cfg.SecretPath())
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("load secret key: %w", err)
	}
	st.SetSealer(sealer)

	password, created, err := auth.EnsureBootstrap(context.Background(), st, cfg.BootstrapPass)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("auth bootstrap: %w", err)
	}
	if err := seedWorkspace(cfg.WorkspaceDir()); err != nil {
		logger.Warn("could not seed workspace", "error", err)
	}
	if err := seedAgent(context.Background(), st, cfg.WorkspaceDir(), logger); err != nil {
		logger.Warn("could not seed default agent", "error", err)
	}
	if err := seedSettings(context.Background(), st); err != nil {
		logger.Warn("could not seed settings", "error", err)
	}

	hub := realtime.NewHub(logger)
	agentMgr := agent.NewManager(st, logger)
	chanMgr := channels.NewManager(hub, logger)
	chanMgr.Register(webchannel.New())
	chatSvc := chat.New(chat.Options{
		Store: st, Agents: agentMgr, Channels: chanMgr, Events: hub, Log: logger,
		UploadDir: cfg.UploadDir(), Workspace: cfg.WorkspaceDir(),
	})
	if !cfg.DisableWhatsApp {
		if err := registerWhatsApp(chanMgr, chatSvc, st, cfg, logger); err != nil {
			logger.Warn("WhatsApp channel disabled", "error", err)
		}
	}
	agentMgr.SetSink(chatSvc)
	if err := chatSvc.Start(context.Background()); err != nil {
		logger.Warn("startup recovery failed", "error", err)
	}

	a := &App{
		cfg: cfg, log: logger, store: st, agents: agentMgr, channels: chanMgr,
		chat: chatSvc, hub: hub, logFile: logFile,
	}
	if created {
		printBootstrap(cfg, password)
	}
	return a, nil
}

func (a *App) Serve(ctx context.Context) error {
	api, err := httpapi.New(httpapi.Options{
		Config: a.cfg, Store: a.store, Chat: a.chat, Agents: a.agents,
		Channels: a.channels, Hub: a.hub, Log: a.log,
	})
	if err != nil {
		return err
	}
	handler := api.Handler()
	ln, err := net.Listen("tcp", a.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.cfg.Addr, err)
	}
	url := a.publicURL(ln.Addr())
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	a.log.Info("NusaMation is ready", "url", url, "addr", ln.Addr().String(), "data", a.cfg.DataDir, "version", config.Version)
	a.channels.StartAll(ctx)
	if a.cfg.OpenBrowser {
		go openBrowser(url)
	}
	select {
	case <-ctx.Done():
	case err := <-errc:
		a.log.Error("http server failed", "error", err)
		_ = srv.Close()
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	a.channels.StopAll()
	a.agents.StopAll()
	a.log.Info("NusaMation stopped")
	return nil
}

func (a *App) Close() {
	if a.channels != nil {
		a.channels.StopAll()
	}
	if a.agents != nil {
		a.agents.StopAll()
	}
	if a.store != nil {
		_ = a.store.Close()
	}
	if a.logFile != nil {
		_ = a.logFile.Close()
	}
}

func (a *App) publicURL(addr net.Addr) string {
	if a.cfg.PublicURL != "" {
		return a.cfg.PublicURL
	}
	host := a.cfg.Addr
	if addr != nil {
		host = addr.String()
	}
	if strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1:" + strings.TrimPrefix(host, "0.0.0.0:")
	}
	if strings.HasPrefix(host, "[::]:") {
		host = "127.0.0.1:" + strings.TrimPrefix(host, "[::]:")
	}
	if !strings.Contains(host, ":") {
		host = "127.0.0.1:8787"
	}
	return "http://" + host
}

func newLogger(cfg config.Config) (*slog.Logger, *os.File, error) {
	var level slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	f, err := os.OpenFile(filepath.Join(cfg.LogDir(), "nusamation.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}
	w := io.MultiWriter(os.Stdout, f)
	var h slog.Handler
	if cfg.Dev {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h).With("app", "nusamation"), f, nil
}

func seedWorkspace(dir string) error {
	readme := filepath.Join(dir, "README.md")
	if _, err := os.Stat(readme); os.IsNotExist(err) {
		content := `# NusaMation workspace

This is your personal agent's working directory. Files created by the agent live
here. The web control center and every connected chat channel share this
workspace.

## Tips

- Keep secrets in agent environment variables (Settings → Agents), not in files.
- MCP servers can be configured per agent in the control center.
- Use absolute paths in prompts when referring to files outside this workspace.
`
		if err := os.WriteFile(readme, []byte(content), 0o600); err != nil {
			return err
		}
	}
	agents := filepath.Join(dir, "AGENTS.md")
	if _, err := os.Stat(agents); os.IsNotExist(err) {
		content := `# Agent notes

You are operating inside a NusaMation workspace.

- The user may reach you from the web UI or from a chat channel. The web UI sees
  your full stream; chat channels receive a concise final digest.
- Ask before destructive actions unless your approval policy says otherwise.
- Prefer small, verifiable steps. Report what you changed and how you checked it.
- When you need a tool that is not available, explain the missing dependency
  instead of pretending it worked.
`
		if err := os.WriteFile(agents, []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func seedAgent(ctx context.Context, st *store.Store, workspace string, log *slog.Logger) error {
	list, err := st.ListAgents(ctx)
	if err != nil {
		return err
	}
	for _, a := range list {
		if a.Enabled {
			return nil
		}
	}
	path, err := exec.LookPath("devin")
	if err != nil {
		log.Info("Devin CLI not found; add an ACP agent in the web UI")
		return nil
	}
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Devin", Slug: "devin", Description: "Cognition Devin over ACP",
		Command: path, Args: []string{"acp"}, Cwd: workspace, Icon: "sparkles", Color: "amber",
		Enabled: &enabled, ApprovalPolicy: "ask",
	})
	if err != nil {
		return err
	}
	log.Info("seeded default agent", "agent", ag.Slug, "command", path)
	return nil
}

func seedSettings(ctx context.Context, st *store.Store) error {
	if _, err := st.GetSetting(ctx, "app.installed_at"); err != nil {
		_ = st.SetSetting(ctx, "app.installed_at", fmt.Sprintf("%d", time.Now().UnixMilli()))
	}
	if _, err := st.GetSetting(ctx, "routing.defaults"); err != nil {
		_ = st.SetJSONSetting(ctx, "routing.defaults", store.DefaultRouting())
	}
	return nil
}

func registerWhatsApp(mgr *channels.Manager, handler channels.InboundHandler, st *store.Store, cfg config.Config, log *slog.Logger) error {
	wa, err := whatsapp.New(st, cfg, mgr, log)
	if err != nil {
		return err
	}
	wa.SetHandler(handler)
	mgr.Register(wa)
	return nil
}

func printBootstrap(cfg config.Config, password string) {
	fmt.Println()
	fmt.Println("  ╭──────────────────────────────────────────────────────╮")
	fmt.Println("  │  NusaMation is ready                                 │")
	fmt.Println("  ╰──────────────────────────────────────────────────────╯")
	fmt.Printf("  URL:      http://%s\n", cfg.Addr)
	fmt.Printf("  Password: %s\n", password)
	fmt.Println()
	fmt.Println("  Save this password. It is shown only once.")
	fmt.Printf("  Data:     %s\n", cfg.DataDir)
	fmt.Println()
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return
		}
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
