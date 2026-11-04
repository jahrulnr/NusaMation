package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/auth"
)

func TestWhatsAppSendUsesRunningServer(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("nm_session")
		if err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		ss, err := auth.LookupSession(context.Background(), st, cookie.Value)
		if err != nil || ss.CSRF != r.Header.Get("X-CSRF-Token") {
			t.Errorf("invalid temporary session: %v", err)
			w.WriteHeader(403)
			return
		}
		body, _ := io.ReadAll(r.Body)
		called = r.URL.Path == "/api/channels/whatsapp/send" && strings.Contains(string(body), `"type":"poll"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messageId":"WAID"}`))
	}))
	defer srv.Close()
	f, err := os.CreateTemp(dir, "request")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.WriteString(`{"target":"123@s.whatsapp.net","type":"poll","text":"Q","options":["A","B"]}`)
	_, _ = f.Seek(0, 0)
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()
	if err := whatsappSend([]string{"-data-dir", dir, "-addr", strings.TrimPrefix(srv.URL, "http://")}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("server did not receive poll")
	}
}
