package browser

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestLoginOfferRequiresDecisionAndScopesCredential(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	sub := newStream()
	s := &Session{m: &Manager{rec: st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}, profile: "work", streams: []*Stream{sub}}
	payload := `{"origin":"https://example.test","username":"alice","password":"secret-value"}`
	s.saveLoginPayload(payload)
	message := <-sub.Control()
	if strings.Contains(string(message), "secret-value") {
		t.Fatal("offer exposed password")
	}
	var m struct {
		Offer loginOffer `json:"offer"`
	}
	if err := json.Unmarshal(message, &m); err != nil {
		t.Fatal(err)
	}
	if m.Offer.ID == "" || m.Offer.Username != "alice" {
		t.Fatalf("offer = %+v", m.Offer)
	}
	credentials, err := st.BrowserCredentials(context.Background(), "work", "https://example.test")
	if err != nil || len(credentials) != 0 {
		t.Fatalf("stored before consent: %v %v", credentials, err)
	}
	s.saveLoginPayload(payload)
	if len(sub.ctrl) != 0 {
		t.Fatal("duplicate offer")
	}
	if err := s.DecideLoginOffer(context.Background(), "stale", true); err == nil {
		t.Fatal("stale offer accepted")
	}
	if err := s.DecideLoginOffer(context.Background(), m.Offer.ID, true); err != nil {
		t.Fatal(err)
	}
	credentials, err = st.BrowserCredentials(context.Background(), "work", "https://example.test")
	if err != nil || len(credentials) != 1 || credentials[0].Password != "secret-value" {
		t.Fatalf("saved credentials = %v, %v", credentials, err)
	}
	s.saveLoginPayload(payload)
	if s.pendingLogin != nil {
		t.Fatal("unchanged login offered again")
	}
	s.saveLoginPayload(`{"origin":"https://example.test","username":"alice","password":"changed"}`)
	id := s.pendingLogin.ID
	if err := s.DecideLoginOffer(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	credentials, _ = st.BrowserCredentials(context.Background(), "work", "https://example.test")
	if credentials[0].Password != "secret-value" {
		t.Fatal("dismissal changed saved credential")
	}
	s.saveLoginPayload(`{"origin":"https://example.test","username":"alice","password":"draft-value"}`)
	id = s.pendingLogin.ID
	username, password, err := s.LoginOfferDetails(id)
	if err != nil || username != "alice" || password != "draft-value" {
		t.Fatalf("editable offer = %q %q %v", username, password, err)
	}
	newUser, newPassword := "bob", "edited-password"
	if err := s.DecideLoginOfferEdited(context.Background(), id, true, &newUser, &newPassword); err != nil {
		t.Fatal(err)
	}
	credentials, err = st.BrowserCredentials(context.Background(), "work", "https://example.test")
	if err != nil || len(credentials) != 2 {
		t.Fatalf("multiple accounts = %v %v", credentials, err)
	}
	for _, c := range credentials {
		if c.Username == "bob" && c.Password != newPassword {
			t.Fatal("edited password not saved")
		}
	}
}

func TestVaultFillInBrowser(t *testing.T) {
	ctx, cancel := browserTestContext(t)
	defer cancel()
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(`<form><input type="email"><input type="password"></form>`)))
	s := secretSession(t, ctx)
	credential := store.BrowserCredential{Username: "alice@example.test", Password: "demo-vault-password"}
	if !s.fillLogin(credential) {
		t.Fatal("login not filled")
	}
	eval[string](t, ctx, `document.querySelector('input[type=password]').value = 'manually-typed'`)
	if s.fillLogin(credential) {
		t.Fatal("agent fill overwrote a manually typed password")
	}
	if !s.fillLoginMode(credential, true) {
		t.Fatal("explicit account selection did not fill")
	}
	if !s.fillLogin(credential) {
		t.Fatal("existing matching login should count as filled")
	}
	if got := s.MaskSecrets("value: demo-vault-password"); got != "value: [[vault:password]]" {
		t.Fatalf("mask = %q", got)
	}
	values := eval[[]string](t, ctx, `Array.from(document.querySelectorAll('input'), x => x.value)`)
	if len(values) != 2 || values[0] != credential.Username || values[1] != credential.Password {
		t.Fatalf("fields = %v", values)
	}
}
