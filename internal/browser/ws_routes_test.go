package browser

import (
	"errors"
	"strings"
	"testing"
)

func TestPrepareWSRoute(t *testing.T) {
	ok, err := prepareWSRoute(WSRouteRule{Pattern: "**/api/ws", Target: "ws://127.0.0.1:9999/ws"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ok.ID, "ws_") {
		t.Fatalf("id = %q", ok.ID)
	}
	if !ok.re.MatchString("ws://host/api/ws") {
		t.Fatal("pattern did not compile against a websocket URL")
	}

	bad := []WSRouteRule{
		{Target: "ws://127.0.0.1/ws"},
		{Pattern: "**/ws", Target: "/relative"},
		{Pattern: "**/ws", Target: "http://127.0.0.1/ws"},
		{Pattern: "**/ws", Target: "ws://"},
	}
	for _, in := range bad {
		if _, err := prepareWSRoute(in); !errors.Is(err, ErrRouteInvalid) {
			t.Errorf("prepareWSRoute(%+v) error = %v, want ErrRouteInvalid", in, err)
		}
	}
}

func TestWSShimJS(t *testing.T) {
	if got := wsShimJS(nil); got != "" {
		t.Fatalf("empty routes should install nothing, got %q", got)
	}
	rule, err := prepareWSRoute(WSRouteRule{Pattern: "**/api/ws", Target: "ws://127.0.0.1:9999/ws"})
	if err != nil {
		t.Fatal(err)
	}
	src := wsShimJS([]*wsRoute{rule})
	for _, want := range []string{
		"class NusaWebSocket extends Native",
		"super(target, protocols)",
		"Object.defineProperty(this, \"url\"",
		"Object.defineProperty(window, \"WebSocket\"",
		rule.re.String(),
		"ws://127.0.0.1:9999/ws",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("shim missing %q", want)
		}
	}
}
