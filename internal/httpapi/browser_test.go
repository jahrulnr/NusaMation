package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/browser"
)

func TestNormalizeBrowserURL(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		ok       bool
		implicit bool
	}{
		{"", "about:blank", true, false},
		{"  ", "about:blank", true, false},
		{"example.com", "https://example.com", true, true},
		{" example.com ", "https://example.com", true, true},
		{"localhost:8787", "https://localhost:8787", true, true},
		{"192.168.1.5:3000/x", "https://192.168.1.5:3000/x", true, true},
		{"http://a.test/x", "http://a.test/x", true, false},
		{"https://a.test", "https://a.test", true, false},
		{"about:blank", "about:blank", true, false},
		{"data:text/html,<h1>x</h1>", "data:text/html,<h1>x</h1>", true, false},
		{"file:///etc/passwd", "file:///etc/passwd", true, false},
		{"http://", "", false, false},
		{"https://", "", false, false},
		{"about:", "", false, false},
		{"file://", "", false, false},
		{"javascript:alert(1)", "https://www.google.com/search?q=javascript%3Aalert%281%29", true, false},
		{"ftp://a.test", "https://www.google.com/search?q=ftp%3A%2F%2Fa.test", true, false},
		{"//host-only", "https://www.google.com/search?q=%2F%2Fhost-only", true, false},
		{"just text no dots", "https://www.google.com/search?q=just+text+no+dots", true, false},
		{"myhost", "https://www.google.com/search?q=myhost", true, false},
		{"nas:5000/share", "https://nas:5000/share", true, true},
	}
	for _, c := range cases {
		got, implicit, ok := normalizeBrowserURL(c.in)
		if ok != c.ok {
			t.Errorf("normalizeBrowserURL(%q) ok=%v, want %v (got %q)", c.in, ok, c.ok, got)
			continue
		}
		if ok && got != c.want {
			t.Errorf("normalizeBrowserURL(%q) = %q, want %q", c.in, got, c.want)
		}
		if implicit != c.implicit {
			t.Errorf("normalizeBrowserURL(%q) implicit=%v, want %v", c.in, implicit, c.implicit)
		}
	}
}

func TestBrowserInstallHandlersNeedManager(t *testing.T) {
	s := &Server{}
	rr := httptest.NewRecorder()
	s.handleBrowserInstallOffer(rr, httptest.NewRequest(http.MethodGet, "/api/browser/install", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET offer without manager = %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	s.handleBrowserInstall(rr, httptest.NewRequest(http.MethodPost, "/api/browser/install", strings.NewReader(`{"method":"download"}`)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST install without manager = %d", rr.Code)
	}
}

func TestBrowserInstallValidatesPayloadAndMethod(t *testing.T) {
	mgr := browser.NewManager(browser.Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(mgr.Close)
	s := &Server{browser: mgr}

	rr := httptest.NewRecorder()
	s.handleBrowserInstall(rr, httptest.NewRequest(http.MethodPost, "/api/browser/install", strings.NewReader(`{oops`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON = %d", rr.Code)
	}

	// A non-whitelisted method must never run — the handler surfaces the
	// browser layer's rejection, not a shell.
	rr = httptest.NewRecorder()
	s.handleBrowserInstall(rr, httptest.NewRequest(http.MethodPost, "/api/browser/install",
		strings.NewReader(`{"method":"apt-get install -y curl"}`)))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("non-whitelisted method = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "install_failed") {
		t.Fatalf("expected install_failed envelope, got %s", rr.Body.String())
	}
}
