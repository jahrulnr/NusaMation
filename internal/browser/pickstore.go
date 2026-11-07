package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusamation/internal/fsutil"
	"github.com/jahrulnr/nusamation/internal/store"
)

// pickRecord is the file-backed form of a Pick: descriptor + a snapshot of
// the marked-screenshot upload so a pick resolves after restart without the
// uploads table.
type pickRecord struct {
	ID             string                     `json:"id"`
	SessionID      string                     `json:"sessionId"`
	ConversationID string                     `json:"conversationId"`
	Marker         int                        `json:"marker"`
	Descriptor     store.AnnotationDescriptor `json:"descriptor"`
	Upload         store.Upload               `json:"upload"`
	CreatedAt      int64                      `json:"createdAt"`
}

// pickFile persists a pick as <dir>/<id>.json (atomic tmp+rename) — picks are
// ephemeral UI state, kept as plain files so they stay greppable and editable
// instead of riding the SQLite migrations.
func savePickFile(dir string, p *Pick) error {
	rec := pickRecord{
		ID: p.ID, SessionID: p.SessionID, ConversationID: p.ConversationID,
		Marker: p.Marker, Descriptor: p.Descriptor, Upload: p.Upload, CreatedAt: p.CreatedAt,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(dir, p.ID+".json", data, 0o600)
}

// validPickID mirrors convMetaPath's traversal guard: pick ids come from the
// request body and must never reach the filesystem unvalidated.
func validPickID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`) && !strings.Contains(id, "..")
}

func loadPickFile(dir, id string) (*Pick, error) {
	if !validPickID(id) {
		return nil, fmt.Errorf("invalid pick id %q", id)
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var rec pickRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	if rec.ID != id {
		return nil, fmt.Errorf("pick id mismatch: file %q holds %q", id, rec.ID)
	}
	return &Pick{
		ID: rec.ID, SessionID: rec.SessionID, ConversationID: rec.ConversationID,
		Marker: rec.Marker, Descriptor: rec.Descriptor, Upload: rec.Upload, CreatedAt: rec.CreatedAt,
	}, nil
}

// deleteSessionPickFiles removes pick files whose session_id matches; a read
// error on one file does not stop the sweep.
func deleteSessionPickFiles(dir, sessionID string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		// Only *.json — a .pick-*.tmp mid-write must never be swept under a
		// session id its half-written JSON happens to parse.
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var rec struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(data, &rec) == nil && rec.SessionID == sessionID {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// sweepPickFiles removes pick files older than the cutoff (createdAt field —
// falls back to mtime when the record cannot be parsed).
func sweepPickFiles(dir string, cutoff int64) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		stale := false
		if data, err := os.ReadFile(path); err == nil {
			var rec struct {
				CreatedAt int64 `json:"createdAt"`
			}
			if json.Unmarshal(data, &rec) == nil && rec.CreatedAt > 0 {
				stale = rec.CreatedAt < cutoff
			}
		}
		if !stale {
			if fi, err := e.Info(); err == nil {
				stale = fi.ModTime().UnixMilli() < cutoff
			}
		}
		if stale {
			if err := os.Remove(path); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
