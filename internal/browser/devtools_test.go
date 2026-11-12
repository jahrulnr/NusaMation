package browser

import "testing"

func TestDebugOutputCapturesCompleteEndpoint(t *testing.T) {
	d := &debugOutput{}
	_, _ = d.Write([]byte("Chrome starting\nDevTools listening on ws://127.0.0.1:"))
	if d.addr != "" {
		t.Fatalf("accepted incomplete address: %q", d.addr)
	}
	_, _ = d.Write([]byte("12345/devtools/browser/abc\n"))
	if d.addr != "127.0.0.1:12345" {
		t.Fatalf("address = %q", d.addr)
	}
}
