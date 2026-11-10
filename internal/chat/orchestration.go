package chat

import (
	"context"
	"errors"

	"github.com/jahrulnr/nusamation/internal/store"
)

// MarkSpawned records the parent conversation on a newly delegated chat. It
// uses patchMeta so a concurrent metadata writer cannot lose the link.
func (s *Service) MarkSpawned(ctx context.Context, childID, parentID string) (store.Conversation, error) {
	if childID == "" || parentID == "" {
		return store.Conversation{}, errors.New("child and parent conversation ids are required")
	}
	return s.patchMeta(ctx, childID, map[string]any{"spawnedBy": parentID})
}
