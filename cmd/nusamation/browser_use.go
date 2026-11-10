package main

import (
	"fmt"
	"os"

	"github.com/jahrulnr/nusamation/internal/mcp/browseruse"
)

// browser-use is the stdio MCP server named browser_use. The desktop process
// spawns it with the loopback URL and callback token; it has no flags.
func browserUseCmd() error {
	url := os.Getenv(browseruse.EnvURL)
	token := os.Getenv(browseruse.EnvToken)
	if url == "" || token == "" {
		return fmt.Errorf("mcp run --browser-use is started by NusaMation and needs %s and %s", browseruse.EnvURL, browseruse.EnvToken)
	}
	return browseruse.Serve(os.Stdin, os.Stdout, url, token)
}
