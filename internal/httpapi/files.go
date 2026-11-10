package httpapi

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var fileLineSuffix = regexp.MustCompile(`(?::[0-9]+){1,2}$`)

// handleConversationFile serves local artifacts linked by an agent. Relative
// paths use the conversation's working directory; absolute paths may point to
// generated artifacts elsewhere (for example screenshots under /tmp).
func (s *Server) handleConversationFile(w http.ResponseWriter, r *http.Request) {
	conv, err := s.store.GetConversation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "not_found", "Conversation not found.")
		return
	}
	base := conv.Cwd
	if base == "" {
		base = s.cfg.WorkspaceDir()
	}
	path, err := localFilePath(r.URL.Query().Get("path"), base)
	if err != nil {
		writeError(w, 400, "invalid_path", err.Error())
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		// Prefer exact filenames before interpreting editor :line:column suffixes.
		withoutLine := fileLineSuffix.ReplaceAllString(path, "")
		if withoutLine != path {
			path = withoutLine
			info, err = os.Stat(path)
		}
	}
	if err != nil {
		writeError(w, 404, "file_not_found", "The linked file is missing or cannot be read.")
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, 422, "not_a_file", "This link does not point to a regular file.")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, 404, "file_not_found", "The linked file is missing or cannot be read.")
		return
	}
	defer file.Close()
	kind := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if kind == "" {
		var buf [512]byte
		n, _ := file.Read(buf[:])
		kind = http.DetectContentType(buf[:n])
		_, _ = file.Seek(0, 0)
	}
	w.Header().Set("Content-Type", kind)
	setFileDisposition(w, r, filepath.Base(path), kind)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}

func localFilePath(raw, base string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", errors.New("A file path is required.")
	}
	if strings.HasPrefix(strings.ToLower(raw), "file:") {
		// Agents hand over file URLs verbatim — a bare "#" in the name is a
		// valid filename character, not a fragment marker, so escape it before
		// parsing or url.Parse silently truncates the path.
		u, err := url.Parse(strings.ReplaceAll(raw, "#", "%23"))
		if err != nil {
			return "", errors.New("Invalid file URL.")
		}
		raw = u.Path
		if u.Host != "" && u.Host != "localhost" {
			if runtime.GOOS == "windows" {
				if len(u.Host) == 2 && u.Host[1] == ':' {
					raw = u.Host + raw
				} else {
					raw = "//" + u.Host + raw
				}
			} else {
				return "", errors.New("The file URL must refer to this server.")
			}
		}
		if runtime.GOOS == "windows" && len(raw) > 2 && raw[0] == '/' && raw[2] == ':' {
			raw = raw[1:]
		}
	} else if strings.Contains(raw, "://") {
		return "", errors.New("Expected a local file path.")
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		raw = filepath.Join(home, raw[1:])
	}
	if raw == "" {
		return "", errors.New("A file path is required.")
	}
	raw = filepath.FromSlash(raw)
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(base, raw)
	}
	return filepath.Abs(raw)
}

func setFileDisposition(w http.ResponseWriter, r *http.Request, name, kind string) {
	disposition := "attachment"
	if r.URL.Query().Get("download") != "1" && (strings.HasPrefix(kind, "image/") || strings.HasPrefix(kind, "video/") || strings.HasPrefix(kind, "audio/") || kind == "application/pdf") {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
}
