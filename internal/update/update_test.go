package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/events"
)

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.2.0", "0.1.0", true},
		{"v0.2.0", "0.1.0", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.2.0", false},
		{"1.0.0", "0.9.9", true},
		{"0.1.1", "0.1.0", true},
		{"0.10.0", "0.9.9", true},
		{"0.2.0-beta.1", "0.1.0", true},
		{"bogus", "0.1.0", true},
		{"0.1.0", "dev", true},
		{"dev", "dev", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.latest, c.current); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

// fakeRelease serves a GitHub "latest release" JSON plus the assets it lists.
func fakeRelease(t *testing.T, tag string, binary []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/x/y/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		asset := assetName()
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://example.test/rel","assets":[`+
			`{"name":%q,"browser_download_url":"%s/bin"},`+
			`{"name":"SHA256SUMS","browser_download_url":"%s/sums"}]}`, tag, asset, srv.URL, srv.URL)
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binary)
	})
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256(binary)
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), assetName())
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type captureBus struct {
	events atomic.Int32
}

func (c *captureBus) Broadcast(events.Event) { c.events.Add(1) }

func newTestManager(t *testing.T, srv *httptest.Server, exe string) *Manager {
	t.Helper()
	m := New(Options{
		APIBase: srv.URL, Repo: "x/y", Current: "0.1.0",
		Bus: &captureBus{}, Client: srv.Client(),
	})
	m.exePath = exe
	m.status.CanApply = exe != ""
	return m
}

func TestCheckFindsNewRelease(t *testing.T) {
	srv := fakeRelease(t, "v0.2.0", []byte("#!/bin/sh\n"))
	m := newTestManager(t, srv, "")
	st := m.Check(context.Background())
	if st.Error != "" || !st.Available || st.Latest != "0.2.0" {
		t.Fatalf("unexpected status: %+v", st)
	}
	if st.CheckedAt == 0 {
		t.Fatal("checkedAt not set")
	}
}

func TestCheckNoReleases(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/x/y/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	m := newTestManager(t, srv, "")
	st := m.Check(context.Background())
	if st.Error != "" || st.Available {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestCheckOlderRelease(t *testing.T) {
	srv := fakeRelease(t, "v0.1.0", []byte("#!/bin/sh\n"))
	m := newTestManager(t, srv, "")
	st := m.Check(context.Background())
	if st.Available || st.Error != "" {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestApplyDownloadsVerifiesAndRestarts(t *testing.T) {
	binary := []byte("#!/bin/sh\necho updated\n")
	srv := fakeRelease(t, "v0.2.0", binary)
	dir := t.TempDir()
	exe := filepath.Join(dir, "nusamation")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, srv, exe)
	var restarted atomic.Bool
	m.restartFn = func(gotExe, staged string) error {
		if gotExe != exe {
			t.Errorf("restart exe = %q, want %q", gotExe, exe)
		}
		// On Windows the swap is deferred to the post-exit helper; the staged
		// file must already carry the verified bytes when restart starts.
		if runtime.GOOS == "windows" {
			if data, err := os.ReadFile(staged); err != nil || string(data) != string(binary) {
				t.Errorf("staged binary = %q, %v — want downloaded bytes", data, err)
			}
		}
		restarted.Store(true)
		return nil
	}
	if err := m.Apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for i := 0; i < 200 && !restarted.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !restarted.Load() {
		t.Fatal("restart hook never ran; status: " + m.Status().Error)
	}
	// Unix swaps the exe in-process; on Windows the detached helper moves the
	// staged file over it only after this process exits, so exe still reads
	// "old" here by design.
	if runtime.GOOS != "windows" {
		got, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(binary) {
			t.Fatalf("binary not swapped: %q", got)
		}
	}
	st := m.Status()
	if st.Stage != "restarting" || st.Error != "" {
		t.Fatalf("unexpected final status: %+v", st)
	}
}

func TestApplyRejectsWhileChecking(t *testing.T) {
	// A release endpoint that blocks until released lets us hold a check in
	// flight while Apply tries to start.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		fmt.Fprintf(w, `{"tag_name":"v0.2.0","html_url":"","assets":[]}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	exe := filepath.Join(dir, "nusamation")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, srv, exe)
	done := make(chan Status, 1)
	go func() { done <- m.Check(context.Background()) }()
	for i := 0; i < 200 && !m.Status().Checking; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.Apply(); err == nil {
		t.Fatal("Apply must refuse to start while a check is in flight")
	}
	close(release)
	st := <-done
	if st.Error != "" {
		t.Fatalf("check failed: %v", st.Error)
	}
}

func TestApplyRejectsDevBuild(t *testing.T) {
	srv := fakeRelease(t, "v0.2.0", []byte("x"))
	m := newTestManager(t, srv, "")
	if err := m.Apply(); err == nil {
		t.Fatal("expected apply to refuse a non-updatable binary")
	}
}

func TestApplyChecksumMismatchFails(t *testing.T) {
	// Checksums describe "good" while the downloaded binary is "tampered".
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256([]byte("good"))
		switch r.URL.Path {
		case "/repos/x/y/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v0.2.0","html_url":"","assets":[{"name":%q,"browser_download_url":"%s/bin"},{"name":"SHA256SUMS","browser_download_url":"%s/sums"}]}`, assetName(), srv.URL, srv.URL)
		case "/bin":
			_, _ = w.Write([]byte("tampered"))
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), assetName())
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	exe := filepath.Join(dir, "nusamation")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, srv, exe)
	m.restartFn = func(_, _ string) error { t.Fatal("must not restart on bad checksum"); return nil }
	if err := m.Apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for i := 0; i < 200 && m.Status().Error == ""; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	st := m.Status()
	if st.Applying || st.Error == "" || !strings.Contains(st.Error, "checksum") {
		t.Fatalf("expected checksum failure, got %+v", st)
	}
	if data, _ := os.ReadFile(exe); string(data) != "old" {
		t.Fatal("binary was replaced despite checksum failure")
	}
}
