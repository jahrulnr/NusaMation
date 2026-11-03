package agent

import "os/exec"

// KnownCLI describes a popular agent CLI that speaks ACP over stdio. Candidates
// are tried in order; the first binary found on PATH wins. Each candidate is an
// argv slice where element 0 is the binary name and the rest are arguments.
type KnownCLI struct {
	Name        string
	Slug        string
	Description string
	Icon        string
	Color       string
	Candidates  [][]string
}

// DetectedCLI is a KnownCLI whose binary was found on PATH, with the resolved
// binary path and the arguments to launch it in ACP mode.
type DetectedCLI struct {
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	Color       string   `json:"color"`
	Command     string   `json:"command"`
	Args        []string `json:"args"`
}

// KnownCLIs lists popular ACP-capable CLIs, ordered by preference for the
// first-run seed. Entries follow the ACP registry invocations.
var KnownCLIs = []KnownCLI{
	{
		Name: "Devin", Slug: "devin", Description: "Cognition Devin over ACP",
		Icon: "sparkles", Color: "amber",
		Candidates: [][]string{{"devin", "acp"}},
	},
	{
		Name: "Claude Agent", Slug: "claude", Description: "Anthropic Claude via the ACP adapter",
		Icon: "brain", Color: "violet",
		Candidates: [][]string{{"claude-agent-acp"}, {"claude-code-acp"}},
	},
	{
		Name: "Codex", Slug: "codex", Description: "OpenAI Codex via the ACP adapter",
		Icon: "terminal", Color: "emerald",
		Candidates: [][]string{{"codex-acp"}, {"codex", "acp"}},
	},
	{
		Name: "Gemini CLI", Slug: "gemini", Description: "Google Gemini CLI in ACP mode",
		Icon: "star", Color: "blue",
		Candidates: [][]string{{"gemini", "--acp"}, {"gemini", "--experimental-acp"}},
	},
	{
		Name: "GitHub Copilot", Slug: "copilot", Description: "GitHub Copilot CLI in ACP mode",
		Icon: "globe", Color: "slate",
		Candidates: [][]string{{"copilot", "--acp"}},
	},
	{
		Name: "OpenCode", Slug: "opencode", Description: "OpenCode agent in ACP mode",
		Icon: "terminal", Color: "rose",
		Candidates: [][]string{{"opencode", "acp"}},
	},
	{
		Name: "Auggie", Slug: "auggie", Description: "Augment Code Auggie in ACP mode",
		Icon: "zap", Color: "amber",
		Candidates: [][]string{{"auggie", "--acp"}},
	},
	{
		Name: "goose", Slug: "goose", Description: "Block goose agent in ACP mode",
		Icon: "star", Color: "emerald",
		Candidates: [][]string{{"goose", "acp"}},
	},
	{
		Name: "Kimi CLI", Slug: "kimi", Description: "Moonshot AI Kimi CLI in ACP mode",
		Icon: "sparkles", Color: "blue",
		Candidates: [][]string{{"kimi", "acp"}},
	},
	{
		Name: "Cursor", Slug: "cursor", Description: "Cursor agent CLI in ACP mode",
		Icon: "zap", Color: "violet",
		Candidates: [][]string{{"cursor-agent", "acp"}},
	},
	{
		Name: "Kilo", Slug: "kilo", Description: "Kilo Code agent in ACP mode",
		Icon: "zap", Color: "emerald",
		Candidates: [][]string{{"kilo", "acp"}},
	},
	{
		Name: "Qwen Code", Slug: "qwen", Description: "Alibaba Qwen Code in ACP mode",
		Icon: "sparkles", Color: "rose",
		Candidates: [][]string{{"qwen", "--acp"}},
	},
	{
		Name: "Mistral Vibe", Slug: "vibe", Description: "Mistral Vibe ACP agent",
		Icon: "globe", Color: "blue",
		Candidates: [][]string{{"vibe-acp"}},
	},
	{
		Name: "Kiro CLI", Slug: "kiro", Description: "AWS Kiro CLI in ACP mode",
		Icon: "sparkles", Color: "amber",
		Candidates: [][]string{
			{"kiro-cli", "acp", "--agent-engine=v3"},
			{"kiro-cli-chat", "acp", "--agent-engine=v3"},
		},
	},
}

// Detect returns each known CLI found on PATH, preserving catalog order.
func Detect() []DetectedCLI {
	var out []DetectedCLI
	for _, k := range KnownCLIs {
		for _, argv := range k.Candidates {
			if len(argv) == 0 {
				continue
			}
			path, err := exec.LookPath(argv[0])
			if err != nil {
				continue
			}
			out = append(out, DetectedCLI{
				Name: k.Name, Slug: k.Slug, Description: k.Description,
				Icon: k.Icon, Color: k.Color,
				Command: path, Args: append([]string(nil), argv[1:]...),
			})
			break
		}
	}
	return out
}
