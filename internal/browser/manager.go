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
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
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
	// NUSAMATION_BROWSER_EXEC). Empty triggers PATH discovery.
	ExecPath string
	Log      *slog.Logger
	// SaveAttachment persists an annotation crop through the upload path
	// (chat.Service.SaveInboundAttachment).
	SaveAttachment SaveAttachmentFunc
	// IdleTTL closes sessions with no stream and no commands after this long.
	IdleTTL time.Duration
	// BrowserIdleTTL stops the shared Chromium once it has had no sessions
	// for this long; the next session relaunches it lazily.
	BrowserIdleTTL time.Duration
}

type Pick struct {
	ID             string
	SessionID      string
	ConversationID string
	BackendNodeID  int64
	Descriptor     store.AnnotationDescriptor
	Upload         store.Upload
	CreatedAt      int64
}

type Manager struct {
	execPath       string
	log            *slog.Logger
	save           SaveAttachmentFunc
	idleTTL        time.Duration
	browserIdleTTL time.Duration

	mu           sync.Mutex
	sessions     map[string]*Session
	picks        map[string]*Pick
	allocCancel  context.CancelFunc
	browserCtx   context.Context
	browserStop  context.CancelFunc
	browserIdle  time.Time
	closed       bool
	launching    chan struct{}
	launchErr    error
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
		idleTTL:        opts.IdleTTL,
		browserIdleTTL: opts.BrowserIdleTTL,
		sessions:       map[string]*Session{},
		picks:          map[string]*Pick{},
		sweepStop:      make(chan struct{}),
		sweepStopped:   make(chan struct{}),
	}
	if m.idleTTL <= 0 {
		m.idleTTL = 15 * time.Minute
	}
	if m.browserIdleTTL <= 0 {
		m.browserIdleTTL = 2 * time.Minute
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
	m.closed = true
	m.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
	m.killBrowser()
}

// CreateSession opens one isolated BrowserContext with a single tab and starts
// loading url. The shared Chromium is launched lazily on first use.
func (m *Manager) CreateSession(ctx context.Context, conversationID, url string, width, height int) (*Session, error) {
	if url == "" {
		url = "about:blank"
	}
	browserCtx, err := m.ensureBrowser(ctx)
	if err != nil {
		return nil, err
	}
	bcRes, err := chromedp.CallBrowser(browserCtx, target.CreateBrowserContext,
		target.CreateBrowserContextParams{DisposeOnDetach: ptr(true)})
	if err != nil {
		return nil, fmt.Errorf("%w: create browser context: %v", ErrUnavailable, err)
	}
	tRes, err := chromedp.CallBrowser(browserCtx, target.CreateTarget,
		target.CreateTargetParams{URL: url, BrowserContextID: bcRes.BrowserContextID, NewWindow: ptr(true)})
	if err != nil {
		_, _ = chromedp.CallBrowser(browserCtx, target.DisposeBrowserContext,
			target.DisposeBrowserContextParams{BrowserContextID: bcRes.BrowserContextID})
		return nil, fmt.Errorf("%w: create target: %v", ErrUnavailable, err)
	}
	tabCtx, tabCancel := chromedp.NewContext(browserCtx, chromedp.WithTargetID(tRes.TargetID))
	setup := func() error {
		if _, err := chromedp.Call(tabCtx, page.Enable, page.EnableParams{}); err != nil {
			return err
		}
		if _, err := chromedp.Call(tabCtx, dom.Enable, dom.EnableParams{}); err != nil {
			return err
		}
		_, err := chromedp.Call(tabCtx, emulation.SetDeviceMetricsOverride,
			emulation.SetDeviceMetricsOverrideParams{Width: int64(width), Height: int64(height), DeviceScaleFactor: 1, Mobile: false})
		return err
	}
	if err := setup(); err != nil {
		tabCancel()
		_, _ = chromedp.CallBrowser(browserCtx, target.DisposeBrowserContext,
			target.DisposeBrowserContextParams{BrowserContextID: bcRes.BrowserContextID})
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s := &Session{
		m:              m,
		id:             ids.New("bs"),
		conversationID: conversationID,
		url:            url,
		status:         "active",
		width:          width,
		height:         height,
		createdAt:      time.Now().UnixMilli(),
		tabCtx:         tabCtx,
		tabCancel:      tabCancel,
		browserCtx:     browserCtx,
		browserCtxID:   bcRes.BrowserContextID,
		ackCh:          make(chan int64, 32),
		maintCh:        make(chan func(), 8),
	}
	s.touch()
	s.refreshNavState()
	m.mu.Lock()
	m.sessions[s.id] = s
	m.browserIdle = time.Time{}
	m.mu.Unlock()
	go s.watchNav()
	go s.watchCrash()
	go s.maintLoop()
	return s, nil
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

// Pick resolves an annotation pick id in the manager-wide pick store.
func (m *Manager) Pick(id string) (*Pick, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.picks[id]
	return p, ok
}

func (m *Manager) addPick(p *Pick) {
	m.mu.Lock()
	m.picks[p.ID] = p
	m.mu.Unlock()
}

func (m *Manager) dropSessionPicks(sessionID string) {
	m.mu.Lock()
	for id, p := range m.picks {
		if p.SessionID == sessionID {
			delete(m.picks, id)
		}
	}
	m.mu.Unlock()
}

// ensureBrowser lazily launches the shared headless Chromium and returns its
// context. The context dies with the process, which is the crash signal.
// The launch runs off m.mu so concurrent Get/List/Pick callers are not
// stalled behind a cold (up to ~20s) start.
func (m *Manager) ensureBrowser(ctx context.Context) (context.Context, error) {
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, ErrClosed
		}
		if m.browserCtx != nil && m.browserCtx.Err() == nil {
			b := m.browserCtx
			m.mu.Unlock()
			return b, nil
		}
		if m.launching == nil {
			m.launching = make(chan struct{})
			m.launchErr = nil
			go m.launch(m.launching)
		}
		ch := m.launching
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		m.mu.Lock()
		b, err, retry := m.browserCtx, m.launchErr, m.launching != nil
		m.mu.Unlock()
		if b != nil {
			return b, nil
		}
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
func (m *Manager) launch(done chan struct{}) {
	defer close(done)
	path, err := m.resolveExecPath()
	var browserCtx context.Context
	var browserCancel, allocCancel context.CancelFunc
	if err == nil {
		var allocCtx context.Context
		allocCtx, allocCancel = chromedp.NewExecAllocator(context.Background(),
			chromedp.ExecPath(path),
			chromedp.Headless,
			chromedp.WindowSize(1280, 800),
			chromedp.NoFirstRun,
			chromedp.NoDefaultBrowserCheck,
			chromedp.DisableGPU,
		)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.launching = nil
	if err != nil {
		if browserCancel != nil {
			browserCancel()
		}
		if allocCancel != nil {
			allocCancel()
		}
		m.launchErr = err
		return
	}
	m.allocCancel = allocCancel
	m.browserCtx = browserCtx
	m.browserStop = browserCancel
	m.browserIdle = time.Now()
	go m.watchBrowser(browserCtx)
	m.log.Info("chromium started", "exec", path)
}

// watchBrowser marks every live session crashed when the Chromium process
// dies; the context Done fires because the allocator cancels it on loss.
func (m *Manager) watchBrowser(browserCtx context.Context) {
	<-browserCtx.Done()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browserCtx != browserCtx {
		return
	}
	m.browserCtx = nil
	m.browserStop = nil
	m.allocCancel = nil
	for _, s := range m.sessions {
		s.markCrashed("chromium process exited")
	}
	m.log.Warn("chromium process exited; sessions marked crashed")
}

func (m *Manager) killBrowser() {
	m.mu.Lock()
	browserCancel := m.browserStop
	allocCancel := m.allocCancel
	m.browserCtx = nil
	m.browserStop = nil
	m.allocCancel = nil
	m.mu.Unlock()
	if browserCancel != nil {
		browserCancel()
	}
	if allocCancel != nil {
		allocCancel()
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
	return "", fmt.Errorf("%w: no Chromium executable found (searched: %s); set -browser-exec or NUSAMATION_BROWSER_EXEC",
		ErrUnavailable, strings.Join(execCandidates, ", "))
}

func (m *Manager) removeSession(s *Session) {
	m.mu.Lock()
	if m.sessions[s.id] == s {
		delete(m.sessions, s.id)
	}
	empty := len(m.sessions) == 0
	if empty && m.browserCtx != nil {
		m.browserIdle = time.Now()
	}
	m.mu.Unlock()
	m.dropSessionPicks(s.id)
}

// sweepLoop implements the idle GC: sessions with no open stream and no
// commands for idleTTL are closed; the shared browser stops when it has been
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
		m.mu.Lock()
		for _, s := range m.sessions {
			if s.idleFor(m.idleTTL) {
				dead = append(dead, s)
			}
		}
		idleBrowser := m.browserCtx != nil && len(m.sessions) == 0 &&
			!m.browserIdle.IsZero() && time.Since(m.browserIdle) > m.browserIdleTTL
		m.mu.Unlock()
		for _, s := range dead {
			s.Close()
		}
		if idleBrowser {
			m.killBrowser()
		}
	}
}

func ptr[T any](v T) *T { return &v }
