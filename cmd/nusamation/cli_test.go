package main

import (
	"flag"
	"io"
	"reflect"
	"testing"
)

func testFlagSet() (*flag.FlagSet, *string, *bool, *stringList) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("data-dir", "/tmp/x", "")
	jsonOut := fs.Bool("json", false, "")
	var args stringList
	fs.Var(&args, "arg", "")
	return fs, dir, jsonOut, &args
}

func TestParseInterleaved(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		pos  []string
		dir  string
		json bool
		args []string
	}{
		{
			name: "flags then positional",
			in:   []string{"-data-dir", "/d", "-json", "ref"},
			pos:  []string{"ref"}, dir: "/d", json: true,
		},
		{
			name: "positional then flags",
			in:   []string{"ref", "-data-dir", "/d", "-json"},
			pos:  []string{"ref"}, dir: "/d", json: true,
		},
		{
			name: "two positionals around flags",
			in:   []string{"key", "-json", "value"},
			pos:  []string{"key", "value"}, json: true,
		},
		{
			name: "repeated list flag with positional",
			in:   []string{"ref", "-arg", "a", "-arg", "b"},
			pos:  []string{"ref"}, args: []string{"a", "b"},
		},
		{
			name: "no args",
			in:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, dir, jsonOut, args := testFlagSet()
			pos, err := parseInterleaved(fs, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(pos, tc.pos) && !(len(pos) == 0 && len(tc.pos) == 0) {
				t.Fatalf("pos = %#v, want %#v", pos, tc.pos)
			}
			if tc.dir != "" && *dir != tc.dir {
				t.Fatalf("dir = %q", *dir)
			}
			if *jsonOut != tc.json {
				t.Fatalf("json = %v", *jsonOut)
			}
			if tc.args != nil && !reflect.DeepEqual([]string(*args), tc.args) {
				t.Fatalf("args = %#v", []string(*args))
			}
		})
	}
}

func TestParseInterleavedErrors(t *testing.T) {
	fs, _, _, _ := testFlagSet()
	if _, err := parseInterleaved(fs, []string{"ref", "-nope"}); err == nil {
		t.Fatal("expected error for unknown flag")
	}
	fs, _, _, _ = testFlagSet()
	if _, err := parseInterleaved(fs, []string{"-h"}); err != flag.ErrHelp {
		t.Fatalf("expected ErrHelp, got %v", err)
	}
}

func TestEnvList(t *testing.T) {
	got, err := envList([]string{"A=1", "B=x=y", " SP = v "})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "x=y", "SP": " v "}
	if len(got) != 3 {
		t.Fatalf("envList = %#v", got)
	}
	for _, e := range got {
		if e.Value != want[e.Name] {
			t.Fatalf("%s = %q, want %q", e.Name, e.Value, want[e.Name])
		}
	}
	for _, bad := range []string{"no-equals", "=v", " =v"} {
		if _, err := envList([]string{bad}); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}
