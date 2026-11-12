package httpapi

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// handleBrowserDevTools serves the frontend bundled with the session's Chrome
// and connects its websocket to that session's current page target.
func (s *Server) handleBrowserDevTools(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad_origin", "Request origin is not allowed.")
		return
	}
	sess := s.browserSession(w, r)
	if sess == nil {
		return
	}
	addr, targetID, err := sess.DevToolsEndpoint()
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	asset := r.PathValue("path")
	if asset == "" || strings.Contains(asset, "..") {
		http.NotFound(w, r)
		return
	}
	upstream := &url.URL{Scheme: "http", Host: addr}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme, req.URL.Host = "http", addr
		if asset == "socket" {
			req.URL.Path = "/devtools/page/" + targetID
		} else {
			req.URL.Path = "/devtools/" + asset
		}
		req.URL.RawPath = ""
		req.Host = addr
		req.Header.Set("Origin", "http://"+addr)
	}
	proxy.ModifyResponse = func(res *http.Response) error {
		res.Header.Set("X-Frame-Options", "SAMEORIGIN")
		res.Header.Set("Content-Security-Policy", "default-src 'self' data: blob:; script-src 'self' 'unsafe-inline' 'unsafe-eval' blob:; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data:; connect-src 'self' ws: wss: https: http:; worker-src 'self' blob:; frame-src 'self' blob:; frame-ancestors 'self'")
		return nil
	}
	w.Header().Del("X-Frame-Options")
	w.Header().Del("Content-Security-Policy")
	proxy.ServeHTTP(w, r)
}
