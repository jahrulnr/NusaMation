package chat

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jahrulnr/nusamation/internal/store"
)

type ReferenceCandidate struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

// ReferenceCandidates uses the same cwd resolution as ACP session creation.
func (s *Service) ReferenceCandidates(ctx context.Context, conversationID, projectID, agentID, query string) ([]ReferenceCandidate, bool, error) {
	conv := store.Conversation{AgentID: agentID, ProjectID: projectID}
	if conversationID != "" {
		var err error
		conv, err = s.store.GetConversation(ctx, conversationID)
		if err != nil {
			return nil, false, err
		}
	} else if projectID != "" {
		if _, err := s.store.GetProject(ctx, projectID); err != nil {
			return nil, false, err
		}
	}
	ag, err := s.store.GetAgent(ctx, conv.AgentID)
	if err != nil {
		return nil, false, err
	}
	req := s.sessionRequest(ctx, conv, ag)
	roots := append([]string{req.Cwd}, req.AdditionalDirs...)
	q := strings.ToLower(query)
	items := []ReferenceCandidate{}
	seen := map[string]bool{}
	add := func(c ReferenceCandidate) {
		key := c.Kind + "\x00" + c.ID
		if !seen[key] && strings.Contains(strings.ToLower(c.Name+" "+c.Detail), q) {
			seen[key] = true
			items = append(items, c)
		}
	}
	mcps, err := s.store.ListMCP(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, server := range mcps {
		if server.Enabled {
			add(ReferenceCandidate{Kind: "mcp", ID: server.Name, Name: server.Name, Detail: "MCP server"})
		}
	}
	for _, name := range s.agents.InjectedMCPNames() {
		add(ReferenceCandidate{Kind: "mcp", ID: name, Name: name, Detail: "MCP server"})
	}

	home, _ := os.UserHomeDir()
	skillRoots := []string{}
	for _, base := range append(append([]string{}, roots...), home) {
		if base == "" {
			continue
		}
		for _, d := range []string{".agents/skills", ".codex/skills", ".claude/skills", ".opencode/skills"} {
			skillRoots = append(skillRoots, filepath.Join(base, filepath.FromSlash(d)))
		}
	}
	seenSkillDirs := map[string]bool{}
	truncated := false
	var visitSkills func(string, int)
	visitSkills = func(dir string, depth int) {
		if depth > 4 || ctx.Err() != nil {
			return
		}
		if len(seenSkillDirs) >= 5000 {
			truncated = true
			return
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil || seenSkillDirs[resolved] {
			return
		}
		seenSkillDirs[resolved] = true
		if info, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && info.Mode().IsRegular() {
			add(ReferenceCandidate{Kind: "skill", ID: dir, Name: filepath.Base(dir), Detail: dir})
			return
		}
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				visitSkills(path, depth+1)
			}
		}
	}
	for _, dir := range skillRoots {
		visitSkills(dir, 0)
	}
	visited := 0
	fileLimit := false
	for _, root := range roots {
		if root == "" {
			continue
		}
		ignore := openReferenceGitIgnore(ctx, root)
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if walkErr != nil {
				truncated = true
				return nil
			}
			if path == root {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			if ignore != nil {
				if d.Name() == ".git" {
					if d.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
				excluded, err := ignore.ignored(rel, d.IsDir())
				if err != nil {
					truncated = true
				} else if excluded {
					if d.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
			}
			visited++
			if visited > 50000 {
				truncated = true
				fileLimit = true
				return fs.SkipAll
			}
			isDir, isFile := d.IsDir(), d.Type().IsRegular()
			if d.Type()&os.ModeSymlink != 0 {
				if info, err := os.Stat(path); err == nil {
					isDir, isFile = info.IsDir(), info.Mode().IsRegular()
				}
			}
			kind := "file"
			if isDir {
				kind = "folder"
			}
			if isFile || isDir {
				add(ReferenceCandidate{Kind: kind, ID: path, Name: d.Name(), Detail: filepath.ToSlash(rel)})
			}
			return nil
		})
		if ignore != nil {
			ignore.close()
		}
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if fileLimit {
			break
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)
		ai, bi := strings.HasPrefix(a, q), strings.HasPrefix(b, q)
		if ai != bi {
			return ai
		}
		if a != b {
			return a < b
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].ID < items[j].ID
	})
	counts := map[string]int{}
	limited := make([]ReferenceCandidate, 0, min(len(items), 100))
	for _, item := range items {
		if counts[item.Kind] >= 25 || len(limited) >= 100 {
			truncated = true
			continue
		}
		counts[item.Kind]++
		limited = append(limited, item)
	}
	return limited, truncated, nil
}
