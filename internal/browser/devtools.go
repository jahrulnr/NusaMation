package browser

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// debugOutput captures the local endpoint printed by Chromium at startup.
type debugOutput struct {
	mu   sync.Mutex
	text string
	addr string
}

func (d *debugOutput) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.addr == "" {
		d.text += string(p)
		lines := strings.Split(d.text, "\n")
		for _, line := range lines[:len(lines)-1] {
			const prefix = "DevTools listening on "
			if after, ok := strings.CutPrefix(line, prefix); ok {
				u, err := url.Parse(strings.TrimSpace(after))
				if err == nil && u.Host != "" {
					d.addr = u.Host
				}
			}
		}
		if len(d.text) > 65536 {
			d.text = d.text[len(d.text)-4096:]
		}
	}
	return len(p), nil
}

// DevToolsEndpoint returns the profile's local debugging server and this tab's target.
func (s *Session) DevToolsEndpoint() (string, string, error) {
	s.mu.Lock()
	profile, tab, status := s.profile, s.tab(), s.status
	s.mu.Unlock()
	if status != "active" || tab == nil {
		return "", "", ErrClosed
	}
	s.m.mu.Lock()
	p := s.m.browsers[profile]
	if p == nil || p.debug == nil {
		s.m.mu.Unlock()
		return "", "", ErrUnavailable
	}
	d := p.debug
	s.m.mu.Unlock()
	d.mu.Lock()
	addr := d.addr
	d.mu.Unlock()
	if addr == "" {
		return "", "", fmt.Errorf("%w: Chrome debugging endpoint missing", ErrUnavailable)
	}
	return addr, string(tab.TargetID), nil
}
