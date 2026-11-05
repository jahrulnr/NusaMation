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

func (f *structuredFake) UserInfo(_ context.Context, jid string) (map[string]any, error) {
	return map[string]any{"jid": jid, "found": true}, f.err
}

func (*structuredFake) Name() string                { return "whatsapp" }
func (*structuredFake) Label() string               { return "WhatsApp" }
func (*structuredFake) Start(context.Context) error { return nil }
func (*structuredFake) Stop() error                 { return nil }
func (*structuredFake) Status() channels.Status     { return channels.Status{} }
func (*structuredFake) Begin(context.Context, channels.Target, string) (channels.Delivery, error) {
	return nil, errors.New("not implemented")
}

func TestWhatsAppUserEndpoint(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mgr := channels.NewManager(nil, nil)
	mgr.Register(&structuredFake{})
	s, err := New(Options{Store: st, Channels: mgr})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.CreateSession(context.Background(), st, "test", "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/channels/whatsapp/user?jid=123%40s.whatsapp.net", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"jid":"123@s.whatsapp.net"`) {
		t.Fatalf("status %d body %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/channels/whatsapp/user?jid=123%40s.whatsapp.net", nil)
	response = httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", response.Code)
	}
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
