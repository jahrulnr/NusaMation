package httpapi

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/store"
)

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "projects", err.Error())
		return
	}
	if projects == nil {
		projects = []store.Project{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name             string   `json:"name"`
		Sources          []string `json:"sources"`
		FoldersConfirmed bool     `json:"foldersConfirmed"`
	}
	if err := readJSON(w, r, &in, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Invalid project payload.")
		return
	}
	project, err := s.chat.CreateProject(r.Context(), in.Name, in.Sources, in.FoldersConfirmed)
	if err != nil {
		writeProjectError(w, err, "Project not found.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": project})
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name             *string   `json:"name"`
		Sources          *[]string `json:"sources"`
		Pinned           *bool     `json:"pinned"`
		UpdatedAt        *int64    `json:"updatedAt"`
		FoldersConfirmed *bool     `json:"foldersConfirmed"`
	}
	if err := readJSON(w, r, &in, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Invalid project payload.")
		return
	}
	patch := chat.ProjectPatch{
		Name:             in.Name,
		Pinned:           in.Pinned,
		FoldersConfirmed: in.FoldersConfirmed != nil && *in.FoldersConfirmed,
	}
	if in.Sources != nil {
		patch.SourcesSet = true
		patch.Sources = *in.Sources
	}
	if in.UpdatedAt != nil {
		patch.UpdatedAtSet = true
		patch.UpdatedAt = *in.UpdatedAt
	}
	project, err := s.chat.UpdateProject(r.Context(), r.PathValue("id"), patch)
	if err != nil {
		writeProjectError(w, err, "Project not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project})
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	project, ids, err := s.chat.RemoveProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProjectError(w, err, "Project not found.")
		return
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "conversationIds": ids})
}

func (s *Server) handleRestoreProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name            string   `json:"name"`
		Sources         []string `json:"sources"`
		Pinned          bool     `json:"pinned"`
		ConversationIDs []string `json:"conversationIds"`
	}
	if err := readJSON(w, r, &in, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Invalid project payload.")
		return
	}
	project, reattached, err := s.chat.RestoreProject(r.Context(), r.PathValue("id"), in.Name, in.Sources, in.Pinned, in.ConversationIDs)
	if err != nil {
		writeProjectError(w, err, "Project not found.")
		return
	}
	if reattached == nil {
		reattached = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "conversationIds": reattached})
}

func writeProjectError(w http.ResponseWriter, err error, notFound string) {
	var confirm *chat.ConfirmationRequiredError
	if errors.As(err, &confirm) {
		added := confirm.Added
		if added == nil {
			added = []string{}
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "Confirm the new folders before saving.",
			"code":  "confirmation_required",
			"added": added,
		})
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	if store.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "not_found", notFound)
		return
	}
	var invalid *chat.InvalidError
	if errors.As(err, &invalid) {
		writeError(w, http.StatusBadRequest, "invalid", invalid.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "project", err.Error())
}

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Server) handleListDirs(w http.ResponseWriter, r *http.Request) {
	listed, err := listChildDirs(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, listed)
}

func listChildDirs(path string) (map[string]any, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = home
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, errors.New("Folder does not exist or is not a directory.")
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	dirs := []dirEntry{}
	for _, entry := range entries {
		if !entry.IsDir() {
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			// IsDir does not follow links; a symlink to a folder still belongs
			// in the picker.
			info, err := os.Stat(filepath.Join(abs, entry.Name()))
			if err != nil || !info.IsDir() {
				continue
			}
		}
		dirs = append(dirs, dirEntry{Name: entry.Name(), Path: filepath.Join(abs, entry.Name())})
	}
	slices.SortFunc(dirs, func(a, b dirEntry) int { return strings.Compare(a.Name, b.Name) })
	parent := ""
	if filepath.Dir(abs) != abs {
		parent = filepath.Dir(abs)
	}
	return map[string]any{"path": abs, "parent": parent, "dirs": dirs}, nil
}
