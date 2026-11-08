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

func TestNormalizeBrowserUseTool(t *testing.T) {
	cases := map[string]string{
		"browser_navigate":         "navigate",
		"browser_navigate_back":    "navigate_back",
		"browser_navigate_forward": "navigate_forward",
		"browser_snapshot":         "tree",
		"browser_take_screenshot":  "snapshot",
		"browser_click":            "click",
		"browser_mouse_click_xy":   "click",
		"browser_mouse_move_xy":    "move",
		"browser_hover":            "hover",
		"browser_type":             "type",
		"browser_press_key":        "press",
		"browser_wait_for":         "wait",
		"browser_evaluate":         "eval",
		"browser_console_messages": "console",
		"browser_select_option":    "select",
		"browser_fill_form":        "fill",
		"browser_resize":           "emulate",
		"browser_scroll":           "scroll",
		"browser_emulate":          "emulate",
		"navigate":                 "navigate",
		"snapshot":                 "snapshot",
		"eval":                     "eval",
		"something_else":           "something_else",
	}
	for in, want := range cases {
		if got := normalizeBrowserUseTool(in); got != want {
			t.Errorf("normalizeBrowserUseTool(%q) = %q, want %q", in, got, want)
		}
	}
	for _, op := range []string{"navigate", "navigate_back", "navigate_forward", "snapshot", "tree",
		"console", "wait", "move", "hover", "click", "type", "press", "scroll", "emulate", "eval", "select", "fill"} {
		if !knownBrowserUseTool(op) {
			t.Errorf("knownBrowserUseTool(%q) = false", op)
		}
	}
	if knownBrowserUseTool("bogus") {
		t.Error("knownBrowserUseTool(bogus) = true")
	}
}

func TestBrowserUseInvalid(t *testing.T) {
	cases := []struct {
		name string
		in   browserUseRequest
		tool string
		want string
	}{
		{"click ref ok", browserUseRequest{Ref: "e1"}, "click", ""},
		{"click xy ok", browserUseRequest{X: new(0.5), Y: new(0.5)}, "click", ""},
		{"click nothing", browserUseRequest{}, "click", "ref or x/y"},
		{"hover needs ref", browserUseRequest{}, "hover", "ref"},
		{"select needs values", browserUseRequest{Ref: "e1"}, "select", "values"},
		{"select needs ref", browserUseRequest{Values: []string{"a"}}, "select", "ref"},
		{"fill needs fields", browserUseRequest{}, "fill", "fields"},
		{"wait needs one", browserUseRequest{}, "wait", "exactly one"},
		{"wait rejects two", browserUseRequest{Time: 1, Text: "x"}, "wait", "exactly one"},
		{"wait textGone ok", browserUseRequest{TextGone: "Loading"}, "wait", ""},
		{"eval expression ok", browserUseRequest{Expression: "1+1"}, "eval", ""},
		{"eval function ok", browserUseRequest{Function: "() => 1"}, "eval", ""},
		{"eval needs one", browserUseRequest{}, "eval", "expression or function"},
		{"type ok no ref", browserUseRequest{Text: "hi"}, "type", ""},
		{"type ok ref", browserUseRequest{Text: "hi", Ref: "e2"}, "type", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.invalid(tc.tool)
			if tc.want == "" && got != "" {
				t.Fatalf("invalid() = %q, want ok", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("invalid() = %q, want containing %q", got, tc.want)
			}
		})
	}
}

//go:fix inline
func ptr(f float64) *float64 { return new(f) }
