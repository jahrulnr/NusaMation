package main

import (
	"fmt"
	"os"

	"github.com/jahrulnr/nusamation/internal/mcp/conversation"
)

// conversation-mcp is the stdio MCP server named nusamation_conversation.
// The desktop process spawns it with the loopback URL and callback token; it
// has no flags.
func conversationMCPCmd() error {
	url := os.Getenv(conversation.EnvURL)
	token := os.Getenv(conversation.EnvToken)
	if url == "" || token == "" {
		return fmt.Errorf("mcp run --conversation is started by NusaMation and needs %s and %s", conversation.EnvURL, conversation.EnvToken)
	}
	return conversation.Serve(os.Stdin, os.Stdout, url, token)
}
