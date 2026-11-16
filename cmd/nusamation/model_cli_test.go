package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/auth"
)

func modelTestServer(t *testing.T, dir string, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("nm_session")
		if err != nil {
			w.WriteHeader(401)
			return
		}
		ss, err := auth.LookupSession(context.Background(), st, cookie.Value)
		if err != nil || ss.CSRF != r.Header.Get("X-CSRF-Token") {
			w.WriteHeader(403)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestModelCmdSendsSelector(t *testing.T) {
	dir := t.TempDir()
	var gotSelector string
	srv := modelTestServer(t, dir, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/config") {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var in struct {
			Selector string `json:"selector"`
		}
		_ = json.Unmarshal(body, &in)
		gotSelector = in.Selector
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"conversation":{"id":"c1"},"configs":[],"result":{"model":"fake-smart","applied":[{"key":"effort","value":"high"}]}}`))
	})
	if err := modelCmd([]string{"-data-dir", dir, "-addr", strings.TrimPrefix(srv.URL, "http://"), "c1", "fake-smart[effort=high]"}); err != nil {
		t.Fatal(err)
	}
	if gotSelector != "fake-smart[effort=high]" {
		t.Fatalf("selector was not forwarded verbatim: %q", gotSelector)
	}
}

func TestModelCmdShowsAmbiguousCandidates(t *testing.T) {
	dir := t.TempDir()
	srv := modelTestServer(t, dir, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"\"flash\" matches more than one option","code":"config_failed","candidates":[{"value":"a-flash"},{"value":"b-flash"}]}`))
	})
	err := modelCmd([]string{"-data-dir", dir, "-addr", strings.TrimPrefix(srv.URL, "http://"), "c1", "flash"})
	if err == nil || !strings.Contains(err.Error(), "matches more than one") {
		t.Fatalf("expected the server's ambiguity error, got %v", err)
	}
}

func TestModelCmdPrintsActiveSelector(t *testing.T) {
	dir := t.TempDir()
	srv := modelTestServer(t, dir, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"conversation":{"id":"c1","model":""},"configs":[{"id":"model","category":"model","type":"select","currentValue":"fake-fast","options":[{"value":"fake-fast"}]}]}`))
	})
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := modelCmd([]string{"-data-dir", dir, "-addr", strings.TrimPrefix(srv.URL, "http://"), "c1"})
	_ = w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(r)
	if strings.TrimSpace(string(out)) != "fake-fast" {
		t.Fatalf("active selector = %q", out)
	}
}
