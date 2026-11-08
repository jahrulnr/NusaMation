package app

import (
	"net"
	"testing"
)

func TestLoopbackBaseRewritesWildcards(t *testing.T) {
	v4, err := net.ResolveTCPAddr("tcp", "0.0.0.0:8787")
	if err != nil {
		t.Fatal(err)
	}
	if got := loopbackBase(v4); got != "http://127.0.0.1:8787" {
		t.Fatalf("v4 = %s", got)
	}
	v6, err := net.ResolveTCPAddr("tcp", "[::]:9090")
	if err != nil {
		t.Fatal(err)
	}
	if got := loopbackBase(v6); got != "http://127.0.0.1:9090" {
		t.Fatalf("v6 = %s", got)
	}
	local, err := net.ResolveTCPAddr("tcp", "127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	if got := loopbackBase(local); got != "http://127.0.0.1:8787" {
		t.Fatalf("local = %s", got)
	}
}
