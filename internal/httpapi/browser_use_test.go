package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/browser"
	"github.com/jahrulnr/nusamation/internal/mcp/grants"
)

func TestBrowserUseAuth(t *testing.T) {
	g := grants.New("")
	token := g.Token("cv_test")
	s := &Server{browserUse: g}
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
		"browser_navigate":              "navigate",
		"browser_navigate_back":         "navigate_back",
		"browser_navigate_forward":      "navigate_forward",
		"browser_snapshot":              "tree",
		"browser_take_screenshot":       "snapshot",
		"browser_click":                 "click",
		"browser_mouse_click_xy":        "click",
		"browser_mouse_move_xy":         "move",
		"browser_hover":                 "hover",
		"browser_type":                  "type",
		"browser_press_key":             "press",
		"browser_wait_for":              "wait",
		"browser_evaluate":              "eval",
		"browser_console_messages":      "console",
		"browser_select_option":         "select",
		"browser_fill_form":             "fill",
		"browser_resize":                "emulate",
		"browser_scroll":                "scroll",
		"browser_emulate":               "emulate",
		"browser_profile":               "profile",
		"browser_lock":                  "lock",
		"browser_route":                 "route",
		"browser_route_list":            "route_list",
		"browser_route_clear":           "route_clear",
		"browser_network_log":           "network_log",
		"browser_wait_for_response":     "wait_response",
		"browser_add_init_script":       "add_init_script",
		"browser_remove_init_script":    "remove_init_script",
		"browser_route_ws":              "route_ws",
		"browser_route_ws_clear":        "route_ws_clear",
		"browser_get_recording_status":  "recording_status",
		"browser_get_recording_summary": "recording_summary",
		"browser_get_timeline":          "timeline",
		"browser_get_annotation":        "annotation",
		"browser_get_artifact":          "artifact",
		"navigate":                      "navigate",
		"snapshot":                      "snapshot",
		"eval":                          "eval",
		"something_else":                "something_else",
	}
	for in, want := range cases {
		if got := normalizeBrowserUseTool(in); got != want {
			t.Errorf("normalizeBrowserUseTool(%q) = %q, want %q", in, got, want)
		}
	}
	for _, op := range []string{"navigate", "navigate_back", "navigate_forward", "snapshot", "tree",
		"console", "wait", "move", "hover", "click", "type", "press", "scroll", "emulate", "eval", "select", "fill", "lock", "profile",
		"route", "route_list", "route_clear", "network_log", "wait_response",
		"add_init_script", "remove_init_script", "route_ws", "route_ws_clear",
		"recording_status", "recording_summary", "timeline", "annotation", "artifact"} {
		if !knownBrowserUseTool(op) {
			t.Errorf("knownBrowserUseTool(%q) = false", op)
		}
	}
	for _, op := range []string{"recording_status", "recording_summary", "timeline", "annotation", "artifact"} {
		if !recordingReadTool(op) {
			t.Errorf("recordingReadTool(%q) = false", op)
		}
	}
	if recordingReadTool("click") {
		t.Error("recordingReadTool(click) = true — drive tools must not take the read path")
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
		{"lock needs mode", browserUseRequest{}, "lock", "mode is required"},
		{"lock mode ok", browserUseRequest{Mode: new(true)}, "lock", ""},
		{"annotation needs id", browserUseRequest{}, "annotation", "annotation_id"},
		{"annotation ok", browserUseRequest{AnnotationID: "pk_1"}, "annotation", ""},
		{"artifact needs name", browserUseRequest{}, "artifact", "name"},
		{"artifact ok", browserUseRequest{Name: "shot.png"}, "artifact", ""},
		{"route needs pattern", browserUseRequest{RouteAction: "fulfill"}, "route", "pattern is required"},
		{"route needs action", browserUseRequest{Pattern: "**/api/**"}, "route", "action is required"},
		{"route ok", browserUseRequest{Pattern: "**/api/**", RouteAction: "fulfill"}, "route", ""},
		{"route rejects negative times", browserUseRequest{Pattern: "**", RouteAction: "continue", Times: -1}, "route", "times"},
		{"route_clear without id ok", browserUseRequest{}, "route_clear", ""},
		{"network log limit too high", browserUseRequest{Limit: 301}, "network_log", "limit"},
		{"network log negative after", browserUseRequest{After: -1}, "network_log", "after"},
		{"wait response timeout too high", browserUseRequest{Timeout: 31}, "wait_response", "timeout"},
		{"wait response ok", browserUseRequest{Pattern: "**/api/**", Timeout: 5}, "wait_response", ""},
		{"init script needs source", browserUseRequest{}, "add_init_script", "source"},
		{"init script ok", browserUseRequest{Source: "window.x=1"}, "add_init_script", ""},
		{"remove init script needs id", browserUseRequest{}, "remove_init_script", "id is required"},
		{"route ws needs pattern", browserUseRequest{TargetURL: "ws://127.0.0.1/ws"}, "route_ws", "pattern"},
		{"route ws needs target", browserUseRequest{Pattern: "**/ws"}, "route_ws", "target"},
		{"route ws ok", browserUseRequest{Pattern: "**/ws", Target: "ws://127.0.0.1/ws"}, "route_ws", ""},
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

// A refused ref action and a bad select/fill argument must not fall through to
// the generic navigate_failed envelope: agents need to distinguish "retry after
// dismissing the overlay" from "your argument is wrong".
func TestBrowserErrorActionability(t *testing.T) {
	s := &Server{}
	cases := []struct {
		name string
		err  error
		want int
		code string
	}{
		{"not actionable", fmt.Errorf("%w: e3 is covered by #overlay", browser.ErrNotActionable), http.StatusConflict, "not_actionable"},
		{"invalid action", fmt.Errorf("%w: no matching option", browser.ErrInvalidAction), http.StatusBadRequest, "invalid_action"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			s.browserError(rr, tc.err, false)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("body: %v", err)
			}
			if body["code"] != tc.code {
				t.Fatalf("code = %v, want %q", body["code"], tc.code)
			}
		})
	}
}

//go:fix inline
func ptr(f float64) *float64 { return new(f) }

func TestURLMatcher(t *testing.T) {
	all, err := urlMatcher("", false)
	if err != nil {
		t.Fatal(err)
	}
	if !all("http://anything.test/x") {
		t.Fatal("empty pattern should match everything")
	}
	match, err := urlMatcher("**/api/**", false)
	if err != nil {
		t.Fatal(err)
	}
	if !match("http://host/api/chat") || match("http://host/static/app.js") {
		t.Fatal("glob matcher is wrong")
	}
	if _, err := urlMatcher("([", true); err == nil {
		t.Fatal("invalid regex should fail")
	}
}

func TestFilterNetEntries(t *testing.T) {
	entries := []browser.NetEntry{
		{Seq: 1, Method: "GET", URL: "http://host/api/a", Status: 200, Type: "XHR"},
		{Seq: 2, Method: "GET", URL: "http://host/static/app.js", Status: 200, Type: "Script"},
		{Seq: 3, Method: "POST", URL: "http://host/api/b", Status: 500, Type: "Fetch"},
		{Seq: 4, Method: "POST", URL: "http://host/api/c", Error: "net::ERR_FAILED"},
	}
	match, _ := urlMatcher("**/api/**", false)

	all := filterNetEntries(entries, browserUseRequest{}, match)
	if len(all) != 3 || all[0].Seq != 1 || all[2].Seq != 4 {
		t.Fatalf("pattern filter = %+v", all)
	}
	posts := filterNetEntries(entries, browserUseRequest{Method: "post"}, match)
	if len(posts) != 2 || posts[0].Seq != 3 {
		t.Fatalf("method filter = %+v", posts)
	}
	failures := filterNetEntries(entries, browserUseRequest{Errors: true}, match)
	if len(failures) != 2 || failures[0].Seq != 3 || failures[1].Seq != 4 {
		t.Fatalf("errors filter = %+v", failures)
	}
	status := filterNetEntries(entries, browserUseRequest{RouteStatus: 500}, match)
	if len(status) != 1 || status[0].Seq != 3 {
		t.Fatalf("status filter = %+v", status)
	}
	after := filterNetEntries(entries, browserUseRequest{After: 3}, match)
	if len(after) != 1 || after[0].Seq != 4 {
		t.Fatalf("after cursor = %+v", after)
	}
}

func TestScanNetEntriesPicksNewestAndAdvances(t *testing.T) {
	entries := []browser.NetEntry{
		{Seq: 1, URL: "http://host/api/a", Status: 200},
		{Seq: 2, URL: "http://host/static/app.js", Status: 200},
		{Seq: 3, URL: "http://host/api/b", Status: 201},
	}
	match, _ := urlMatcher("**/api/**", false)
	found, cursor, observed, ok := scanNetEntries(entries, 0, browserUseRequest{}, match)
	if !ok || found.Seq != 3 {
		t.Fatalf("newest match = %+v ok=%v, want seq 3", found, ok)
	}
	if cursor != 3 || observed != 3 {
		t.Fatalf("cursor=%d observed=%d, want 3/3", cursor, observed)
	}
	// The cursor is past every entry, so nothing new matches.
	if _, cursor2, observed2, ok2 := scanNetEntries(entries, cursor, browserUseRequest{}, match); ok2 || cursor2 != 3 || observed2 != 0 {
		t.Fatalf("second scan = ok:%v cursor:%d observed:%d", ok2, cursor2, observed2)
	}
}

func TestBrowserNetworkLogKeepsNewestPage(t *testing.T) {
	var entries []browser.NetEntry
	for i := 1; i <= 10; i++ {
		entries = append(entries, browser.NetEntry{Seq: int64(i), URL: "http://host/api/x", Status: 200})
	}
	out, next, err := browserNetworkLog(entries, browserUseRequest{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0].Seq != 8 || out[2].Seq != 10 {
		t.Fatalf("page = %+v, want seq 8..10", out)
	}
	if next != 10 {
		t.Fatalf("next = %d, want 10", next)
	}
	out, next, err = browserNetworkLog(entries, browserUseRequest{Pattern: "**/nothing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 || next != 0 {
		t.Fatalf("empty result = %+v next=%d", out, next)
	}
	if _, _, err := browserNetworkLog(entries, browserUseRequest{Pattern: "([", Regex: true}); err == nil {
		t.Fatal("bad pattern should fail")
	}
}

func TestBrowserWaitResponseTimeout(t *testing.T) {
	start := time.Now()
	_, err := browserWaitResponse(context.Background(), &browser.Session{}, browserUseRequest{
		Pattern: "**/api/**", Timeout: 0.05,
	})
	if _, ok := errors.AsType[*waitResponseTimeout](err); !ok {
		t.Fatalf("error = %v, want waitResponseTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}
