package httpapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/fsutil"
)

// Conversation meta is ephemeral UI state kept as a plain JSON file per
// conversation (<data>/conversations/{id}/meta.json) — greppable and editable
// without riding SQLite migrations.
type convMetaBrowser struct {
	LastURL string `json:"lastUrl,omitempty"`
	// Open is nil when the client has not said whether the pane was left
	// open. A pointer keeps an explicit false in the file.
	Open *bool `json:"open,omitempty"`
}

type convMetaDraft struct {
	Text           string            `json:"text"`
	Attachments    []json.RawMessage `json:"attachments,omitempty"`
	SendDelivery   string            `json:"sendDelivery,omitempty"`
	ReplyToChannel bool              `json:"replyToChannel,omitempty"`
	UpdatedAt      int64             `json:"updatedAt"`
}

type convMeta struct {
	Title   string           `json:"title,omitempty"`
	Browser *convMetaBrowser `json:"browser,omitempty"`
	Draft   *convMetaDraft   `json:"draft,omitempty"`
}

// convMetaPatch is a merge patch: fields absent are left alone, draft:null
// clears the draft (sent after a successful message send).
type convMetaPatch struct {
	Title   *string          `json:"title"`
	Browser *convMetaBrowser `json:"browser"`
	Draft   json.RawMessage  `json:"draft"`
}

func (s *Server) convMetaPath(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", errors.New("invalid conversation id")
	}
	return filepath.Join(s.cfg.DataDir, "conversations", id, "meta.json"), nil
}

func (s *Server) loadConvMeta(id string) (*convMeta, error) {
	path, err := s.convMetaPath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &convMeta{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := &convMeta{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveConvMeta(id string, m *convMeta) error {
	path, err := s.convMetaPath(id)
	if err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Dir(path), filepath.Base(path), data, 0o600)
}

// handleGetConversationMeta returns the file-backed UI meta; a missing file is
// an empty object, not an error.
func (s *Server) handleGetConversationMeta(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetConversation(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	s.metaMu.Lock()
	m, err := s.loadConvMeta(id)
	s.metaMu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "meta_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": m})
}

func (s *Server) handlePutConversationMeta(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetConversation(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	var patch convMetaPatch
	if err := readJSON(w, r, &patch, 256<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid meta update.")
		return
	}
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	m, err := s.loadConvMeta(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "meta_failed", err.Error())
		return
	}
	if patch.Title != nil {
		m.Title = *patch.Title
	}
	if patch.Browser != nil {
		// lastUrl and open merge independently. about:blank is the session
		// default, so storing it would erase the page the user actually visited.
		if patch.Browser.LastURL != "" && patch.Browser.LastURL != "about:blank" {
			if m.Browser == nil {
				m.Browser = &convMetaBrowser{}
			}
			m.Browser.LastURL = patch.Browser.LastURL
		}
		if patch.Browser.Open != nil {
			if m.Browser == nil {
				m.Browser = &convMetaBrowser{}
			}
			m.Browser.Open = patch.Browser.Open
		}
	}
	if len(patch.Draft) > 0 {
		if string(patch.Draft) == "null" {
			m.Draft = nil
		} else {
			d := &convMetaDraft{}
			if err := json.Unmarshal(patch.Draft, d); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "Invalid draft.")
				return
			}
			d.UpdatedAt = time.Now().UnixMilli()
			m.Draft = d
		}
	}
	if err := s.saveConvMeta(id, m); err != nil {
		writeError(w, http.StatusInternalServerError, "meta_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": m})
}

// dropConvMeta removes a deleted conversation's meta file; the directory is
// left for whatever else lands there later. metaMu keeps a racing PUT from
// recreating the file right after its existence check passed.
func (s *Server) dropConvMeta(id string) {
	if s.browserUse != nil {
		s.browserUse.Revoke(id)
	}
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if path, err := s.convMetaPath(id); err == nil {
		if err := os.RemoveAll(filepath.Dir(path)); err != nil {
			s.log.Warn("conversation meta not removed", "conversation", id, "error", err)
		}
	}
}
