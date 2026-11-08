package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/browser"
	"github.com/jahrulnr/nusamation/internal/browseruse"
)

func TestBrowserUseAuth(t *testing.T) {
	grants := browseruse.NewGrants("")
	token := grants.Token("cv_test")
	s := &Server{browserUse: grants}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/browser-use", s.handleBrowserUse)

	post := func(auth, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/browser-use", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}
	if post("", `{"tool":"snapshot"}`) != http.StatusUnauthorized {
		t.Fatal("missing token")
	}
	if post("Bearer nope", `{"tool":"snapshot"}`) != http.StatusUnauthorized {
		t.Fatal("bad token")
	}
	if post("Bearer "+token, `{"tool":"nope"}`) != http.StatusBadRequest {
		t.Fatal("unknown tool should be rejected before launching a browser")
	}
	if post("Bearer "+token, `{"tool":"emulate"}`) != http.StatusBadRequest {
		t.Fatal("emulate without preset or fields should be rejected")
	}
}

func TestBrowserUseResolvePoint(t *testing.T) {
	in := browserUseRequest{}
	view := browser.View{Width: 800, Height: 600}
	x, y := in.resolvePoint(view, 0.5, 0.25)
	if x != 400 || y != 150 {
		t.Fatalf("fractions should map onto the viewport, got %v,%v", x, y)
	}
	x, y = in.resolvePoint(view, 1, 0)
	if x != 800 || y != 0 {
		t.Fatalf("edge fractions = %v,%v", x, y)
	}
	x, y = in.resolvePoint(view, 320, 240)
	if x != 320 || y != 240 {
		t.Fatalf("absolute CSS px must pass through, got %v,%v", x, y)
	}
	x, y = in.resolvePoint(view, -5, 0.5)
	if x != -5 || y != 0.5 {
		t.Fatalf("a negative axis is absolute, not fractional: %v,%v", x, y)
	}
}

func TestBrowserUseDevicePatch(t *testing.T) {
	cur := browser.DeviceOpts{Width: 1280, Height: 800, DPR: 1}
	in := browserUseRequest{Preset: "mobile"}
	o, err := browser.ResolveDevice(cur, in.Preset, in.devicePatch())
	if err != nil || !o.Mobile || o.Width != 390 {
		t.Fatalf("mobile preset: %+v %v", o, err)
	}
	w := int64(1024)
	in = browserUseRequest{Width: &w}
	o, err = browser.ResolveDevice(cur, in.Preset, in.devicePatch())
	if err != nil || o.Width != 1024 || o.Height != 800 || o.Mobile {
		t.Fatalf("partial override keeps current profile: %+v %v", o, err)
	}
	in = browserUseRequest{Preset: "nope"}
	if _, err = browser.ResolveDevice(cur, in.Preset, in.devicePatch()); !errors.Is(err, browser.ErrUnknownPreset) {
		t.Fatalf("unknown preset needs ErrUnknownPreset, got %v", err)
	}
}
