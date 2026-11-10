// Package browser embeds a headless Chromium for the browser pane: one lazy
// browser process, one isolated BrowserContext per session, one tab per
// session. It mirrors internal/agent layering and does not import chat or
// httpapi — the crop-upload save path is injected by the caller.
package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/jahrulnr/nusamation/internal/ids"
	"github.com/jahrulnr/nusamation/internal/store"
)

var (
	ErrUnavailable = errors.New("browser unavailable")
	ErrBusy        = errors.New("a navigation or annotation is in flight")
	ErrNotFound    = errors.New("browser session not found")
	ErrPickMissed  = errors.New("no element resolved at the point")
	ErrClosed      = errors.New("browser session closed")
)

// NavigateError reports a navigation/load failure (Chromium's reason).
type NavigateError struct{ Reason string }

func (e *NavigateError) Error() string { return e.Reason }

// execCandidates are looked up on PATH when no -browser-exec override is set.
// The unavailable error names them per the API contract.
var execCandidates = []string{
	"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
	"chrome", "headless_shell", "chrome-headless-shell",
}

type SaveAttachmentFunc func(ctx context.Context, conversationID, name, mime string, data []byte) (store.Upload, error)

type Options struct {
	// ExecPath is the Chromium/Chrome executable path (-browser-exec /
	// NUSAMATION_BROWSER_EXEC). Empty triggers PATH discovery plus any
	// platform-specific install-location fallback.
	ExecPath string
	Log      *slog.Logger
	// SaveAttachment persists an annotation crop through the upload path
	// (chat.Service.SaveInboundAttachment).
	SaveAttachment SaveAttachmentFunc
	// DataDir is where pick JSON files persist so composer chips survive a
	// server restart; empty falls back to in-memory only (tests).
	DataDir string
	// ProfileDir roots one persistent Chromium user-data-dir per profile name
	// (<dir>/<name>). Empty keeps the old temp-profile behavior: cookies and
	// storage vanish with each Chromium restart.
	ProfileDir string
	// Store records visit history and saved logins per profile; nil disables
	// persistence (tests).
	Store *store.Store
	// IdleTTL closes sessions with no stream and no commands after this long.
	IdleTTL time.Duration
	// BrowserIdleTTL stops a profile's Chromium once it has had no sessions
	// for this long; the next session relaunches it lazily.
	BrowserIdleTTL time.Duration
}

type Pick struct {
	ID             string
	SessionID      string
	ConversationID string
	Marker         int
	Descriptor     store.AnnotationDescriptor
	Upload         store.Upload
	CreatedAt      int64
}

// browserProc is one running Chromium bound to a profile's user-data-dir.
// Launching is async like the old single-browser path: waiters share
// proc.launching until the process answers or fails.
type browserProc struct {
	name      string
	ctx       context.Context
	stop      context.CancelFunc
	alloc     context.CancelFunc
	idle      time.Time
	launching chan struct{}
	err       error
}

type Manager struct {
	execPath       string
	log            *slog.Logger
	save           SaveAttachmentFunc
	picksDir       string
	profileDir     string
	rec            *store.Store
	idleTTL        time.Duration
	browserIdleTTL time.Duration

	mu           sync.Mutex
	sessions     map[string]*Session
	picks        map[string]*Pick
	browsers     map[string]*browserProc
	closed       bool
	sweepStop    chan struct{}
	sweepStopped chan struct{}
}

func NewManager(opts Options) *Manager {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		execPath:       opts.ExecPath,
		log:            log.With("component", "browser"),
		save:           opts.SaveAttachment,
		picksDir:       opts.DataDir,
		profileDir:     opts.ProfileDir,
		rec:            opts.Store,
		idleTTL:        opts.IdleTTL,
		browserIdleTTL: opts.BrowserIdleTTL,
		sessions:       map[string]*Session{},
		picks:          map[string]*Pick{},
		browsers:       map[string]*browserProc{},
		sweepStop:      make(chan struct{}),
		sweepStopped:   make(chan struct{}),
	}
	if m.idleTTL <= 0 {
		m.idleTTL = 15 * time.Minute
	}
	if m.browserIdleTTL <= 0 {
		m.browserIdleTTL = 2 * time.Minute
	}
	if m.picksDir != "" {
		// Picks outlive their session's process lifetime but not forever —
		// sweep a week's worth so files from dead sessions do not accumulate.
		cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
		if err := sweepPickFiles(m.picksDir, cutoff); err != nil {
			m.log.Warn("stale annotation picks not swept", "error", err)
		}
	}
	go m.sweepLoop()
	return m
}

func (m *Manager) Close() {
	close(m.sweepStop)
	<-m.sweepStopped
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	procs := make([]*browserProc, 0, len(m.browsers))
	for _, p := range m.browsers {
		procs = append(procs, p)
	}
	m.closed = true
	m.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
	for _, p := range procs {
		m.killBrowser(p)
	}
}

// DefaultProfile is the profile pane sessions land in when none is requested.
const DefaultProfile = "default"

// NormalizeProfile reduces a user/agent-supplied profile name to a safe
// directory key: lowercase letters, digits, dash, 1–40 chars.
func NormalizeProfile(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) > 40 {
		out = strings.TrimRight(out[:40], "-_")
	}
	if out == "" {
		return DefaultProfile
	}
	return out
}

// CreateSession opens one isolated BrowserContext with a single tab and starts
// loading url. The shared Chromium is launched lazily on first use.
func (m *Manager) CreateSession(ctx context.Context, conversationID, url string, width, height int) (*Session, error) {
	return m.CreateSessionProfile(ctx, conversationID, url, width, height, DefaultProfile)
}

// CreateSessionProfile opens a session bound to a named browser profile.
// Sessions of one profile share that profile's default browser context, so
// cookies, storage and saved logins persist across sessions and restarts.
func (m *Manager) CreateSessionProfile(ctx context.Context, conversationID, url string, width, height int, profile string) (*Session, error) {
	profile = NormalizeProfile(profile)
	if url == "" {
		url = "about:blank"
	}
	browserCtx, err := m.ensureBrowser(ctx, profile)
	if err != nil {
		return nil, err
	}
	tabCtx, tabCancel, err := m.openTab(browserCtx, url, width, height)
	if err != nil {
		return nil, err
	}
	s := &Session{
		m:              m,
		id:             ids.New("bs"),
		conversationID: conversationID,
		profile:        profile,
		url:            url,
		status:         "active",
		width:          width,
		height:         height,
		device:         DeviceOpts{Width: int64(width), Height: int64(height), DPR: 1},
		createdAt:      time.Now().UnixMilli(),
		tabCtx:         tabCtx,
		tabCancel:      tabCancel,
		browserCtx:     browserCtx,
		ackCh:          make(chan int64, 32),
		maintCh:        make(chan func(), 8),
	}
	s.touch()
	s.refreshNavState()
	m.mu.Lock()
	m.sessions[s.id] = s
	if p := m.browsers[profile]; p != nil {
		p.idle = time.Time{}
	}
	m.mu.Unlock()
	go s.watchNav()
	go s.watchCrash()
	go s.watchDialogs()
	go s.watchLoginBindings()
	go s.watchLoginContexts()
	go s.maintLoop()
	return s, nil
}

// openTab creates a target in the profile's default browser context (shared,
// persistent storage) and installs the session's page hooks.
func (m *Manager) openTab(browserCtx context.Context, url string, width, height int) (context.Context, context.CancelFunc, error) {
	tRes, err := chromedp.CallBrowser(browserCtx, target.CreateTarget,
		target.CreateTargetParams{URL: url, NewWindow: new(true)})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: create target: %v", ErrUnavailable, err)
	}
	tabCtx, tabCancel := chromedp.NewContext(browserCtx, chromedp.WithTargetID(tRes.TargetID))
	setup := func() error {
		if _, err := chromedp.Call(tabCtx, page.Enable, page.EnableParams{}); err != nil {
			return err
		}
		if _, err := chromedp.Call(tabCtx, dom.Enable, dom.EnableParams{}); err != nil {
			return err
		}
		if _, err := chromedp.Call(tabCtx, page.AddScriptToEvaluateOnNewDocument,
			page.AddScriptToEvaluateOnNewDocumentParams{Source: consoleTapJS + ";\n" + selectShimJS + ";\n" + loginTapJS}); err != nil {
			return err
		}
		// The hook only fires on later navigations; run it once for this load.
		if _, err := chromedp.Call(tabCtx, runtime.Evaluate, runtime.EvaluateParams{Expression: consoleTapJS + ";\n" + selectShimJS + ";\n" + loginTapJS}); err != nil {
			return err
		}
		if _, err := chromedp.Call(tabCtx, runtime.AddBinding,
			runtime.AddBindingParams{Name: "__nusaReportLogin"}); err != nil {
			return err
		}
		_, err := chromedp.Call(tabCtx, emulation.SetDeviceMetricsOverride,
			emulation.SetDeviceMetricsOverrideParams{Width: int64(width), Height: int64(height), DeviceScaleFactor: 1, Mobile: false})
		return err
	}
	if err := setup(); err != nil {
		tabCancel()
		return nil, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return tabCtx, tabCancel, nil
}

// MigrateProfile moves a live session onto another browser profile: the old
// target closes, a new one opens in the target profile and reloads the
// session's current URL. The session id and its screencast clients survive.
func (m *Manager) MigrateProfile(ctx context.Context, s *Session, profile string) error {
	profile = NormalizeProfile(profile)
	if profile == s.Profile() {
		return nil
	}
	browserCtx, err := m.ensureBrowser(ctx, profile)
	if err != nil {
		return err
	}
	s.mu.Lock()
	url := s.url
	width, height := s.width, s.height
	s.mu.Unlock()
	tabCtx, tabCancel, err := m.openTab(browserCtx, url, width, height)
	if err != nil {
		return err
	}
	s.swapTab(tabCtx, tabCancel, browserCtx, profile)
	return nil
}

// Profiles lists the profiles that currently hold session-visible data: any
// profile dir on disk plus every live session's profile.
func (m *Manager) Profiles() []string {
	set := map[string]bool{DefaultProfile: true}
	m.mu.Lock()
	for _, s := range m.sessions {
		set[s.profile] = true
	}
	for name := range m.browsers {
		set[name] = true
	}
	m.mu.Unlock()
	if m.profileDir != "" {
		if ents, err := os.ReadDir(m.profileDir); err == nil {
			for _, e := range ents {
				if e.IsDir() {
					set[e.Name()] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ConversationSession returns the live session owned by conversationID, or nil.
func (m *Manager) ConversationSession(conversationID string) *Session {
	if conversationID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *Session
	var bestAt time.Time
	for _, s := range m.sessions {
		if s.conversationID != conversationID {
			continue
		}
		s.mu.Lock()
		status, at := s.status, s.lastActivity
		s.mu.Unlock()
		if status == "crashed" {
			continue
		}
		if best == nil || at.After(bestAt) {
			best, bestAt = s, at
		}
	}
	return best
}

func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

func (m *Manager) List() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// Pick resolves an annotation pick id: memory first, then the pick file —
// picks persist across restarts because the marked screenshot upload does.
func (m *Manager) Pick(id string) (*Pick, bool) {
	if !validPickID(id) {
		return nil, false
	}
	m.mu.Lock()
	p, ok := m.picks[id]
	m.mu.Unlock()
	if ok || m.picksDir == "" {
		return p, ok
	}
	p, err := loadPickFile(m.picksDir, id)
	if err != nil {
		return nil, false
	}
	m.mu.Lock()
	m.picks[p.ID] = p
	m.mu.Unlock()
	return p, true
}

func (m *Manager) addPick(p *Pick) {
	m.mu.Lock()
	m.picks[p.ID] = p
	m.mu.Unlock()
	if m.picksDir == "" {
		return
	}
	if err := savePickFile(m.picksDir, p); err != nil {
		m.log.Warn("annotation pick not persisted; in-memory only", "pick", p.ID, "error", err)
	}
}

func (m *Manager) dropSessionPicks(sessionID string) {
	m.mu.Lock()
	for id, p := range m.picks {
		if p.SessionID == sessionID {
			delete(m.picks, id)
		}
	}
	m.mu.Unlock()
	if m.picksDir != "" {
		if err := deleteSessionPickFiles(m.picksDir, sessionID); err != nil {
			m.log.Warn("annotation picks not cleared", "session", sessionID, "error", err)
		}
	}
}

// ensureBrowser lazily launches the profile's headless Chromium and returns
// its context. The context dies with the process, which is the crash signal.
// The launch runs off m.mu so concurrent Get/List/Pick callers are not
// stalled behind a cold (up to ~20s) start.
func (m *Manager) ensureBrowser(ctx context.Context, profile string) (context.Context, error) {
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, ErrClosed
		}
		p := m.browsers[profile]
		if p == nil {
			p = &browserProc{name: profile}
			m.browsers[profile] = p
		}
		if p.ctx != nil && p.ctx.Err() == nil {
			b := p.ctx
			m.mu.Unlock()
			return b, nil
		}
		if p.launching == nil {
			p.launching = make(chan struct{})
			p.err = nil
			go m.launch(p, p.launching)
		}
		ch := p.launching
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		m.mu.Lock()
		p = m.browsers[profile]
		if p != nil && p.ctx != nil {
			b := p.ctx
			m.mu.Unlock()
			return b, nil
		}
		err := error(nil)
		retry := false
		if p != nil {
			err, retry = p.err, p.launching != nil
		}
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if !retry {
			return nil, ErrUnavailable
		}
	}
}

// launch performs the actual process start and publishes the result (or the
// failure) under m.mu before closing done to wake the waiters.
func (m *Manager) launch(p *browserProc, done chan struct{}) {
	defer close(done)
	path, err := m.resolveExecPath()
	var browserCtx context.Context
	var browserCancel, allocCancel context.CancelFunc
	if err == nil {
		args := []chromedp.ExecAllocatorOption{
			chromedp.ExecPath(path),
			chromedp.Headless,
			chromedp.WindowSize(1280, 800),
			chromedp.NoFirstRun,
			chromedp.NoDefaultBrowserCheck,
			chromedp.DisableGPU,
		}
		if m.profileDir != "" {
			dir := filepath.Join(m.profileDir, p.name)
			if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
				err = fmt.Errorf("%w: profile dir: %v", ErrUnavailable, mkErr)
			} else {
				args = append(args, chromedp.UserDataDir(dir))
			}
		}
		if err == nil {
			var allocCtx context.Context
			allocCtx, allocCancel = chromedp.NewExecAllocator(context.Background(), args...)
			browserCtx, browserCancel = chromedp.NewContext(allocCtx)
			// The allocating call binds the process lifetime to its context, so
			// the launch check runs on browserCtx itself and the timeout only
			// abandons the wait — canceling it would kill the browser.
			errCh := make(chan error, 1)
			go func() {
				_, err := chromedp.CallBrowser(browserCtx, browser.GetVersion, cdp.Empty{})
				errCh <- err
			}()
			select {
			case err = <-errCh:
			case <-time.After(20 * time.Second):
				err = errors.New("launch timed out")
			}
			if err != nil {
				err = fmt.Errorf("%w: launching %s: %v", ErrUnavailable, path, err)
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p.launching = nil
	if err != nil {
		if browserCancel != nil {
			browserCancel()
		}
		if allocCancel != nil {
			allocCancel()
		}
		p.err = err
		return
	}
	p.alloc = allocCancel
	p.ctx = browserCtx
	p.stop = browserCancel
	p.idle = time.Now()
	go m.watchBrowser(p)
	m.log.Info("chromium started", "exec", path, "profile", p.name)
}

// watchBrowser marks a profile's live sessions crashed when its Chromium
// process dies; the context Done fires because the allocator cancels on loss.
func (m *Manager) watchBrowser(p *browserProc) {
	m.mu.Lock()
	ctx := p.ctx
	m.mu.Unlock()
	if ctx == nil {
		return
	}
	<-ctx.Done()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browsers[p.name] != p || p.ctx == nil {
		return
	}
	p.ctx = nil
	p.stop = nil
	p.alloc = nil
	for _, s := range m.sessions {
		if s.profile == p.name {
			s.markCrashed("chromium process exited")
		}
	}
	m.log.Warn("chromium process exited; sessions marked crashed", "profile", p.name)
}

func (m *Manager) killBrowser(p *browserProc) {
	m.mu.Lock()
	stop, alloc := p.stop, p.alloc
	p.ctx = nil
	p.stop = nil
	p.alloc = nil
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	if alloc != nil {
		alloc()
	}
}

func (m *Manager) resolveExecPath() (string, error) {
	if m.execPath != "" {
		return m.execPath, nil
	}
	for _, name := range execCandidates {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	searched := strings.Join(execCandidates, ", ")
	if fb := execFallbackPaths(); len(fb) > 0 {
		searched += ", standard install locations"
		for _, p := range fb {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("%w: no Chromium executable found (searched: %s); set -browser-exec or NUSAMATION_BROWSER_EXEC",
		ErrUnavailable, searched)
}

func (m *Manager) removeSession(s *Session) {
	m.mu.Lock()
	if m.sessions[s.id] == s {
		delete(m.sessions, s.id)
	}
	if p := m.browsers[s.profile]; p != nil && p.ctx != nil {
		inUse := false
		for _, other := range m.sessions {
			if other.profile == s.profile {
				inUse = true
				break
			}
		}
		if !inUse {
			p.idle = time.Now()
		}
	}
	m.mu.Unlock()
	m.dropSessionPicks(s.id)
}

// sweepLoop implements the idle GC: sessions with no open stream and no
// commands for idleTTL are closed; a profile's browser stops when it has been
// session-free for browserIdleTTL.
func (m *Manager) sweepLoop() {
	defer close(m.sweepStopped)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.sweepStop:
			return
		case <-ticker.C:
		}
		var dead []*Session
		var idleProcs []*browserProc
		m.mu.Lock()
		for _, s := range m.sessions {
			if s.idleFor(m.idleTTL) {
				dead = append(dead, s)
			}
		}
		inUse := map[string]bool{}
		for _, s := range m.sessions {
			inUse[s.profile] = true
		}
		for _, p := range m.browsers {
			if p.ctx != nil && !inUse[p.name] && !p.idle.IsZero() && time.Since(p.idle) > m.browserIdleTTL {
				idleProcs = append(idleProcs, p)
			}
		}
		m.mu.Unlock()
		for _, s := range dead {
			s.Close()
		}
		for _, p := range idleProcs {
			m.killBrowser(p)
		}
	}
}

//go:fix inline
func ptr[T any](v T) *T { return new(v) }
