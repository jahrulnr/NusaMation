// Package update checks GitHub releases for a newer NusaMation version and
// self-installs it: download the release asset, verify the SHA-256 checksum,
// swap the binary and restart the process. The web UI follows the progress
// over "update.status" events and reloads once the new server answers.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/notify"
	"github.com/jahrulnr/nusamation/internal/store"
)

const (
	defaultRepo     = "jahrulnr/NusaMation"
	defaultAPIBase  = "https://api.github.com"
	defaultInterval = 6 * time.Hour
	firstCheckDelay = 20 * time.Second
)

// Status is the JSON payload of GET /api/update and the "update.status"
// WebSocket event. Progress is 0-100 within the current stage.
type Status struct {
	Current    string `json:"current"`
	Latest     string `json:"latest,omitempty"`
	Available  bool   `json:"available"`
	CanApply   bool   `json:"canApply"`
	ApplyBlock string `json:"applyBlock,omitempty"`
	Checking   bool   `json:"checking"`
	Applying   bool   `json:"applying"`
	Stage      string `json:"stage,omitempty"`
	Progress   int    `json:"progress"`
	Detail     string `json:"detail,omitempty"`
	Error      string `json:"error,omitempty"`
	CheckedAt  int64  `json:"checkedAt,omitempty"`
	ReleaseURL string `json:"releaseUrl,omitempty"`
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type releaseInfo struct {
	Tag    string
	URL    string
	Assets []releaseAsset
}

type Options struct {
	Store     *store.Store
	Bus       events.Broadcaster
	Notify    *notify.Broadcaster // nil disables the OS push on new releases
	Log       *slog.Logger
	Current   string // running version; defaults to the embedded one
	Repo      string // owner/name; defaults to the upstream repo
	APIBase   string // GitHub API base; tests override with httptest
	Client    *http.Client
	Interval  time.Duration // poll interval; defaults to 6h
	OnRestart func()        // e.g. mute notifications before exec
}

type Manager struct {
	store     *store.Store
	bus       events.Broadcaster
	notifier  *notify.Broadcaster
	log       *slog.Logger
	repo      string
	apiBase   string
	client    *http.Client
	interval  time.Duration
	onRestart func()

	mu       sync.Mutex
	status   Status
	notified string

	exePath   string
	applyWhy  string                         // why self-update is refused, when CanApply is false
	restartFn func(exe, staged string) error // platform hook; tests replace
}

func New(opts Options) *Manager {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Bus == nil {
		opts.Bus = events.Nop{}
	}
	if opts.Repo == "" {
		opts.Repo = defaultRepo
	}
	if opts.APIBase == "" {
		opts.APIBase = defaultAPIBase
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultInterval
	}
	m := &Manager{
		store: opts.Store, bus: opts.Bus, notifier: opts.Notify,
		log: opts.Log.With("component", "update"), repo: opts.Repo,
		apiBase: opts.APIBase, client: opts.Client, interval: opts.Interval,
		onRestart: opts.OnRestart, restartFn: restartSelf,
	}
	m.status.Current = opts.Current
	if m.status.Current == "" {
		m.status.Current = config.Version
	}
	m.probeExecutable()
	m.rehydrate()
	return m
}

// probeExecutable resolves the running binary and decides whether an in-place
// self-update is possible (a `go run` temp build is not updatable, and only
// platforms the release workflow ships can self-update).
func (m *Manager) probeExecutable() {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		m.applyWhy = "no prebuilt release for " + runtime.GOOS + "/" + runtime.GOARCH
		m.status.CanApply = false
		m.status.ApplyBlock = m.applyWhy
		return
	}
	exe, err := os.Executable()
	if err == nil && exe != "" {
		// Rename replaces the directory entry — resolve links so the real
		// binary is swapped, not the symlink pointing at it.
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
	}
	tmp := filepath.Clean(os.TempDir())
	switch {
	case err != nil:
		m.applyWhy = "cannot locate the running executable"
	case exe == "":
		m.applyWhy = "cannot locate the running executable"
	case strings.HasPrefix(filepath.Clean(exe), tmp+string(os.PathSeparator)):
		m.applyWhy = "development build — update via the installer or rebuild"
	default:
		m.exePath = exe
	}
	m.status.CanApply = m.exePath != ""
	m.status.ApplyBlock = m.applyWhy
}

// rehydrate restores the last known release info so the UI can show it before
// the first poll finishes.
func (m *Manager) rehydrate() {
	if m.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var saved struct {
		Latest     string `json:"latest"`
		Available  bool   `json:"available"`
		CheckedAt  int64  `json:"checkedAt"`
		ReleaseURL string `json:"releaseUrl"`
	}
	if err := m.store.GetJSONSetting(ctx, "update.status", &saved); err == nil {
		m.status.Latest = saved.Latest
		m.status.Available = saved.Available && saved.Latest != "" && newerVersion(saved.Latest, m.status.Current)
		m.status.CheckedAt = saved.CheckedAt
		m.status.ReleaseURL = saved.ReleaseURL
	}
	if v, err := m.store.GetSetting(ctx, "update.notified"); err == nil {
		m.notified = v
	}
}

// Status returns a consistent snapshot for the API.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) emit() {
	m.bus.Broadcast(events.New("update.status", "", m.Status()))
}

// Run polls the release feed: once shortly after boot, then every interval.
func (m *Manager) Run(ctx context.Context) {
	first := time.NewTimer(firstCheckDelay)
	defer first.Stop()
	tick := time.NewTicker(m.interval)
	defer tick.Stop()
	check := func() {
		c, cancel := context.WithTimeout(ctx, 40*time.Second)
		m.Check(c)
		cancel()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			check()
		case <-tick.C:
			check()
		}
	}
}

// Check queries the latest GitHub release once and updates the status.
func (m *Manager) Check(ctx context.Context) Status {
	m.mu.Lock()
	if m.status.Applying || m.status.Checking {
		s := m.status
		m.mu.Unlock()
		return s
	}
	m.status.Checking = true
	m.status.Stage = "checking"
	m.status.Error = ""
	m.mu.Unlock()
	m.emit()

	rel, err := m.fetchLatest(ctx)

	m.mu.Lock()
	m.status.Checking = false
	m.status.Stage = ""
	notifyTag := m.applyCheckLocked(rel, err)
	s := m.status
	m.mu.Unlock()
	m.persist(s)
	m.persistNotified(notifyTag)
	m.emit()
	m.notifyRelease(notifyTag)
	return s
}

// applyCheckLocked folds a fetch result into the status and returns the
// release tag that deserves an OS notification ("" when none). Callers hold
// m.mu; the dedupe write happens later in persistNotified.
func (m *Manager) applyCheckLocked(rel *releaseInfo, err error) string {
	m.status.CheckedAt = time.Now().UnixMilli()
	if err != nil {
		m.status.Error = err.Error()
		return ""
	}
	m.status.Error = ""
	if rel == nil || rel.Tag == "" {
		m.status.Latest = ""
		m.status.Available = false
		return ""
	}
	m.status.Latest = strings.TrimPrefix(rel.Tag, "v")
	m.status.ReleaseURL = rel.URL
	m.status.Available = newerVersion(m.status.Latest, m.status.Current)
	if !m.status.Available || rel.Tag == m.notified {
		return ""
	}
	m.notified = rel.Tag
	return rel.Tag
}

// notifyRelease pushes one OS notification per newly seen version. It runs
// outside the mutex: Notify shells out to the OS and can block.
func (m *Manager) notifyRelease(tag string) {
	if tag == "" || m.notifier == nil {
		return
	}
	st := m.Status()
	ctx, cancel := context.WithTimeout(context.Background(), notify.DeliveryTimeout)
	defer cancel()
	m.notifier.Send(ctx, notify.Notification{
		Title:   "NusaMation update available",
		Body:    fmt.Sprintf("v%s is available — you're running v%s.", st.Latest, st.Current),
		Urgency: notify.UrgencyLow, Icon: "system-software-update",
	})
}

// persist* run outside the status mutex: SQLite writes can stall and Status()
// must stay cheap for the API and event fan-out.
func (m *Manager) persist(st Status) {
	if m.store == nil {
		return
	}
	_ = m.store.SetJSONSetting(context.Background(), "update.status", map[string]any{
		"latest": st.Latest, "available": st.Available,
		"checkedAt": st.CheckedAt, "releaseUrl": st.ReleaseURL,
	})
}

func (m *Manager) persistNotified(tag string) {
	if tag == "" || m.store == nil {
		return
	}
	_ = m.store.SetSetting(context.Background(), "update.notified", tag)
}

// fetchLatest returns the newest published release, or nil when the repo has
// no releases yet.
func (m *Manager) fetchLatest(ctx context.Context) (*releaseInfo, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", m.apiBase, m.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "nusamation/"+m.status.Current)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("release check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release check failed (HTTP %d)", resp.StatusCode)
	}
	var body struct {
		Tag    string         `json:"tag_name"`
		URL    string         `json:"html_url"`
		Assets []releaseAsset `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("release check failed: %w", err)
	}
	return &releaseInfo{Tag: body.Tag, URL: body.URL, Assets: body.Assets}, nil
}

// Apply starts the self-update in the background. Progress is reported over
// update.status events; on success the process restarts itself.
func (m *Manager) Apply() error {
	m.mu.Lock()
	if m.status.Applying {
		m.mu.Unlock()
		return errors.New("an update is already installing")
	}
	if m.status.Checking {
		m.mu.Unlock()
		return errors.New("an update check is in progress")
	}
	if !m.status.CanApply {
		reason := m.status.ApplyBlock
		m.mu.Unlock()
		if reason == "" {
			reason = "self-update is not possible for this installation"
		}
		return errors.New(reason)
	}
	m.status.Applying = true
	m.status.Error = ""
	m.status.Stage = ""
	m.status.Progress = 0
	m.mu.Unlock()
	m.emit()
	go m.apply(context.Background())
	return nil
}

func (m *Manager) setStage(stage, detail string, progress int) {
	m.mu.Lock()
	m.status.Stage = stage
	m.status.Detail = detail
	if progress >= 0 {
		m.status.Progress = progress
	}
	m.mu.Unlock()
	m.emit()
}

func (m *Manager) fail(stage string, err error) {
	m.mu.Lock()
	m.status.Applying = false
	m.status.Checking = false
	m.status.Stage = ""
	m.status.Error = fmt.Sprintf("%s failed: %v", stage, err)
	m.mu.Unlock()
	m.emit()
	m.log.Error("update failed", "stage", stage, "error", err)
}

func (m *Manager) apply(ctx context.Context) {
	// Re-fetch so the installed version is whatever is newest right now.
	m.setStage("checking", "", 0)
	rel, err := m.fetchLatest(ctx)
	if err != nil {
		m.fail("release check", err)
		return
	}
	m.mu.Lock()
	notifyTag := m.applyCheckLocked(rel, nil)
	available := m.status.Available
	st := m.status
	m.mu.Unlock()
	m.persist(st)
	m.persistNotified(notifyTag)
	m.notifyRelease(notifyTag)
	if rel == nil || !available {
		m.fail("release check", errors.New("no newer release is available"))
		return
	}

	asset := findAsset(rel, assetName())
	sums := findAsset(rel, "SHA256SUMS")
	if asset == nil || sums == nil {
		m.fail("release check", fmt.Errorf("release %s has no %s asset or SHA256SUMS", rel.Tag, assetName()))
		return
	}

	staged := m.exePath + ".new"
	defer os.Remove(staged)
	m.setStage("downloading", asset.Name, 0)
	if err := m.download(ctx, asset.URL, staged, func(frac float64) {
		m.setStage("downloading", asset.Name, int(frac*100))
	}); err != nil {
		m.fail("download", err)
		return
	}

	m.setStage("verifying", "", 0)
	if err := m.verify(ctx, *sums, staged); err != nil {
		m.fail("checksum verification", err)
		return
	}

	m.setStage("installing", "", 0)
	if err := installBinary(m.exePath, staged); err != nil {
		m.fail("install", err)
		return
	}

	m.setStage("restarting", "Restarting to finish the update…", 100)
	m.persist(m.Status())
	if m.onRestart != nil {
		m.onRestart()
	}
	// Give the WebSocket a moment to flush the "restarting" event.
	time.Sleep(700 * time.Millisecond)
	m.log.Info("restarting to finish update", "version", m.Status().Latest, "exe", m.exePath)
	// Note: on success restartFn never returns — on Unix it exec()s, on
	// Windows it exits so the helper can swap the running exe; the deferred
	// os.Remove(staged) intentionally does not run.
	if err := m.restartFn(m.exePath, staged); err != nil {
		m.fail("restart", err)
	}
}

func assetName() string {
	name := fmt.Sprintf("nusamation-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func findAsset(rel *releaseInfo, name string) *releaseAsset {
	for i := range rel.Assets {
		if rel.Assets[i].Name == name && rel.Assets[i].URL != "" {
			return &rel.Assets[i]
		}
	}
	return nil
}

func (m *Manager) download(ctx context.Context, url, dest string, progress func(frac float64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "nusamation/"+m.status.Current)
	// A whole-body download needs a longer timeout than the release-API
	// client, but keep its transport (proxies, TLS, test hooks).
	client := *m.client
	if client.Timeout < 5*time.Minute {
		client.Timeout = 5 * time.Minute
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed (HTTP %d)", resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()

	var written int64
	buf := make([]byte, 256<<10)
	last := -1
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			if resp.ContentLength > 0 {
				pct := int(written * 100 / resp.ContentLength)
				if pct != last {
					last = pct
					progress(float64(written) / float64(resp.ContentLength))
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return f.Sync()
}

// verify hashes the staged binary and compares it to the SHA256SUMS line for
// the platform asset — the same check the installers run.
func (m *Manager) verify(ctx context.Context, sums releaseAsset, staged string) error {
	tmp := staged + ".sums"
	defer os.Remove(tmp)
	if err := m.download(ctx, sums.URL, tmp, func(float64) {}); err != nil {
		return err
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		return err
	}
	want := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == assetName() {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return errors.New("no checksum for " + assetName() + " in SHA256SUMS")
	}
	f, err := os.Open(staged)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch (got %s, want %s)", got, want)
	}
	return nil
}
