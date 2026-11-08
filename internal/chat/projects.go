package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusamation/internal/store"
)

// ErrProjectNotFound is an unknown project id on conversation create.
var ErrProjectNotFound = errors.New("project not found")

// InvalidError is a rejected project payload.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string {
	if e == nil || e.Reason == "" {
		return "invalid"
	}
	return e.Reason
}

func invalidProject(reason string) error { return &InvalidError{Reason: reason} }

// ConfirmationRequiredError means new folders were not confirmed. Nothing was written.
type ConfirmationRequiredError struct {
	Added []string
}

func (e *ConfirmationRequiredError) Error() string { return "confirmation required" }

type conflictError struct{ msg string }

func (e *conflictError) Error() string { return e.msg }

func (e *conflictError) Unwrap() error { return store.ErrConflict }

func conflict(msg string) error { return &conflictError{msg: msg} }

// ProjectPatch is a partial project update. An omitted pin stays as stored.
// SourcesSet replaces the whole folder list.
type ProjectPatch struct {
	Name             *string
	Sources          []string
	SourcesSet       bool
	Pinned           *bool
	UpdatedAt        int64
	UpdatedAtSet     bool
	FoldersConfirmed bool
}

func (s *Service) CreateProject(ctx context.Context, name string, sources []string, foldersConfirmed bool) (store.Project, error) {
	roots, err := cleanRoots(sources)
	if err != nil {
		return store.Project{}, err
	}
	if !foldersConfirmed {
		return store.Project{}, &ConfirmationRequiredError{Added: append([]string{}, roots...)}
	}
	project, err := s.store.InsertProject(ctx, projectName(name, roots[0]), roots)
	if err != nil {
		return store.Project{}, err
	}
	s.emit("project.updated", "", project)
	return project, nil
}

func (s *Service) UpdateProject(ctx context.Context, id string, patch ProjectPatch) (store.Project, error) {
	save := patch.SourcesSet || patch.Name != nil
	if !save {
		if patch.Pinned == nil {
			return store.Project{}, invalidProject("Nothing to update.")
		}
		return s.placeProject(ctx, id, patch.Pinned)
	}
	if !patch.UpdatedAtSet {
		return store.Project{}, invalidProject("updatedAt is required to save name or folders.")
	}
	cur, err := s.store.GetProject(ctx, id)
	if err != nil {
		return store.Project{}, err
	}
	roots := append([]string{}, cur.RootPaths...)
	if patch.SourcesSet {
		roots, err = cleanRoots(patch.Sources)
		if err != nil {
			return store.Project{}, err
		}
	}
	if len(roots) == 0 {
		return store.Project{}, invalidProject("A project needs at least one folder.")
	}
	name := cur.Name
	if patch.Name != nil {
		name = projectName(*patch.Name, roots[0])
	}
	added := addedPaths(cur.RootPaths, roots)
	if len(added) > 0 && !patch.FoldersConfirmed {
		return store.Project{}, &ConfirmationRequiredError{Added: added}
	}
	// metaMu covers the member metadata write so a concurrent patchMeta cannot
	// put the previous additionalDirectories back.
	s.metaMu.Lock()
	project, changed, memberIDs, err := s.store.SaveProjectSources(ctx, id, name, roots, patch.UpdatedAt, patch.Pinned)
	s.metaMu.Unlock()
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.Project{}, conflict("The project was updated somewhere else. Reload it and try again.")
		}
		return store.Project{}, err
	}
	if changed {
		s.markRootReset(ctx, memberIDs)
		s.emitConversations(ctx, memberIDs)
	}
	s.emit("project.updated", "", project)
	return project, nil
}

func (s *Service) placeProject(ctx context.Context, id string, pinned *bool) (store.Project, error) {
	project, err := s.store.PlaceProject(ctx, id, pinned)
	if err != nil {
		return store.Project{}, err
	}
	s.emit("project.updated", "", project)
	return project, nil
}

func (s *Service) RemoveProject(ctx context.Context, id string) (store.Project, []string, error) {
	project, ids, err := s.store.DeleteProject(ctx, id)
	if err != nil {
		return store.Project{}, nil, err
	}
	s.emit("project.updated", "", map[string]any{"id": project.ID, "deleted": true})
	s.emitConversations(ctx, ids)
	return project, ids, nil
}

// RestoreProject re-inserts a deleted project and reattaches the members that
// are still unassigned. The returned ids are the conversations that rejoined.
func (s *Service) RestoreProject(ctx context.Context, id, name string, sources []string, pinned bool, conversationIDs []string) (store.Project, []string, error) {
	roots, err := cleanRoots(sources)
	if err != nil {
		return store.Project{}, nil, err
	}
	if _, err := s.store.GetProject(ctx, id); err == nil {
		return store.Project{}, nil, conflict("That project already exists.")
	} else if !store.IsNotFound(err) {
		return store.Project{}, nil, err
	}
	s.metaMu.Lock()
	project, reattached, resetIDs, err := s.store.RestoreProject(ctx, id, projectName(name, roots[0]), roots, pinned, conversationIDs)
	s.metaMu.Unlock()
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.Project{}, nil, conflict("That project already exists.")
		}
		return store.Project{}, nil, err
	}
	s.markRootReset(ctx, resetIDs)
	s.emit("project.updated", "", project)
	s.emitConversations(ctx, reattached)
	return project, reattached, nil
}

func (s *Service) emitConversations(ctx context.Context, ids []string) {
	for _, id := range ids {
		conv, err := s.store.GetConversation(ctx, id)
		if err != nil {
			continue
		}
		s.emit("conversation.updated", id, conv)
	}
}

// markRootReset drops idle sessions whose folders changed. A running turn or an
// open session preparation keeps the loaded session until finishActive or the
// last endPrepare. The active/preparing check, the pending flag, and the busy
// flag share s.mu. An idle drop sets the busy flag before releasing the lock,
// and setActive waits until that drop finishes, so a turn cannot publish itself
// and then lose the session. ResetConversation runs without s.mu.
func (s *Service) markRootReset(ctx context.Context, conversationIDs []string) {
	if len(conversationIDs) == 0 {
		return
	}
	idle := make([]string, 0, len(conversationIDs))
	s.mu.Lock()
	if s.pendingRootReset == nil {
		s.pendingRootReset = map[string]bool{}
	}
	if s.rootResetBusy == nil {
		s.rootResetBusy = map[string]bool{}
	}
	for _, id := range conversationIDs {
		if id == "" {
			continue
		}
		if s.active[id] != nil || s.preparing[id] > 0 {
			s.pendingRootReset[id] = true
			continue
		}
		if s.rootResetBusy[id] {
			continue
		}
		s.rootResetBusy[id] = true
		idle = append(idle, id)
	}
	s.mu.Unlock()
	for _, id := range idle {
		s.dropIdleSession(ctx, id)
	}
}

func (s *Service) dropIdleSession(ctx context.Context, id string) {
	defer s.endRootResetBusy(id)
	s.resetConversation(ctx, id)
}

func cleanRoots(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, invalidProject("A project needs at least one folder.")
	}
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, invalidProject("Folder does not exist or is not a directory.")
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, invalidProject("Folder does not exist or is not a directory.")
		}
		abs = filepath.Clean(abs)
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return nil, invalidProject("Folder does not exist or is not a directory.")
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	if len(out) == 0 {
		return nil, invalidProject("A project needs at least one folder.")
	}
	return out, nil
}

func projectName(name, primary string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(primary)
	}
	return name
}

func addedPaths(stored, next []string) []string {
	have := make(map[string]bool, len(stored))
	for _, p := range stored {
		have[p] = true
	}
	added := []string{}
	for _, p := range next {
		if !have[p] {
			added = append(added, p)
		}
	}
	return added
}
