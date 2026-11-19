package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// animalLabels are the auto-assigned room handles, in preference order: the
// oldest rooms get the most memorable animals. Single lowercase ASCII words
// so they are easy to @mention from the composer.
var animalLabels = []string{
	"cat", "dog", "tiger", "lion", "wolf", "fox", "bear", "eagle", "hawk",
	"falcon", "owl", "raven", "crow", "panther", "leopard", "cheetah", "jaguar",
	"cougar", "lynx", "bobcat", "coyote", "jackal", "hyena", "otter", "seal",
	"whale", "dolphin", "orca", "shark", "ray", "turtle", "frog", "toad",
	"salamander", "newt", "gecko", "iguana", "lizard", "viper", "cobra",
	"python", "boar", "bison", "moose", "elk", "deer", "marten", "badger",
	"ferret", "weasel", "mink", "mole", "shrew", "hedgehog", "rabbit", "hare",
	"squirrel", "chipmunk", "beaver", "marmot", "hamster", "gerbil", "vole",
	"mouse", "rat", "bat", "gorilla", "chimp", "baboon", "macaque", "lemur",
	"sloth", "tapir", "rhino", "hippo", "elephant", "giraffe", "zebra", "camel",
	"alpaca", "llama", "goat", "sheep", "pony", "horse", "donkey", "mule",
	"yak", "ox", "buffalo", "kangaroo", "wombat", "koala", "panda", "platypus",
	"echidna", "possum", "armadillo", "anteater", "aardvark", "porcupine",
	"capybara", "chinchilla", "pelican", "flamingo", "penguin", "puffin",
	"heron", "crane", "stork", "egret", "ibis", "swan", "goose", "duck",
	"loon", "gull", "sparrow", "robin", "wren", "finch", "jay", "magpie",
	"dove", "emu", "kiwi", "toucan", "macaw", "parrot", "peacock", "vulture",
	"condor", "ostrich", "octopus", "squid", "crab", "shrimp", "lobster",
	"clam", "mantis", "beetle", "cricket", "hornet", "wasp", "dragonfly",
	"firefly", "scorpion",
}

// ErrLabelTaken reports a label that another conversation already owns.
var ErrLabelTaken = errors.New("label already taken")

// NormalizeLabel canonicalizes an operator-supplied label: lowercase, no
// surrounding space. An empty result means "clear".
func NormalizeLabel(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// ValidLabel reports whether a normalized label is mention-safe: 1–24
// characters of a–z, 0–9 and hyphen, not starting or ending with a hyphen.
func ValidLabel(label string) bool {
	if len(label) < 1 || len(label) > 24 {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return !strings.HasPrefix(label, "-") && !strings.HasSuffix(label, "-")
}

// conversationLabels loads every label a conversation owns, lowercased.
func (s *Store) conversationLabels(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT label FROM conversations WHERE label <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[string]bool{}
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}
		used[strings.ToLower(label)] = true
	}
	return used, rows.Err()
}

// pickLabel returns the first animal missing from used, falling back to
// animal-2, animal-3, … when every base name is taken. Empty when nothing
// is free — practically unreachable.
func pickLabel(used map[string]bool) string {
	for _, a := range animalLabels {
		if !used[a] {
			return a
		}
	}
	for _, a := range animalLabels {
		for n := 2; n < 1000; n++ {
			if c := fmt.Sprintf("%s-%d", a, n); !used[c] {
				return c
			}
		}
	}
	return ""
}

// nextConversationLabel picks a label free under the current rows.
func (s *Store) nextConversationLabel(ctx context.Context) (string, error) {
	used, err := s.conversationLabels(ctx)
	if err != nil {
		return "", err
	}
	return pickLabel(used), nil
}

// backfillConversationLabels assigns an animal to every pre-label
// conversation, oldest first, so existing rooms become mentionable right
// after the upgrade. Best-effort: a row that fails keeps its empty label
// and stays reachable by id and title.
func (s *Store) backfillConversationLabels(ctx context.Context) {
	used, err := s.conversationLabels(ctx)
	if err != nil {
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM conversations WHERE label = '' ORDER BY created_at, id`)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}
	for _, id := range ids {
		label := pickLabel(used)
		if label == "" {
			return
		}
		used[label] = true
		// The WHERE keeps a concurrent manual rename from being clobbered.
		_, _ = s.db.ExecContext(ctx, `UPDATE conversations SET label=? WHERE id=? AND label=''`, label, id)
	}
}

// SetConversationLabel stores a validated, normalized label. An empty label
// clears the handle. Returns ErrNotFound for an unknown conversation and
// ErrLabelTaken when another row already owns the label.
func (s *Store) SetConversationLabel(ctx context.Context, id, label string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE conversations SET label=? WHERE id=?`, label, id)
	if err != nil {
		if isUniqueLabelError(err) {
			return ErrLabelTaken
		}
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// FindConversationByLabel resolves a mention handle to its conversation.
func (s *Store) FindConversationByLabel(ctx context.Context, label string) (Conversation, error) {
	row := s.db.QueryRowContext(ctx, conversationSelect+" WHERE c.label=?", NormalizeLabel(label))
	return s.scanConversation(row)
}

func isUniqueLabelError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "label")
}
