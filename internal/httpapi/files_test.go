package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConversationFiles(t *testing.T) {
	f := newControlFixture(t)
	dir := t.TempDir()
	conv, err := f.store.CreateConversation(context.Background(), f.agent.ID, "files", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	name := "report #1 + ü.txt"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("hello preview"), 0600); err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	if filepath.VolumeName(path) != "" {
		uri = (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(path)}).String()
	}
	refs := []string{path, name, uri, path + ":12:3"}
	if runtime.GOOS == "windows" {
		refs = append(refs, "file://"+filepath.ToSlash(path))
	}
	for _, ref := range refs {
		req := httptest.NewRequest("GET", "/api/conversations/"+conv.ID+"/files?path="+url.QueryEscape(ref), nil)
		req.SetPathValue("id", conv.ID)
		rr := httptest.NewRecorder()
		f.srv.handleConversationFile(rr, req)
		if rr.Code != http.StatusOK || rr.Body.String() != "hello preview" {
			t.Fatalf("%q: status=%d body=%s", ref, rr.Code, rr.Body.String())
		}
		if !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/plain") {
			t.Fatal(rr.Header())
		}
	}
	for _, tc := range []struct {
		ref    string
		status int
	}{{"", 400}, {"missing.png", 404}, {dir, 422}, {"https://example.com/x", 400}} {
		req := httptest.NewRequest("GET", "/files?path="+url.QueryEscape(tc.ref), nil)
		req.SetPathValue("id", conv.ID)
		rr := httptest.NewRecorder()
		f.srv.handleConversationFile(rr, req)
		if rr.Code != tc.status {
			t.Fatalf("%q: status=%d body=%s", tc.ref, rr.Code, rr.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "/files?path="+url.QueryEscape(path)+"&download=1", nil)
	req.SetPathValue("id", conv.ID)
	req.Header.Set("Range", "bytes=0-4")
	rr := httptest.NewRecorder()
	f.srv.handleConversationFile(rr, req)
	if rr.Code != 206 || rr.Body.String() != "hello" || !strings.HasPrefix(rr.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("range/download: %d %s %v", rr.Code, rr.Body.String(), rr.Header())
	}
	req = httptest.NewRequest("GET", "/api/conversations/"+conv.ID+"/files?path="+url.QueryEscape(path), nil)
	rr = httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("unauthenticated file = %d", rr.Code)
	}
}

// A bare "#" in a file URL is a filename character, not a fragment marker —
// the path must keep everything after it.
func TestLocalFilePathBareHash(t *testing.T) {
	dir := t.TempDir()
	got, err := localFilePath("file://"+filepath.ToSlash(dir)+"/report #1.txt", dir)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(filepath.Join(dir, "report #1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
