package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/channels/whatsapp"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

type structuredFake struct {
	req whatsapp.OutboundRequest
	err error
}

func (*structuredFake) Name() string                { return "whatsapp" }
func (*structuredFake) Label() string               { return "WhatsApp" }
func (*structuredFake) Start(context.Context) error { return nil }
func (*structuredFake) Stop() error                 { return nil }
func (*structuredFake) Status() channels.Status     { return channels.Status{} }
func (*structuredFake) Begin(context.Context, channels.Target, string) (channels.Delivery, error) {
	return nil, errors.New("not implemented")
}
func (*structuredFake) Notify(context.Context, channels.Target, string) error { return nil }
func (f *structuredFake) SendStructured(_ context.Context, req whatsapp.OutboundRequest) (string, error) {
	f.req = req
	return "WAID", f.err
}

func TestWhatsAppSendEndpoint(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	defer st.Close()
	mgr := channels.NewManager(nil, nil)
	f := &structuredFake{}
	mgr.Register(f)
	s, err := New(Options{Config: config.Config{}, Store: st, Channels: mgr})
	if err != nil {
		t.Fatal(err)
	}
	token, session, err := auth.CreateSession(context.Background(), st, "test", "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"target":"123@s.whatsapp.net","type":"poll","text":"Pick","options":["A","B"],"maxSelectable":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/channels/whatsapp/send", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	req.Header.Set("X-CSRF-Token", session.CSRF)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"messageId":"WAID"`) || f.req.Type != "poll" || len(f.req.Options) != 2 {
		t.Fatalf("status %d body %s req %#v", rec.Code, rec.Body.String(), f.req)
	}
	f.err = errors.New("poll sent, metadata failed")
	req = httptest.NewRequest(http.MethodPost, "/api/channels/whatsapp/send", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	req.Header.Set("X-CSRF-Token", session.CSRF)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"messageId":"WAID"`) || !strings.Contains(rec.Body.String(), `"send_partial"`) {
		t.Fatalf("partial response status %d body %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/channels/whatsapp/send", strings.NewReader(body))
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", rec.Code)
	}
}
