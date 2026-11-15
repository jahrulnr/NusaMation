package browser

// Capture sources feeding the active Recorder (recording Phase B):
//
//	L1 in-page tap — recordingTapJS buffers DOM events in window.__nusaRecBuf;
//	    they reach the Recorder through the __nusaRecPush binding (preferred)
//	    or the ~1s evalQuiet drain of __nusaRecTake (covers contexts where the
//	    binding is not installed yet).
//	L3 CDP events — root-frame navigations (watchNav), the Network domain
//	    (enabled only while recording), and browser-level download events.
//
// Everything is best-effort: a nil recorder or a marshal/parse failure drops
// the event, and nothing here ever blocks input dispatch or the screencast.

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const (
	// recDrainInterval polls the in-page buffer — the fallback for contexts
	// where the __nusaRecPush binding is not installed yet.
	recDrainInterval = time.Second
	// recNetPendingCap bounds the request-id table; overflow drops one
	// pending entry's bookkeeping, never the recording itself.
	recNetPendingCap = 1024
	// recDlGuidCap bounds attributed downloads per capture epoch.
	recDlGuidCap = 256
)

// recCapture bundles the per-tab capture state for one recording epoch.
// Watchers are bound to a ctx derived from the tab ctx, so swapTab's ctx
// teardown ends them; the waiter started in startRecordingCapture then
// tears the bundle down exactly once. The network table and the
// attributed-download set live here so a restarted epoch never reuses a
// stale one's bookkeeping.
type recCapture struct {
	rec        *Recorder
	tabCtx     context.Context
	browserCtx context.Context
	rootFrame  cdp.FrameID
	cancel     context.CancelFunc
	once       sync.Once
	// teardownDone closes when the once-guarded teardown finishes; the next
	// capture waits on it before touching the shared browser context so an
	// old "restore default" can never land after the new "allowAndName".
	teardownDone chan struct{}

	netMu   sync.Mutex
	pending map[network.RequestID]*recNetReq

	dlMu  sync.Mutex
	guids map[string]bool
	// dlKey is the browser this capture armed SetDownloadBehavior on (nil
	// when it never did); it keys the shared arm list in recDlArms.
	dlKey *chromedp.Browser
}

// SetDownloadBehavior applies to the profile's whole default browser
// context, so sessions recording on the same browser process share one
// setting and the last arm wins. recDlArms lists the armed captures per
// browser; a teardown touches the context only when its capture is the
// current (last) holder, re-arming the previous holder's downloads dir — or
// the CDP default when none is left — so an older capture's restore can
// never kill a sibling's live capture and a newer capture's teardown hands
// the behavior back rather than dropping it. recDlMu also covers the CDP
// calls themselves: it keeps an arm from landing between a release decision
// and its restore, and the calls are rare and bounded (ioTimeout / 2s).
var (
	recDlMu   sync.Mutex
	recDlArms = map[*chromedp.Browser][]*recCapture{}
)

// recNetReq is the per-request bookkeeping behind network.jsonl — metadata
// only (never bodies, cookies, or auth headers).
type recNetReq struct {
	method       string
	url          string
	resourceType string
	documentURL  string
	initiatorURL string
	mono         cdp.MonotonicTime
	wall         cdp.TimeSinceEpoch
}

// linkCtx returns a ctx that ends when either parent or cap ends — used to
// bind a watcher on the browser ctx to the capture epoch's lifetime.
func linkCtx(parent, cap context.Context) context.Context {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-cap.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}

// startRecordingCapture arms every capture source on the current tab. The
// bundle ends with the tab ctx or the recorder — whichever first — through
// the once-guarded teardown, so swapTab just calls this again for the new
// target. All CDP calls are best-effort.
func (s *Session) startRecordingCapture(r *Recorder) {
	s.mu.Lock()
	tabCtx, browserCtx := s.tabCtx, s.browserCtx
	prev := s.recCap
	var tab *chromedp.Target
	if tabCtx != nil {
		tab = chromedp.FromContext(tabCtx).Target
	}
	s.mu.Unlock()
	if tabCtx == nil || tab == nil {
		return // no live tab (unit tests) — the recorder still works standalone
	}
	capCtx, capCancel := context.WithCancel(tabCtx)
	cap := &recCapture{
		rec: r, tabCtx: tabCtx, browserCtx: browserCtx, cancel: capCancel,
		teardownDone: make(chan struct{}),
		pending:      map[network.RequestID]*recNetReq{}, guids: map[string]bool{},
	}
	s.mu.Lock()
	s.recCap = cap
	s.mu.Unlock()
	go func() {
		select {
		case <-r.Done():
		case <-capCtx.Done():
		}
		s.stopRecordingCapture(cap)
		s.mu.Lock()
		if s.recCap == cap {
			s.recCap = nil
		}
		s.mu.Unlock()
	}()
	// Wait for the previous bundle's teardown before touching the shared
	// browser context: swapTab's old bundle restores the default download
	// behavior there, and an unordered restore would cancel this arm. A dead
	// tabCtx cannot wedge the wait — its teardown has already run or fails
	// fast on the dead context.
	if prev != nil {
		select {
		case <-prev.teardownDone:
		case <-time.After(5 * time.Second): // worst-case teardown is two 2s CDP timeouts
		}
	}
	// The Network domain is enabled once at tab setup (session level), so the
	// capture only needs the frame id — pause drops events at the tap while
	// the domain stays on.
	ctx, c := context.WithTimeout(tabCtx, ioTimeout)
	res, _ := chromedp.Call(ctx, page.GetFrameTree, cdp.Empty{})
	c()
	if res.FrameTree != nil && res.FrameTree.Frame != nil {
		cap.rootFrame = res.FrameTree.Frame.ID
	}
	// Downloads are allowed only while recording (headless Chromium would
	// otherwise drop them): files land in the recording's downloads dir
	// named by guid. The behavior applies to the profile's shared default
	// browser context — sibling sessions' downloads are redirected too, but
	// only this tab's are logged (correlated by root frame id). The arm is
	// registered before the call so a racing teardown sees this capture.
	if browserCtx != nil && os.MkdirAll(r.DownloadsDir(), 0o700) == nil {
		dlCtx, dlCancel := context.WithTimeout(browserCtx, ioTimeout)
		recDlMu.Lock()
		if c := chromedp.FromContext(browserCtx); c != nil && c.Browser != nil {
			cap.dlKey = c.Browser
			recDlArms[cap.dlKey] = append(recDlArms[cap.dlKey], cap)
		}
		_, _ = chromedp.CallBrowser(dlCtx, browser.SetDownloadBehavior, browser.SetDownloadBehaviorParams{
			Behavior:     browser.SetDownloadBehaviorBehaviorAllowAndName,
			DownloadPath: r.DownloadsDir(), EventsEnabled: new(true),
		})
		// A teardown that fired while this arm was registering never saw it —
		// release here or the entry (and the stale download dir) leaks.
		if capCtx.Err() != nil {
			if dir, apply := cap.releaseDownloadArm(); apply {
				params := browser.SetDownloadBehaviorParams{Behavior: browser.SetDownloadBehaviorBehaviorDefault}
				if dir != "" {
					params.Behavior = browser.SetDownloadBehaviorBehaviorAllowAndName
					params.DownloadPath = dir
					params.EventsEnabled = new(true)
				}
				_, _ = chromedp.CallBrowser(dlCtx, browser.SetDownloadBehavior, params)
			}
		}
		recDlMu.Unlock()
		dlCancel()
	}
	go s.watchRecBindings(capCtx, cap, tab)
	go s.recDrainLoop(capCtx, cap)
	s.startNetWatchers(capCtx, cap, tab)
	s.startDownloadWatchers(capCtx, cap)
}

// stopRecordingCapture tears a bundle down exactly once: watchers die with
// capCtx, the Network domain is disabled again (the DevTools proxy keeps its
// own session state), and the context-wide download behavior is handed back
// through releaseDownloadArm — restoring the default only when this capture
// was the last to arm it.
func (s *Session) stopRecordingCapture(cap *recCapture) {
	cap.once.Do(func() {
		defer close(cap.teardownDone)
		cap.cancel()
		if cap.browserCtx != nil {
			ctx, c := context.WithTimeout(cap.browserCtx, 2*time.Second)
			recDlMu.Lock()
			if dir, apply := cap.releaseDownloadArm(); apply {
				params := browser.SetDownloadBehaviorParams{
					Behavior: browser.SetDownloadBehaviorBehaviorDefault,
				}
				if dir != "" {
					// A sibling capture still holds the context — re-arm its
					// downloads dir rather than dropping to the default.
					params.Behavior = browser.SetDownloadBehaviorBehaviorAllowAndName
					params.DownloadPath = dir
					params.EventsEnabled = new(true)
				}
				_, _ = chromedp.CallBrowser(ctx, browser.SetDownloadBehavior, params)
			}
			recDlMu.Unlock()
			c()
		}
	})
}

// releaseDownloadArm drops the capture from its browser's armed list — the
// caller holds recDlMu — and reports what the context-wide behavior should
// become: apply=false when the capture never held the behavior (a later arm
// already replaced it, so restoring would clobber that capture); otherwise
// the previous holder's downloads dir, or "" to restore the CDP default.
func (cap *recCapture) releaseDownloadArm() (dir string, apply bool) {
	if cap.dlKey == nil {
		return "", false
	}
	list := recDlArms[cap.dlKey]
	idx := -1
	for i, c := range list {
		if c == cap {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", false
	}
	wasHolder := idx == len(list)-1
	recDlArms[cap.dlKey] = append(list[:idx], list[idx+1:]...)
	if !wasHolder {
		return "", false
	}
	rest := recDlArms[cap.dlKey]
	if len(rest) == 0 {
		delete(recDlArms, cap.dlKey)
		return "", true
	}
	return rest[len(rest)-1].rec.DownloadsDir(), true
}

// watchRecBindings consumes the batches recordingTapJS pushes through the
// __nusaRecPush binding — the preferred L1 path.
func (s *Session) watchRecBindings(ctx context.Context, cap *recCapture, tab *chromedp.Target) {
	for ev, err := range cdp.Events[runtime.EventBindingCalled](ctx, tab, runtime.BindingCalled) {
		if err != nil {
			return
		}
		if ev.Name == "__nusaRecPush" {
			cap.rec.appendDOMBatch([]byte(ev.Payload))
		}
	}
}

// recDrainLoop drains the in-page buffer on a slow tick via evalQuiet — the
// fallback for the window between document start and binding install. The
// eval never takes the busy claim, so the drain cannot starve
// Annotate/PreviewAnnotate; whichever path splices first wins.
func (s *Session) recDrainLoop(ctx context.Context, cap *recCapture) {
	ticker := time.NewTicker(recDrainInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		// evalQuiet does not request ReturnByValue, so the page side
		// stringifies — appendDOMBatch unwraps the nested JSON.
		raw, err := s.evalQuiet(`(window.__nusaRecTake ? JSON.stringify(window.__nusaRecTake()) : '[]')`)
		if err != nil || len(raw) == 0 {
			continue
		}
		cap.rec.appendDOMBatch(raw)
		// The drain can splice a batch the binding path never saw — feed the
		// obs ring too or Observations() loses events while recording.
		s.appendObsBatch(raw)
	}
}

// appendDOMBatch parses one batch of in-page events (a JSON array, or the
// same array inside a JSON string when the binding serializes twice) and
// appends each entry as an L1 "dom" event. The page's own ts becomes the
// event timestamp; seq still records append order. Malformed entries drop.
func (r *Recorder) appendDOMBatch(payload []byte) {
	var evs []json.RawMessage
	if json.Unmarshal(payload, &evs) != nil {
		var nested string
		if json.Unmarshal(payload, &nested) != nil || json.Unmarshal([]byte(nested), &evs) != nil {
			return
		}
	}
	for _, raw := range evs {
		var head struct {
			T  string `json:"t"`
			TS int64  `json:"ts"`
		}
		if json.Unmarshal(raw, &head) != nil || head.T == "" {
			continue
		}
		r.AppendRaw("dom", head.T, head.TS, raw)
	}
}

// ---------- Network ----------

// startNetWatchers subscribes the Network events of this tab on the capture
// ctx — one goroutine per event type, like the other session watchers. Each
// network.jsonl line is one request stage: a request that never terminates
// still leaves its "request"/"response" lines, and each hop of a redirect
// chain ends with a "redirect" line.
func (s *Session) startNetWatchers(ctx context.Context, cap *recCapture, tab *chromedp.Target) {
	go func() {
		for ev, err := range cdp.Events[network.EventRequestWillBeSent](ctx, tab, network.RequestWillBeSent) {
			if err != nil {
				return
			}
			cap.onRequest(ev)
		}
	}()
	go func() {
		for ev, err := range cdp.Events[network.EventResponseReceived](ctx, tab, network.ResponseReceived) {
			if err != nil {
				return
			}
			cap.onResponse(ev)
		}
	}()
	go func() {
		for ev, err := range cdp.Events[network.EventLoadingFinished](ctx, tab, network.LoadingFinished) {
			if err != nil {
				return
			}
			cap.onFinished(ev)
		}
	}()
	go func() {
		for ev, err := range cdp.Events[network.EventLoadingFailed](ctx, tab, network.LoadingFailed) {
			if err != nil {
				return
			}
			cap.onFailed(ev)
		}
	}()
}

func epochMillis(t cdp.TimeSinceEpoch) int64 { return int64(float64(t) * 1000) }

func (cap *recCapture) onRequest(ev network.EventRequestWillBeSent) {
	// A redirected request reuses the request id: the previous hop ends here
	// with the response that caused the redirect.
	if rr := ev.RedirectResponse; rr != nil {
		cap.finishNetLine(ev.RequestID, "redirect", ev.Timestamp, map[string]any{
			"status": rr.Status, "mimeType": rr.MimeType, "url": rr.URL,
		})
	}
	req := &recNetReq{mono: ev.Timestamp, wall: ev.WallTime,
		resourceType: string(ev.Type), documentURL: ev.DocumentURL}
	if ev.Request != nil {
		req.method = ev.Request.Method
		req.url = ev.Request.URL
	}
	if ev.Initiator != nil {
		req.initiatorURL = ev.Initiator.URL
	}
	cap.netMu.Lock()
	if len(cap.pending) >= recNetPendingCap {
		for id := range cap.pending {
			delete(cap.pending, id)
			break
		}
	}
	cap.pending[ev.RequestID] = req
	cap.netMu.Unlock()
	cap.rec.AppendNet(map[string]any{
		"stage": "request", "requestId": string(ev.RequestID), "method": req.method,
		"url": req.url, "type": req.resourceType, "documentUrl": req.documentURL,
		"initiatorUrl": req.initiatorURL, "frameId": string(ev.FrameID),
		"ts": epochMillis(ev.WallTime),
	})
}

func (cap *recCapture) onResponse(ev network.EventResponseReceived) {
	cap.netMu.Lock()
	req := cap.pending[ev.RequestID]
	cap.netMu.Unlock()
	line := map[string]any{
		"stage": "response", "requestId": string(ev.RequestID),
		"type": string(ev.Type), "ts": nowMillis(),
	}
	var status int64
	var mime, url string
	var respTime cdp.TimeSinceEpoch
	if resp := ev.Response; resp != nil {
		status, mime, url, respTime = resp.Status, resp.MimeType, resp.URL, resp.ResponseTime
	}
	if url == "" && req != nil {
		url = req.url
	}
	line["status"], line["mimeType"], line["url"] = status, mime, url
	if respTime != 0 {
		line["ts"] = epochMillis(respTime)
	}
	if req != nil {
		line["method"] = req.method
		if req.initiatorURL != "" {
			line["initiatorUrl"] = req.initiatorURL
		}
		if respTime != 0 && req.wall != 0 {
			line["ttfbMs"] = int64((float64(respTime) - float64(req.wall)) * 1000)
		}
	}
	cap.rec.AppendNet(line)
	if status >= 400 {
		e := map[string]any{"url": url, "status": status, "resourceType": string(ev.Type)}
		if req != nil {
			e["method"] = req.method
			if req.initiatorURL != "" {
				e["initiatorUrl"] = req.initiatorURL
			}
		}
		cap.rec.Append("browser", "network_error", e)
	}
}

func (cap *recCapture) onFinished(ev network.EventLoadingFinished) {
	cap.finishNetLine(ev.RequestID, "finished", ev.Timestamp, map[string]any{
		"encodedDataLength": ev.EncodedDataLength,
	})
}

func (cap *recCapture) onFailed(ev network.EventLoadingFailed) {
	req := cap.finishNetLine(ev.RequestID, "failed", ev.Timestamp, map[string]any{
		"errorText": ev.ErrorText, "canceled": ev.Canceled,
	})
	if ev.Canceled {
		return // an aborted load is a navigation artifact, not a failure
	}
	e := map[string]any{"error": ev.ErrorText, "resourceType": string(ev.Type)}
	if req != nil {
		e["method"], e["url"] = req.method, req.url
		if req.initiatorURL != "" {
			e["initiatorUrl"] = req.initiatorURL
		}
		if req.mono != 0 {
			e["durationMs"] = int64((float64(ev.Timestamp) - float64(req.mono)) * 1000)
		}
	}
	cap.rec.Append("browser", "network_error", e)
}

// finishNetLine emits the terminal stage line for a request and drops its
// bookkeeping; the returned entry feeds the network_error payload.
func (cap *recCapture) finishNetLine(id network.RequestID, stage string, mono cdp.MonotonicTime, extra map[string]any) *recNetReq {
	cap.netMu.Lock()
	req := cap.pending[id]
	delete(cap.pending, id)
	cap.netMu.Unlock()
	line := map[string]any{"stage": stage, "requestId": string(id)}
	maps.Copy(line, extra)
	ts := nowMillis()
	if req != nil {
		if line["method"] == nil {
			line["method"] = req.method
		}
		if line["url"] == nil {
			line["url"] = req.url
		}
		line["type"] = req.resourceType
		if req.initiatorURL != "" && line["initiatorUrl"] == nil {
			line["initiatorUrl"] = req.initiatorURL
		}
		if req.wall != 0 {
			ts = epochMillis(req.wall)
			if mono != 0 && req.mono != 0 {
				ts += int64((float64(mono) - float64(req.mono)) * 1000)
				line["durationMs"] = int64((float64(mono) - float64(req.mono)) * 1000)
			}
		}
	}
	line["ts"] = ts
	cap.rec.AppendNet(line)
	return req
}

// ---------- Downloads ----------

// startDownloadWatchers subscribes browser-level download events. The set
// behavior is context-wide, so willBegin is attributed to this session by
// the root frame id resolved at capture start; progress events carry only a
// guid and are attributed through it.
func (s *Session) startDownloadWatchers(ctx context.Context, cap *recCapture) {
	if cap.browserCtx == nil {
		return
	}
	go func() {
		for ev, err := range chromedp.BrowserEvents[browser.EventDownloadWillBegin](linkCtx(cap.browserCtx, ctx), browser.DownloadWillBegin) {
			if err != nil {
				return
			}
			cap.onDownloadBegin(ev)
		}
	}()
	go func() {
		for ev, err := range chromedp.BrowserEvents[browser.EventDownloadProgress](linkCtx(cap.browserCtx, ctx), browser.DownloadProgress) {
			if err != nil {
				return
			}
			cap.onDownloadProgress(ev)
		}
	}()
}

func (cap *recCapture) onDownloadBegin(ev browser.EventDownloadWillBegin) {
	// Another tab in the shared context, or the root frame was never
	// resolved — the file still lands in this recording's dir (the behavior
	// is context-wide) but is not logged against this session.
	if cap.rootFrame == "" || ev.FrameID != cap.rootFrame {
		return
	}
	cap.dlMu.Lock()
	if len(cap.guids) < recDlGuidCap {
		cap.guids[ev.GUID] = true
	}
	cap.dlMu.Unlock()
	cap.rec.Append("browser", "download", map[string]any{
		"guid": ev.GUID, "url": ev.URL, "suggestedFilename": ev.SuggestedFilename,
		"state": "started",
	})
}

func (cap *recCapture) onDownloadProgress(ev browser.EventDownloadProgress) {
	state := string(ev.State)
	if state == "inProgress" {
		state = "progress"
	}
	cap.dlMu.Lock()
	ours := cap.guids[ev.GUID]
	if state == "completed" || state == "canceled" {
		delete(cap.guids, ev.GUID)
	}
	cap.dlMu.Unlock()
	if !ours {
		return
	}
	d := map[string]any{"guid": ev.GUID, "state": state,
		"receivedBytes": ev.ReceivedBytes, "totalBytes": ev.TotalBytes}
	if state == "completed" && ev.FilePath != "" {
		d["file"] = "downloads/" + filepath.Base(ev.FilePath)
	}
	cap.rec.Append("browser", "download", d)
}
