package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jahrulnr/nusamation/internal/store"
)

// OrchestrationPolicy is stored in conversations.metadata_json under
// "orchestration". It decides whether this conversation may control others.
type OrchestrationPolicy struct {
	Enabled       bool   `json:"enabled"`
	ReadOnly      bool   `json:"readOnly"`
	Scope         string `json:"scope"` // "all" (default) or "project"
	Spawn         bool   `json:"spawn,omitempty"`
	ProgressPings bool   `json:"progressPings,omitempty"`
}

// ErrInvalidOrchestrationScope is returned for a scope other than all/project.
var ErrInvalidOrchestrationScope = errors.New("orchestration scope must be all or project")

// OrchestrationFrom decodes the policy from conversation metadata. Missing
// metadata means disabled; a corrupt value is treated as disabled rather than
// permissive.
func OrchestrationFrom(meta json.RawMessage) OrchestrationPolicy {
	var wrapper struct {
		Orchestration *OrchestrationPolicy `json:"orchestration"`
	}
	if len(meta) == 0 || json.Unmarshal(meta, &wrapper) != nil || wrapper.Orchestration == nil {
		return OrchestrationPolicy{}
	}
	p := *wrapper.Orchestration
	p.Scope = strings.ToLower(strings.TrimSpace(p.Scope))
	if p.Scope == "" {
		p.Scope = "all"
	}
	if p.Scope != "all" && p.Scope != "project" {
		return OrchestrationPolicy{}
	}
	return p
}

// SetOrchestration merges the policy into conversation metadata. The merge is
// serialized by patchMeta so concurrent metadata writers cannot lose keys.
func (s *Service) SetOrchestration(ctx context.Context, conversationID string, policy OrchestrationPolicy) (store.Conversation, error) {
	policy.Scope = strings.ToLower(strings.TrimSpace(policy.Scope))
	if policy.Scope == "" {
		policy.Scope = "all"
	}
	if policy.Scope != "all" && policy.Scope != "project" {
		return store.Conversation{}, ErrInvalidOrchestrationScope
	}
	return s.patchMeta(ctx, conversationID, map[string]any{"orchestration": policy})
}

// MarkSpawned records the parent conversation on a newly delegated chat. It
// uses patchMeta so a concurrent metadata writer cannot lose the link.
func (s *Service) MarkSpawned(ctx context.Context, childID, parentID string) (store.Conversation, error) {
	if childID == "" || parentID == "" {
		return store.Conversation{}, errors.New("child and parent conversation ids are required")
	}
	return s.patchMeta(ctx, childID, map[string]any{"spawnedBy": parentID})
}
