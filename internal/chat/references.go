package chat

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/store"
)

func validatePromptReferences(text string, refs []store.PromptReference) error {
	previous := 0
	for _, ref := range refs {
		if ref.Start < previous || ref.End <= ref.Start || ref.End > len(text) || !utf8.ValidString(text[:ref.Start]) || !utf8.ValidString(text[:ref.End]) || text[ref.Start:ref.End] != ref.Label || ref.ID == "" {
			return fmt.Errorf("invalid selected reference")
		}
		switch ref.Kind {
		case "file", "folder", "skill", "mcp", "command":
		default:
			return fmt.Errorf("invalid selected reference kind")
		}
		previous = ref.End
	}
	return nil
}

func referencesFromMessage(message store.Message) []store.PromptReference {
	var metadata struct {
		References []store.PromptReference `json:"references"`
	}
	if json.Unmarshal(message.Metadata, &metadata) != nil || validatePromptReferences(message.Text, metadata.References) != nil {
		return nil
	}
	return metadata.References
}

func promptIsCommand(text string, refs []store.PromptReference) bool {
	for _, ref := range refs {
		if ref.Kind == "command" {
			return true
		}
	}
	if len(refs) > 0 && refs[0].Start == 0 && strings.HasPrefix(refs[0].Label, "/") {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(text), "/")
}

// promptReferences preserves the displayed message in storage while mapping
// selected references to ACP content for the agent.
func promptReferences(text string, refs []store.PromptReference) (string, []acp.ContentBlock, bool) {
	var details []string
	var blocks []acp.ContentBlock
	command := ""
	for _, ref := range refs {
		switch ref.Kind {
		case "command":
			if command == "" {
				command = "/" + strings.TrimPrefix(ref.ID, "/")
				text = strings.TrimSpace(text[:ref.Start] + text[ref.End:])
			}
		case "file":
			details = append(details, fmt.Sprintf("%s refers to file %s.", ref.Label, ref.ID))
			blocks = append(blocks, acp.ContentBlock{Type: "resource_link", URI: fileURI(ref.ID), Name: filepath.Base(ref.ID)})
		case "folder":
			details = append(details, fmt.Sprintf("%s refers to folder %s.", ref.Label, ref.ID))
		case "skill":
			details = append(details, fmt.Sprintf("%s selects the skill at %s; read its SKILL.md and follow its instructions for this request.", ref.Label, ref.ID))
		case "mcp":
			details = append(details, fmt.Sprintf("%s selects MCP server %s; use its tools when appropriate for this request.", ref.Label, ref.ID))
		}
	}
	if command != "" {
		text = strings.TrimSpace(command + " " + text)
	} else if len(refs) > 0 && refs[0].Start == 0 && strings.HasPrefix(refs[0].Label, "/") {
		// A selected file such as /README.md is a reference, not an agent command.
		text = "Reference: " + text
	}
	if len(details) > 0 {
		blocks = append(blocks, acp.ContentBlock{Type: "text", Text: "Selected references:\n" + strings.Join(details, "\n")})
	}
	return text, blocks, command != ""
}
