package loopback

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostSendsBearerAndDecodes(t *testing.T) {
	var gotPath, gotAuth, gotType, gotTool string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotTool, _ = body["tool"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "value": 7})
	}))
	defer srv.Close()

	c := New(srv.URL+"/", "tok")
	var out struct {
		OK    bool `json:"ok"`
		Value int  `json:"value"`
	}
	if err := c.Post(context.Background(), "/api/test", map[string]any{"tool": "read"}, &out); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/test" || gotAuth != "Bearer tok" || gotType != "application/json" || gotTool != "read" {
		t.Fatalf("path=%q auth=%q type=%q tool=%q", gotPath, gotAuth, gotType, gotTool)
	}
	if !out.OK || out.Value != 7 {
		t.Fatalf("out = %+v", out)
	}
}

func TestPostReturnsEnvelopeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "nope", "code": "orchestrated_turn"})
	}))
	defer srv.Close()

	err := New(srv.URL, "tok").Post(context.Background(), "/api/test", map[string]any{}, nil)
	var le *Error
	if !errors.As(err, &le) {
		t.Fatalf("err = %T %v", err, err)
	}
	if le.Status != http.StatusForbidden || le.Code != "orchestrated_turn" || le.Message != "nope" {
		t.Fatalf("error = %+v", le)
	}
}

func TestPostReturnsPlainTextError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	err := New(srv.URL, "tok").Post(context.Background(), "/api/test", map[string]any{}, nil)
	var le *Error
	if !errors.As(err, &le) || le.Status != http.StatusBadGateway || !strings.Contains(le.Message, "boom") {
		t.Fatalf("err = %#v", err)
	}
}

func TestPostRejectsUnconfiguredClient(t *testing.T) {
	if err := New("", "tok").Post(context.Background(), "/x", map[string]any{}, nil); err == nil {
		t.Fatal("expected unconfigured error")
	}
	if err := New("http://127.0.0.1", "").Post(context.Background(), "/x", map[string]any{}, nil); err == nil {
		t.Fatal("expected missing token error")
	}
}

func TestPostTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = &http.Client{Timeout: 20 * time.Millisecond}
	if err := c.Post(context.Background(), "/x", map[string]any{}, nil); err == nil {
		t.Fatal("expected timeout")
	}
}

func TestPostInvalidJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	var out map[string]any
	if err := New(srv.URL, "tok").Post(context.Background(), "/x", map[string]any{}, &out); err == nil || !strings.Contains(err.Error(), "loopback response") {
		t.Fatalf("err = %v", err)
	}
}

func TestPostNilOutDiscardsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	if err := New(srv.URL, "tok").Post(context.Background(), "/x", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
}
