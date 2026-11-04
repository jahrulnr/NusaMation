package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusamation/internal/security"
)

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 26<<20)
	if err := r.ParseMultipartForm(26 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "upload_failed", "The file is too large or the form is invalid.")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	convID := strings.TrimSpace(r.FormValue("conversationId"))
	if convID != "" {
		if _, err := s.store.GetConversation(r.Context(), convID); err != nil {
			writeError(w, http.StatusBadRequest, "conversation_not_found", "Conversation not found.")
			return
		}
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file_required", "Choose a file to upload.")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (25<<20)+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "upload_failed", "Could not read the uploaded file.")
		return
	}
	if len(data) > 25<<20 {
		writeError(w, http.StatusBadRequest, "file_too_large", "The uploaded file exceeds the 25 MB limit.")
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "empty_file", "The uploaded file is empty.")
		return
	}
	mime := header.Header.Get("Content-Type")
	if mime == "" || mime == "application/octet-stream" {
		mime = http.DetectContentType(data)
	}
	up, err := s.chat.SaveInboundAttachment(r.Context(), convID, header.Filename, mime, data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upload_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"upload": up})
}

func (s *Server) handleGetUpload(w http.ResponseWriter, r *http.Request) {
	up, err := s.store.GetUpload(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Attachment not found.")
		return
	}
	path, err := security.SafePath(s.cfg.UploadDir(), up.Path, true)
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "Attachment path is not accessible.")
		return
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		writeError(w, http.StatusNotFound, "not_found", "Attachment file is missing.")
		return
	}
	if up.MIME == "" {
		up.MIME = "application/octet-stream"
	}
	w.Header().Set("Content-Type", up.MIME)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", st.Size()))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	disposition := "attachment"
	if strings.HasPrefix(up.MIME, "image/") || strings.HasPrefix(up.MIME, "video/") || strings.HasPrefix(up.MIME, "audio/") || up.MIME == "application/pdf" {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, filepath.Base(up.Name)))
	http.ServeFile(w, r, path)
}
