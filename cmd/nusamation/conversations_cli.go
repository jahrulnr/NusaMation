package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jahrulnr/nusamation/internal/mcp/conversation"
	"github.com/jahrulnr/nusamation/internal/mcp/loopback"
)

// conversations is the thin loopback client for the nusamation_conversation
// MCP bridge. Agents normally use the MCP tools; this helps debugging and
// agents that only have a terminal.
func conversationsCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: nusamation conversations <list|read|wait|send|stop|spawn|report> [flags]")
	}
	switch args[0] {
	case "list":
		return conversationsList(args[1:])
	case "projects":
		return conversationsProjects(args[1:])
	case "search":
		return conversationsSearch(args[1:])
	case "export":
		return conversationsExport(args[1:])
	case "read":
		return conversationsRead(args[1:])
	case "wait":
		return conversationsWait(args[1:])
	case "send":
		return conversationsSend(args[1:])
	case "stop":
		return conversationsStop(args[1:])
	case "spawn":
		return conversationsSpawn(args[1:])
	case "report":
		return conversationsReport(args[1:])
	default:
		return fmt.Errorf("unknown conversations command %q", args[0])
	}
}

func conversationFlags(name, usage string) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	url := fs.String("url", os.Getenv(conversation.EnvURL), "conversation control loopback URL")
	token := fs.String("token", os.Getenv(conversation.EnvToken), "conversation control token")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	return fs, url, token
}

func conversationsProjects(args []string) error {
	fs, url, token := conversationFlags("conversations projects", "usage: nusamation conversations projects [--query Q] [--limit N]\n")
	query := fs.String("query", "", "project name or root path")
	limit := fs.Int("limit", 50, "rows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runConversationControl(*url, *token, map[string]any{"tool": "projects_list", "query": *query, "limit": *limit})
}

func conversationsSearch(args []string) error {
	fs, url, token := conversationFlags("conversations search", "usage: nusamation conversations search [--project P] [--agent A] [--channel C] [--kinds text,tool] [--after MS] [--before MS] [--limit N] <query>\n")
	project := fs.String("project", "", "project id or name")
	agent := fs.String("agent", "", "agent id or slug")
	channel := fs.String("channel", "", "origin channel")
	kinds := fs.String("kinds", "", "comma-separated kinds (text,error,tool,thought,system,notice)")
	after := fs.Int64("after", 0, "messages at or after this Unix ms")
	before := fs.Int64("before", 0, "messages at or before this Unix ms")
	archived := fs.Bool("archived", false, "search archived conversations")
	limit := fs.Int("limit", 20, "hits")
	offset := fs.Int("offset", 0, "hits to skip")
	if err := fs.Parse(args); err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		fs.Usage()
		return fmt.Errorf("query is required")
	}
	body := map[string]any{
		"tool": "search", "query": query, "project": *project, "agent": *agent,
		"channel": *channel, "after": *after, "before": *before,
		"archived": *archived, "limit": *limit, "offset": *offset,
	}
	if *kinds != "" {
		body["kinds"] = splitCSV(*kinds)
	}
	return runConversationControl(*url, *token, body)
}

func conversationsExport(args []string) error {
	fs, url, token := conversationFlags("conversations export", "usage: nusamation conversations export [--conversation ID] [--project P] [--thoughts] [--tool-output] [--archived] [--limit N]\n")
	conversationID := fs.String("conversation", "", "conversation id")
	project := fs.String("project", "", "project id or name")
	thoughts := fs.Bool("thoughts", false, "include thought rows")
	toolOutput := fs.Bool("tool-output", false, "include raw tool content")
	archived := fs.Bool("archived", false, "include archived conversations in a project export")
	limit := fs.Int("limit", 10000, "maximum messages")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runConversationControl(*url, *token, map[string]any{
		"tool": "export", "conversationId": *conversationID, "project": *project,
		"includeThoughts": *thoughts, "includeToolOutput": *toolOutput,
		"archived": *archived, "limit": *limit,
	})
}

func splitCSV(raw string) []string {
	out := []string{}
	for part := range strings.SplitSeq(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func conversationsList(args []string) error {
	fs, url, token := conversationFlags("conversations list", "usage: nusamation conversations list [--query Q] [--agent A] [--channel C] [--status S] [--archived] [--limit N] [--offset N]\n")
	query := fs.String("query", "", "title or message text")
	agent := fs.String("agent", "", "agent id or slug")
	channel := fs.String("channel", "", "origin channel")
	status := fs.String("status", "", "conversation status")
	archived := fs.Bool("archived", false, "list archived conversations")
	limit := fs.Int("limit", 30, "rows")
	offset := fs.Int("offset", 0, "rows to skip")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runConversationControl(*url, *token, map[string]any{
		"tool": "list", "query": *query, "agent": *agent, "channel": *channel,
		"status": *status, "archived": *archived, "limit": *limit, "offset": *offset,
	})
}

func conversationsRead(args []string) error {
	fs, url, token := conversationFlags("conversations read", "usage: nusamation conversations read [--after SEQ] [--before SEQ] [--limit N] [--thoughts] <conversation-id>\n")
	after := fs.Int64("after", 0, "messages after this seq")
	before := fs.Int64("before", 0, "messages before this seq")
	limit := fs.Int("limit", 30, "messages")
	thoughts := fs.Bool("thoughts", false, "include thought rows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("conversation id is required")
	}
	return runConversationControl(*url, *token, map[string]any{
		"tool": "read", "conversationId": fs.Arg(0), "afterSeq": *after, "beforeSeq": *before,
		"limit": *limit, "includeThoughts": *thoughts,
	})
}

func conversationsWait(args []string) error {
	fs, url, token := conversationFlags("conversations wait", "usage: nusamation conversations wait [--turn ID] [--after SEQ] [--timeout SECONDS] <conversation-id>\n")
	turn := fs.String("turn", "", "turn id to watch")
	after := fs.Int64("after", 0, "messages after this seq")
	timeout := fs.Int("timeout", 10, "seconds (max 30)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("conversation id is required")
	}
	return runConversationControl(*url, *token, map[string]any{
		"tool": "wait", "conversationId": fs.Arg(0), "turnId": *turn,
		"afterSeq": *after, "timeoutSeconds": *timeout,
	})
}

func conversationsSend(args []string) error {
	fs, url, token := conversationFlags("conversations send", "usage: nusamation conversations send [--delivery queue|steer|after_tools] [--request-id ID] <conversation-id> <text>\n")
	delivery := fs.String("delivery", "queue", "queue, steer, or after_tools")
	requestID := fs.String("request-id", "", "idempotency key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		fs.Usage()
		return fmt.Errorf("conversation id and text are required")
	}
	text := strings.Join(fs.Args()[1:], " ")
	return runConversationControl(*url, *token, map[string]any{
		"tool": "send", "conversationId": fs.Arg(0), "text": text,
		"delivery": *delivery, "requestId": *requestID,
	})
}

func conversationsStop(args []string) error {
	fs, url, token := conversationFlags("conversations stop", "usage: nusamation conversations stop [--turn ID] <conversation-id>\n")
	turn := fs.String("turn", "", "specific turn id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("conversation id is required")
	}
	return runConversationControl(*url, *token, map[string]any{
		"tool": "stop", "conversationId": fs.Arg(0), "turnId": *turn,
	})
}

func conversationsSpawn(args []string) error {
	fs, url, token := conversationFlags("conversations spawn", "usage: nusamation conversations spawn [--title T] [--prompt P] [--project ID] <agent>\n")
	title := fs.String("title", "", "conversation title")
	prompt := fs.String("prompt", "", "first prompt")
	project := fs.String("project", "", "project id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("agent is required")
	}
	return runConversationControl(*url, *token, map[string]any{
		"tool": "spawn", "agent": fs.Arg(0), "title": *title, "prompt": *prompt, "projectId": *project,
	})
}

func conversationsReport(args []string) error {
	fs, url, token := conversationFlags("conversations report", "usage: nusamation conversations report <text>\n")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(text) == "" {
		fs.Usage()
		return fmt.Errorf("text is required")
	}
	return runConversationControl(*url, *token, map[string]any{"tool": "report", "text": text})
}

func runConversationControl(url, token string, body map[string]any) error {
	if strings.TrimSpace(url) == "" || strings.TrimSpace(token) == "" {
		return fmt.Errorf("set %s and %s, or pass --url and --token", conversation.EnvURL, conversation.EnvToken)
	}
	var raw json.RawMessage
	if err := loopback.New(url, token).Post(context.Background(), "/api/conversation-control", body, &raw); err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return err
	}
	fmt.Println(pretty.String())
	return nil
}
