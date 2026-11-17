package browser

// Agent init scripts for browser_use: JavaScript installed with
// Page.addScriptToEvaluateOnNewDocument so it runs before any page script in
// every frame of the tab. This is the seam that lets an agent stub window
// APIs (fetch, timers, matchMedia, WebSocket) before the app boots, the same
// way Playwright's addInitScript does.
//
// The harness already owns document-start scripts for its own taps; agent
// scripts are stored per session and re-installed with them after a tab swap.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/jahrulnr/nusamation/internal/ids"
)

const (
	// initScriptCap bounds one source; initScriptCountCap bounds the list.
	initScriptCap      = 256 << 10
	initScriptCountCap = 32
)

// AgentInitScript is one stored init script as reported back to the agent.
type AgentInitScript struct {
	ID     string `json:"id"`
	Source string `json:"source"`
}

type initScript struct {
	AgentInitScript
	identifier page.ScriptIdentifier
}

var initScriptIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// AddInitScript stores a document-start script and installs it on the current
// tab. An explicit id replaces that script in place, which is how an agent
// updates a stub without stacking a second copy. runImmediately also executes
// the script in the current page; future documents get it at document start.
func (s *Session) AddInitScript(ctx context.Context, source, id string, runImmediately bool) (AgentInitScript, error) {
	if len(source) > initScriptCap {
		return AgentInitScript{}, fmt.Errorf("%w: source is larger than %d bytes", ErrRouteInvalid, initScriptCap)
	}
	if source == "" {
		return AgentInitScript{}, fmt.Errorf("%w: source is required", ErrRouteInvalid)
	}
	if id != "" && !initScriptIDPattern.MatchString(id) {
		return AgentInitScript{}, fmt.Errorf("%w: id must match [A-Za-z0-9_-]{1,64}", ErrRouteInvalid)
	}
	script := &initScript{ID: id, Source: source}
	if script.ID == "" {
		script.ID = ids.New("is")
	}

	s.initMu.Lock()
	replaced := false
	var stale page.ScriptIdentifier
	for i, existing := range s.initScripts {
		if existing.ID == script.ID {
			stale = existing.identifier
			s.initScripts[i] = script
			replaced = true
			break
		}
	}
	if !replaced {
		if len(s.initScripts) >= initScriptCountCap {
			s.initMu.Unlock()
			return AgentInitScript{}, fmt.Errorf("%w: at most %d init scripts", ErrRouteInvalid, initScriptCountCap)
		}
		s.initScripts = append(s.initScripts, script)
	}
	s.initMu.Unlock()
	// A replaced script kept its old identifier; leaving it installed would run
	// both versions on the next document.
	s.removeScriptIdentifier(stale)

	if err := s.applyDocumentScripts(); err != nil {
		return AgentInitScript{}, err
	}
	if runImmediately {
		runCtx, cancel := context.WithTimeout(s.tabCtx, 5*time.Second)
		defer cancel()
		yes := true
		res, err := chromedp.Call(runCtx, runtime.Evaluate, runtime.EvaluateParams{
			Expression: source, AwaitPromise: &yes, ReturnByValue: &yes,
		})
		if err == nil && res.ExceptionDetails != nil {
			desc := res.ExceptionDetails.Text
			if res.ExceptionDetails.Exception != nil && res.ExceptionDetails.Exception.Description != "" {
				desc = res.ExceptionDetails.Exception.Description
			}
			err = errors.New(desc)
		}
		if err != nil {
			// The script is installed for future documents either way; tell
			// the agent what actually failed.
			return script.AgentInitScript, fmt.Errorf(
				"init script installed for future documents, but running it now failed: %w", err)
		}
	}
	return script.AgentInitScript, nil
}

// RemoveInitScript drops one script by id and uninstalls it from the current
// tab. It returns false when no script has that id.
func (s *Session) RemoveInitScript(id string) bool {
	s.initMu.Lock()
	found := false
	var stale page.ScriptIdentifier
	out := s.initScripts[:0]
	for _, script := range s.initScripts {
		if script.ID == id {
			stale = script.identifier
			found = true
			continue
		}
		out = append(out, script)
	}
	s.initScripts = out
	s.initMu.Unlock()
	if !found {
		return false
	}
	// The removed script is already out of the list, so applyDocumentScripts
	// cannot see its identifier; uninstall it here.
	s.removeScriptIdentifier(stale)
	if err := s.applyDocumentScripts(); err != nil && s.m != nil && s.m.log != nil {
		s.m.log.Warn("init script removal not applied to the current tab", "id", id, "error", err)
	}
	return true
}

// removeScriptIdentifier uninstalls one target-scoped script. A stale
// identifier (old tab) only returns an error, which is safe to ignore.
func (s *Session) removeScriptIdentifier(id page.ScriptIdentifier) {
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(s.tabCtx, 5*time.Second)
	defer cancel()
	_, _ = chromedp.Call(ctx, page.RemoveScriptToEvaluateOnNewDocument,
		page.RemoveScriptToEvaluateOnNewDocumentParams{Identifier: id})
}

// InitScripts returns a snapshot without sources: the agent already has them,
// and a listing must not flood the tool result.
func (s *Session) InitScripts() []AgentInitScript {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	out := make([]AgentInitScript, 0, len(s.initScripts))
	for _, script := range s.initScripts {
		out = append(out, AgentInitScript{ID: script.ID})
	}
	return out
}

// reinstallDocumentScripts restores the agent scripts and the WebSocket shim
// after a tab swap. The old identifiers belonged to the closed target.
func (s *Session) reinstallDocumentScripts() {
	if err := s.applyDocumentScripts(); err != nil && s.m != nil && s.m.log != nil {
		s.m.log.Warn("document scripts not re-installed after tab swap", "error", err)
	}
}
