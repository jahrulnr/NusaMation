package browser

// WebSocket routing for browser_use. CDP's Fetch domain does not intercept a
// WebSocket handshake — only HTTP requests reach it — so this follows the
// approach Playwright uses: an injected constructor that rewrites matching
// URLs to a target socket. The agent runs the fake hub itself (a small local
// process, not a second browser) and points the pattern at it.
//
// Scope: the shim installs through Page.addScriptToEvaluateOnNewDocument, so
// it runs before any page script in every frame of the tab. WebSockets created
// inside a dedicated Worker are not covered; the shim lives on the window.
// Hits are not counted because the constructor has no channel back to the
// harness.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/jahrulnr/nusamation/internal/ids"
)

const (
	// wsRouteCap bounds the number of rewrite patterns.
	wsRouteCap = 32
	// wsPatternCap bounds a URL pattern.
	wsPatternCap = routePatternCap
)

// WSRouteRule is one WebSocket rewrite rule as accepted from the agent.
type WSRouteRule struct {
	Pattern string `json:"pattern"`
	Target  string `json:"target"`
}

// WSRouteState is a stored WebSocket rule.
type WSRouteState struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Target  string `json:"target"`
}

type wsRoute struct {
	WSRouteState
	re *regexp.Regexp
}

// prepareWSRoute validates a WebSocket rule.
func prepareWSRoute(in WSRouteRule) (*wsRoute, error) {
	if strings.TrimSpace(in.Pattern) == "" {
		return nil, fmt.Errorf("%w: pattern is required", ErrRouteInvalid)
	}
	if len(in.Pattern) > wsPatternCap {
		return nil, fmt.Errorf("%w: pattern is longer than %d bytes", ErrRouteInvalid, wsPatternCap)
	}
	re, err := GlobURL(in.Pattern, false)
	if err != nil {
		return nil, err
	}
	target := strings.TrimSpace(in.Target)
	parsed, err := url.Parse(target)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return nil, fmt.Errorf("%w: target must be an absolute ws:// or wss:// URL", ErrRouteInvalid)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "ws", "wss":
	default:
		return nil, fmt.Errorf("%w: target scheme must be ws or wss", ErrRouteInvalid)
	}
	return &wsRoute{
		ID: ids.New("ws"), Pattern: in.Pattern, Target: target,
		re: re,
	}, nil
}

// AddWSRoute registers a WebSocket rewrite and applies the document-start
// shim. When reload is true the current page is reloaded so the shim is in
// place before the app opens its socket.
func (s *Session) AddWSRoute(ctx context.Context, in WSRouteRule, reload bool) (WSRouteState, error) {
	rule, err := prepareWSRoute(in)
	if err != nil {
		return WSRouteState{}, err
	}
	s.initMu.Lock()
	if len(s.wsRoutes) >= wsRouteCap {
		s.initMu.Unlock()
		return WSRouteState{}, fmt.Errorf("%w: at most %d WebSocket rules", ErrRouteInvalid, wsRouteCap)
	}
	// One rule per pattern: re-adding the same pattern retargets it instead of
	// stacking two shims that both match.
	replaced := false
	for i, existing := range s.wsRoutes {
		if existing.Pattern == rule.Pattern {
			s.wsRoutes[i] = rule
			replaced = true
			break
		}
	}
	if !replaced {
		s.wsRoutes = append(s.wsRoutes, rule)
	}
	s.initMu.Unlock()
	if err := s.applyDocumentScripts(); err != nil {
		return WSRouteState{}, err
	}
	if reload {
		if _, err := s.History(ctx, "reload"); err != nil {
			return WSRouteState{}, err
		}
	}
	return rule.WSRouteState, nil
}

// ClearWSRoutes removes every WebSocket rule and the shim. A live page keeps
// the shim until it navigates, so reload is offered for the same reason as on
// add.
func (s *Session) ClearWSRoutes(ctx context.Context, reload bool) (int, error) {
	s.initMu.Lock()
	removed := len(s.wsRoutes)
	s.wsRoutes = nil
	s.initMu.Unlock()
	if err := s.applyDocumentScripts(); err != nil {
		return 0, err
	}
	if reload && removed > 0 {
		if _, err := s.History(ctx, "reload"); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// WSRoutes returns a snapshot of the WebSocket rules.
func (s *Session) WSRoutes() []WSRouteState {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	out := make([]WSRouteState, 0, len(s.wsRoutes))
	for _, r := range s.wsRoutes {
		out = append(out, r.WSRouteState)
	}
	return out
}

// applyDocumentScripts (re)installs the harness-owned document-start scripts:
// the WebSocket shim built from the current rules and every agent init script.
// It is also the recovery path after a tab swap, where the target-scoped
// identifiers from the old tab are dead.
func (s *Session) applyDocumentScripts() error {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	ctx, cancel := context.WithTimeout(s.tabCtx, 5*time.Second)
	defer cancel()
	// The shim and the agent scripts are budgeted by separate limits and
	// re-added together, so drop both sets before adding fresh ones.
	if s.wsShimID != "" {
		_, _ = chromedp.Call(ctx, page.RemoveScriptToEvaluateOnNewDocument,
			page.RemoveScriptToEvaluateOnNewDocumentParams{Identifier: s.wsShimID})
		s.wsShimID = ""
	}
	for _, script := range s.initScripts {
		if script.identifier == "" {
			continue
		}
		_, _ = chromedp.Call(ctx, page.RemoveScriptToEvaluateOnNewDocument,
			page.RemoveScriptToEvaluateOnNewDocumentParams{Identifier: script.identifier})
		script.identifier = ""
	}
	if src := wsShimJS(s.wsRoutes); src != "" {
		res, err := chromedp.Call(ctx, page.AddScriptToEvaluateOnNewDocument,
			page.AddScriptToEvaluateOnNewDocumentParams{Source: src})
		if err != nil {
			return fmt.Errorf("%w: install WebSocket shim: %v", ErrUnavailable, err)
		}
		s.wsShimID = res.Identifier
	}
	for _, script := range s.initScripts {
		res, err := chromedp.Call(ctx, page.AddScriptToEvaluateOnNewDocument,
			page.AddScriptToEvaluateOnNewDocumentParams{Source: script.Source})
		if err != nil {
			return fmt.Errorf("%w: install init script %s: %v", ErrUnavailable, script.ID, err)
		}
		script.identifier = res.Identifier
	}
	return nil
}

// wsShimRule is the JSON payload the shim evaluates. Patterns are compiled in
// Go so the shim only needs RegExp.
type wsShimRule struct {
	Re     string `json:"re"`
	Target string `json:"target"`
}

// wsShimJS builds the document-start shim, or "" when there are no rules.
func wsShimJS(routes []*wsRoute) string {
	if len(routes) == 0 {
		return ""
	}
	payload := make([]wsShimRule, 0, len(routes))
	for _, r := range routes {
		payload = append(payload, wsShimRule{Re: r.re.String(), Target: r.Target})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return `(() => {
  const Native = window.WebSocket;
  if (!Native || Native.__nusaRouteWS) return;
  const rules = ` + string(encoded) + `;
  class NusaWebSocket extends Native {
    constructor(url, protocols) {
      const original = String(url);
      let target = original;
      for (const rule of rules) {
        let re;
        try { re = new RegExp(rule.re); } catch (_) { continue; }
        if (re.test(original)) { target = rule.target; break; }
      }
      super(target, protocols);
      if (target !== original) {
        try {
          Object.defineProperty(this, "url", { value: original, configurable: true, enumerable: true });
        } catch (_) {}
      }
    }
  }
  Object.defineProperty(Native, "__nusaRouteWS", { value: true });
  Object.defineProperty(window, "WebSocket", { value: NusaWebSocket, configurable: true, writable: true });
})();`
}
