package browser

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
)

func TestGlobURL(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		regex   bool
		url     string
		want    bool
	}{
		{"double star spans path", "**/api/**", false, "http://127.0.0.1:3000/api/chat/stream", true},
		{"double star spans host", "**/api/**", false, "https://example.test/v1/api/chat?x=1", true},
		{"single star stops at slash", "http://host/*/api", false, "http://host/a/api", true},
		{"single star does not cross slash", "http://host/*/api", false, "http://host/a/b/api", false},
		{"question mark is one character", "http://host/file?.js", false, "http://host/file1.js", true},
		{"question mark is not empty", "http://host/file?.js", false, "http://host/file.js", false},
		{"anchored at end", "**/api", false, "http://host/api/v2", false},
		{"dots are literal", "http://host/a.js", false, "http://host/axjs", false},
		{"query string is matched", "**/search?q=*", false, "http://host/search?q=go", true},
		{"escaped star is literal", `**/file\*.js`, false, "http://host/a/file*.js", true},
		{"escaped star rejects wildcard match", `**/file\*.js`, false, "http://host/a/file1.js", false},
		{"escaped question is literal", `**/q\?=1`, false, "http://host/q?=1", true},
		{"regex mode", `^https://api\.test/v\d+/`, true, "https://api.test/v2/users", true},
		{"regex mode is not glob", `api/\*`, true, "http://host/api/x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			re, err := GlobURL(tc.pattern, tc.regex)
			if err != nil {
				t.Fatalf("GlobURL(%q): %v", tc.pattern, err)
			}
			if got := re.MatchString(tc.url); got != tc.want {
				t.Fatalf("GlobURL(%q).MatchString(%q) = %v, want %v", tc.pattern, tc.url, got, tc.want)
			}
		})
	}
}

func TestGlobURLRejectsInvalidPattern(t *testing.T) {
	if _, err := GlobURL("", false); !errors.Is(err, ErrRouteInvalid) {
		t.Fatalf("empty pattern error = %v, want ErrRouteInvalid", err)
	}
	if _, err := GlobURL("([", true); !errors.Is(err, ErrRouteInvalid) {
		t.Fatalf("bad regex error = %v, want ErrRouteInvalid", err)
	}
	if _, err := GlobURL(strings.Repeat("a", routePatternCap+1), false); !errors.Is(err, ErrRouteInvalid) {
		t.Fatalf("oversized pattern error = %v, want ErrRouteInvalid", err)
	}
}

func TestPrepareRouteRule(t *testing.T) {
	t.Run("fulfill defaults", func(t *testing.T) {
		r, err := prepareRouteRule(RouteRule{Pattern: "**/api/**", Action: "FULFILL", Body: `{"ok":true}`})
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != 200 {
			t.Fatalf("status = %d, want 200", r.Status)
		}
		if r.remaining != -1 {
			t.Fatalf("remaining = %d, want -1 (unlimited)", r.remaining)
		}
		if string(r.body) != `{"ok":true}` {
			t.Fatalf("body = %q", r.body)
		}
	})

	t.Run("base64 body decodes once", func(t *testing.T) {
		raw := []byte{0x00, 0x01, 0xff}
		r, err := prepareRouteRule(RouteRule{
			Pattern: "**/blob", Action: RouteFulfill, BodyBase64: true,
			Body: base64.StdEncoding.EncodeToString(raw),
		})
		if err != nil {
			t.Fatal(err)
		}
		if string(r.body) != string(raw) {
			t.Fatalf("body = %v, want %v", r.body, raw)
		}
	})

	t.Run("times budget", func(t *testing.T) {
		r, err := prepareRouteRule(RouteRule{Pattern: "**", Action: RouteContinue, Times: 2})
		if err != nil {
			t.Fatal(err)
		}
		if r.remaining != 2 {
			t.Fatalf("remaining = %d, want 2", r.remaining)
		}
		if _, err := prepareRouteRule(RouteRule{Pattern: "**", Action: RouteContinue, Times: -1}); !errors.Is(err, ErrRouteInvalid) {
			t.Fatalf("negative times error = %v", err)
		}
	})

	invalid := []struct {
		name string
		in   RouteRule
	}{
		{"missing action", RouteRule{Pattern: "**"}},
		{"unknown action", RouteRule{Pattern: "**", Action: "mock"}},
		{"unknown resource type", RouteRule{Pattern: "**", Action: RouteContinue, ResourceType: "xhr2"}},
		{"websocket belongs to route_ws", RouteRule{Pattern: "**", Action: RouteContinue, ResourceType: "websocket", TargetURL: "ws://127.0.0.1/ws"}},
		{"bad method", RouteRule{Pattern: "**", Action: RouteContinue, Method: "GE T"}},
		{"status out of range", RouteRule{Pattern: "**", Action: RouteFulfill, Status: 700}},
		{"header injection", RouteRule{Pattern: "**", Action: RouteFulfill, Headers: map[string]string{"X": "a\r\nb"}}},
		{"bad header name", RouteRule{Pattern: "**", Action: RouteFulfill, Headers: map[string]string{"X Y": "v"}}},
		{"bad base64", RouteRule{Pattern: "**", Action: RouteFulfill, BodyBase64: true, Body: "!!"}},
		{"delay without budget", RouteRule{Pattern: "**", Action: RouteDelay}},
		{"delay too long", RouteRule{Pattern: "**", Action: RouteDelay, DelayMS: int(routeDelayCap.Milliseconds()) + 1}},
		{"abort reason unknown", RouteRule{Pattern: "**", Action: RouteAbort, ErrorReason: "nope"}},
		{"relative target", RouteRule{Pattern: "**", Action: RouteContinue, TargetURL: "/other"}},
		{"data target", RouteRule{Pattern: "**", Action: RouteContinue, TargetURL: "data:text/plain,x"}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := prepareRouteRule(tc.in); !errors.Is(err, ErrRouteInvalid) {
				t.Fatalf("error = %v, want ErrRouteInvalid", err)
			}
		})
	}
}

func TestFulfillParamsContentType(t *testing.T) {
	cases := []struct {
		name string
		rule RouteRule
		want string
	}{
		{"json inferred", RouteRule{Pattern: "**", Action: RouteFulfill, Body: `{"a":1}`}, "application/json"},
		{"text inferred", RouteRule{Pattern: "**", Action: RouteFulfill, Body: "hello"}, "text/plain; charset=utf-8"},
		{"base64 inferred", RouteRule{Pattern: "**", Action: RouteFulfill, BodyBase64: true, Body: "AAE="}, "application/octet-stream"},
		{"explicit header wins", RouteRule{Pattern: "**", Action: RouteFulfill, Body: `{"a":1}`, Headers: map[string]string{"content-type": "application/vnd.test+json"}}, "application/vnd.test+json"},
		{"empty body has no type", RouteRule{Pattern: "**", Action: RouteFulfill}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := prepareRouteRule(tc.rule)
			if err != nil {
				t.Fatal(err)
			}
			params := fulfillParams("1", r)
			got := ""
			for _, h := range params.ResponseHeaders {
				if strings.EqualFold(h.Name, "content-type") {
					got = h.Value
				}
			}
			if got != tc.want {
				t.Fatalf("content-type = %q, want %q", got, tc.want)
			}
			if params.ResponseCode != 200 {
				t.Fatalf("status = %d, want 200", params.ResponseCode)
			}
		})
	}
}

func TestRouteRuleMatches(t *testing.T) {
	r, err := prepareRouteRule(RouteRule{
		Pattern: "**/api/**", Action: RouteFulfill, Method: "POST", ResourceType: "xhr",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.matches("POST", network.ResourceTypeXHR, "http://host/api/chat") {
		t.Fatal("expected match")
	}
	if r.matches("GET", network.ResourceTypeXHR, "http://host/api/chat") {
		t.Fatal("method filter ignored")
	}
	if r.matches("POST", network.ResourceTypeFetch, "http://host/api/chat") {
		t.Fatal("resource type filter ignored")
	}
	if r.matches("POST", network.ResourceTypeXHR, "http://host/static/a.js") {
		t.Fatal("url filter ignored")
	}
}

func TestRemoveRoute(t *testing.T) {
	rules := []*routeRule{{id: "a"}, {id: "b"}, {id: "c"}}
	got := removeRoute(rules, "b")
	if len(got) != 2 || got[0].id != "a" || got[1].id != "c" {
		t.Fatalf("removeRoute = %v", []string{got[0].id, got[1].id})
	}
	if len(removeRoute(rules, "missing")) != 3 {
		t.Fatal("removing a missing id changed the list")
	}
}

func TestFulfillParamsDropsContentLength(t *testing.T) {
	r, err := prepareRouteRule(RouteRule{
		Pattern: "**", Action: RouteFulfill, Body: "x",
		Headers: map[string]string{"Content-Length": "99"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range fulfillParams("1", r).ResponseHeaders {
		if strings.EqualFold(h.Name, "content-length") {
			t.Fatalf("synthetic Content-Length must be dropped, got %q", h.Value)
		}
	}
}

func TestFulfillParamsKeepsDeclaredStatus(t *testing.T) {
	r, err := prepareRouteRule(RouteRule{Pattern: "**", Action: RouteFulfill, Status: 503, Body: "down"})
	if err != nil {
		t.Fatal(err)
	}
	params := fulfillParams(fetch.RequestID("r1"), r)
	if params.ResponseCode != 503 {
		t.Fatalf("status = %d, want 503", params.ResponseCode)
	}
	if string(params.Body) != "down" {
		t.Fatalf("body = %q", params.Body)
	}
}
