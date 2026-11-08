package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/browseruse"
)

func TestBrowserUseAuth(t *testing.T) {
	grants := browseruse.NewGrants("")
	token := grants.Token("cv_test")
	s := &Server{browserUse: grants}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/browser-use", s.handleBrowserUse)

	post := func(auth, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/browser-use", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}
	if post("", `{"tool":"snapshot"}`) != http.StatusUnauthorized {
		t.Fatal("missing token")
	}
	if post("Bearer nope", `{"tool":"snapshot"}`) != http.StatusUnauthorized {
		t.Fatal("bad token")
	}
	if post("Bearer "+token, `{"tool":"nope"}`) != http.StatusBadRequest {
		t.Fatal("unknown tool should be rejected before launching a browser")
	}
}
