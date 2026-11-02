package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jahrulnr/nusamation/internal/ids"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)

func NormalizeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.TrimRight(out[:40], "-")
	}
	if out == "" {
		out = "agent-" + ids.New("")[2:8]
	}
	return out
}

func ValidSlug(s string) bool { return slugRe.MatchString(s) }

func (s *Store) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.name, a.slug, a.description, a.command, a.args_json, a.env_enc, a.cwd,
		       a.icon, a.color, a.enabled, a.auto_start, a.approval_policy, a.default_model,
		       a.models_json, a.config_options_json, a.capabilities_json, a.auth_methods_json,
		       a.status, a.last_error, a.last_probe_at, a.created_at, a.updated_at,
		       (SELECT COUNT(*) FROM conversations c WHERE c.agent_id=a.id) AS cnt
		FROM agents a ORDER BY a.created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := s.scanAgent(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAgent(ctx context.Context, id string) (Agent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT a.id, a.name, a.slug, a.description, a.command, a.args_json, a.env_enc, a.cwd,
		       a.icon, a.color, a.enabled, a.auto_start, a.approval_policy, a.default_model,
		       a.models_json, a.config_options_json, a.capabilities_json, a.auth_methods_json,
		       a.status, a.last_error, a.last_probe_at, a.created_at, a.updated_at, 0
		FROM agents a WHERE a.id=?`, id)
	return s.scanAgent(row, true)
}

func (s *Store) GetAgentBySlug(ctx context.Context, slug string) (Agent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT a.id, a.name, a.slug, a.description, a.command, a.args_json, a.env_enc, a.cwd,
		       a.icon, a.color, a.enabled, a.auto_start, a.approval_policy, a.default_model,
		       a.models_json, a.config_options_json, a.capabilities_json, a.auth_methods_json,
		       a.status, a.last_error, a.last_probe_at, a.created_at, a.updated_at, 0
		FROM agents a WHERE a.slug=?`, slug)
	return s.scanAgent(row, true)
}

func (s *Store) FirstEnabledAgent(ctx context.Context) (Agent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT a.id, a.name, a.slug, a.description, a.command, a.args_json, a.env_enc, a.cwd,
		       a.icon, a.color, a.enabled, a.auto_start, a.approval_policy, a.default_model,
		       a.models_json, a.config_options_json, a.capabilities_json, a.auth_methods_json,
		       a.status, a.last_error, a.last_probe_at, a.created_at, a.updated_at, 0
		FROM agents a WHERE a.enabled=1 ORDER BY a.created_at ASC LIMIT 1`)
	return s.scanAgent(row, true)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanAgent(row rowScanner, decrypt bool) (Agent, error) {
	var a Agent
	var argsJSON, envEnc, models, configs, caps, auths string
	var enabled, autoStart int
	err := row.Scan(&a.ID, &a.Name, &a.Slug, &a.Description, &a.Command, &argsJSON, &envEnc, &a.Cwd,
		&a.Icon, &a.Color, &enabled, &autoStart, &a.ApprovalPolicy, &a.DefaultModel,
		&models, &configs, &caps, &auths, &a.Status, &a.LastError, &a.LastProbeAt,
		&a.CreatedAt, &a.UpdatedAt, &a.ConversationCnt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return a, ErrNotFound
		}
		return a, err
	}
	a.Enabled = enabled != 0
	a.AutoStart = autoStart != 0
	_ = json.Unmarshal([]byte(argsJSON), &a.Args)
	a.Models = raw(models, "[]")
	a.ConfigOptions = raw(configs, "[]")
	a.Capabilities = raw(caps, "{}")
	a.AuthMethods = raw(auths, "[]")
	if decrypt {
		a.Env = s.openSecrets(envEnc)
	} else {
		a.Env = publicSecrets(s.openSecrets(envEnc))
	}
	return a, nil
}

func (s *Store) openSecrets(encoded string) SecretList {
	if encoded == "" {
		return nil
	}
	var list SecretList
	if s.sealer != nil {
		if b, err := s.sealer.Open(encoded); err == nil {
			_ = json.Unmarshal(b, &list)
			return list
		}
	}
	// Legacy plaintext fallback.
	_ = json.Unmarshal([]byte(encoded), &list)
	return list
}

func (s *Store) sealSecrets(list SecretList) (string, error) {
	for i := range list {
		list[i].Name = strings.TrimSpace(list[i].Name)
		list[i].HasValue = list[i].Value != ""
	}
	if len(list) == 0 {
		return "", nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	if s.sealer == nil {
		return string(b), nil
	}
	return s.sealer.Seal(b)
}

func publicSecrets(in SecretList) SecretList {
	out := make(SecretList, 0, len(in))
	for _, v := range in {
		out = append(out, EnvVar{Name: v.Name, HasValue: v.Value != "" || v.HasValue})
	}
	return out
}

func mergeSecrets(in SecretList, existing SecretList) SecretList {
	old := map[string]string{}
	for _, e := range existing {
		old[e.Name] = e.Value
	}
	out := make(SecretList, 0, len(in))
	seen := map[string]bool{}
	for _, e := range in {
		e.Name = strings.TrimSpace(e.Name)
		if e.Name == "" || seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		if e.Value == "" && e.HasValue {
			e.Value = old[e.Name]
		}
		out = append(out, e)
	}
	return out
}

func (s *Store) CreateAgent(ctx context.Context, in AgentInput) (Agent, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Agent{}, errors.New("agent name is required")
	}
	slug := NormalizeSlug(in.Slug)
	if slug == "" || !ValidSlug(slug) {
		return Agent{}, errors.New("invalid agent slug")
	}
	if strings.TrimSpace(in.Command) == "" {
		return Agent{}, errors.New("command is required")
	}
	if in.Cwd == "" {
		return Agent{}, errors.New("working directory is required")
	}
	policy := in.ApprovalPolicy
	if policy == "" {
		policy = "ask"
	}
	if policy != "ask" && policy != "auto_safe" && policy != "auto_all" {
		return Agent{}, errors.New("invalid approval policy")
	}
	enabled, autoStart := true, false
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if in.AutoStart != nil {
		autoStart = *in.AutoStart
	}
	envEnc, err := s.sealSecrets(mergeSecrets(in.Env, nil))
	if err != nil {
		return Agent{}, err
	}
	now := nowMS()
	id := ids.New("ag")
	icon := in.Icon
	if icon == "" {
		icon = "sparkles"
	}
	color := in.Color
	if color == "" {
		color = "amber"
	}
	args := in.Args
	if args == nil {
		args = []string{}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agents
		(id,name,slug,description,command,args_json,env_enc,cwd,icon,color,enabled,auto_start,
		 approval_policy,default_model,models_json,config_options_json,capabilities_json,
		 auth_methods_json,status,last_error,last_probe_at,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,'[]','[]','{}','[]','unknown','',0,?,?)`,
		id, name, slug, strings.TrimSpace(in.Description), strings.TrimSpace(in.Command), js(args, "[]"),
		envEnc, in.Cwd, icon, color, boolInt(enabled), boolInt(autoStart), policy,
		strings.TrimSpace(in.DefaultModel), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Agent{}, fmt.Errorf("agent slug %q is already in use", slug)
		}
		return Agent{}, err
	}
	return s.GetAgent(ctx, id)
}

func (s *Store) UpdateAgent(ctx context.Context, id string, in AgentInput) (Agent, error) {
	cur, err := s.GetAgent(ctx, id)
	if err != nil {
		return Agent{}, err
	}
	if strings.TrimSpace(in.Name) != "" {
		cur.Name = strings.TrimSpace(in.Name)
	}
	if in.Slug != "" {
		cur.Slug = NormalizeSlug(in.Slug)
	}
	if !ValidSlug(cur.Slug) {
		return Agent{}, errors.New("invalid agent slug")
	}
	if in.Command != "" {
		cur.Command = strings.TrimSpace(in.Command)
	}
	if in.Args != nil {
		cur.Args = in.Args
	}
	if in.Cwd != "" {
		cur.Cwd = in.Cwd
	}
	if in.Description != "" {
		cur.Description = strings.TrimSpace(in.Description)
	}
	if in.Icon != "" {
		cur.Icon = in.Icon
	}
	if in.Color != "" {
		cur.Color = in.Color
	}
	if in.Enabled != nil {
		cur.Enabled = *in.Enabled
	}
	if in.AutoStart != nil {
		cur.AutoStart = *in.AutoStart
	}
	if in.ApprovalPolicy != "" {
		if in.ApprovalPolicy != "ask" && in.ApprovalPolicy != "auto_safe" && in.ApprovalPolicy != "auto_all" {
			return Agent{}, errors.New("invalid approval policy")
		}
		cur.ApprovalPolicy = in.ApprovalPolicy
	}
	if in.DefaultModel != "" || in.DefaultModelSet {
		cur.DefaultModel = strings.TrimSpace(in.DefaultModel)
	}
	if in.Env != nil {
		cur.Env = mergeSecrets(in.Env, cur.Env)
		if len(in.Env) == 0 {
			cur.Env = nil
		}
	}
	envEnc, err := s.sealSecrets(cur.Env)
	if err != nil {
		return Agent{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE agents SET
		name=?,slug=?,description=?,command=?,args_json=?,env_enc=?,cwd=?,icon=?,color=?,
		enabled=?,auto_start=?,approval_policy=?,default_model=?,updated_at=? WHERE id=?`,
		cur.Name, cur.Slug, cur.Description, cur.Command, js(cur.Args, "[]"), envEnc, cur.Cwd,
		cur.Icon, cur.Color, boolInt(cur.Enabled), boolInt(cur.AutoStart), cur.ApprovalPolicy,
		cur.DefaultModel, nowMS(), id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Agent{}, fmt.Errorf("agent slug %q is already in use", cur.Slug)
		}
		return Agent{}, err
	}
	return s.GetAgent(ctx, id)
}

func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE agent_id=?`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("agent has %d conversation(s); archive or delete them first", count)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateAgentStatus(ctx context.Context, id, status, lastErr string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET status=?, last_error=?, updated_at=? WHERE id=?`,
		status, lastErr, nowMS(), id)
	return err
}

func (s *Store) UpdateAgentProbe(ctx context.Context, id, status, lastErr string, models, configs, caps, auths json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET status=?, last_error=?, models_json=?, config_options_json=?,
		capabilities_json=?, auth_methods_json=?, last_probe_at=?, updated_at=? WHERE id=?`,
		status, lastErr, string(rawBytes(models, "[]")), string(rawBytes(configs, "[]")),
		string(rawBytes(caps, "{}")), string(rawBytes(auths, "[]")), nowMS(), nowMS(), id)
	return err
}

func (s *Store) UpdateAgentConfigCache(ctx context.Context, id string, configs json.RawMessage) error {
	if len(configs) == 0 {
		return nil
	}
	// Keep the model list in sync with the latest session config options.
	models := ExtractModels(configs)
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET config_options_json=?, models_json=?, updated_at=? WHERE id=?`,
		string(configs), string(models), nowMS(), id)
	return err
}

func rawBytes(v json.RawMessage, fallback string) json.RawMessage {
	if len(v) == 0 || string(v) == "null" {
		return json.RawMessage(fallback)
	}
	return v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ExtractModels flattens the `model` category from ACP session config options.
func ExtractModels(configs json.RawMessage) json.RawMessage {
	var opts []struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Category string          `json:"category"`
		Type     string          `json:"type"`
		Options  json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(configs, &opts); err != nil {
		return json.RawMessage("[]")
	}
	for _, o := range opts {
		if o.Category != "model" {
			continue
		}
		var flat []struct {
			Value       string `json:"value"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
		}
		var choices []struct {
			Value       string `json:"value"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
		}
		if err := json.Unmarshal(o.Options, &flat); err == nil {
			// If this is a grouped array, flat entries won't have a value.
			for _, g := range flat {
				if g.Value != "" {
					choices = append(choices, g)
				}
			}
			if len(choices) == 0 {
				var groups []struct {
					Group   string `json:"group"`
					Name    string `json:"name"`
					Options []struct {
						Value       string `json:"value"`
						Name        string `json:"name"`
						Description string `json:"description,omitempty"`
					} `json:"options"`
				}
				_ = json.Unmarshal(o.Options, &groups)
				for _, g := range groups {
					for _, c := range g.Options {
						choices = append(choices, c)
					}
				}
			}
		}
		return rawBytes(mustJSON(choices), "[]")
	}
	return json.RawMessage("[]")
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (s *Store) ListMCP(ctx context.Context, agentID string) ([]MCPServer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,agent_id,name,transport,command,args_json,env_enc,url,headers_enc,enabled,created_at,updated_at
		FROM mcp_servers WHERE agent_id=? ORDER BY created_at ASC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPServer
	for rows.Next() {
		m, err := s.scanMCP(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ListAllMCP(ctx context.Context) (map[string][]MCPServer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,agent_id,name,transport,command,args_json,env_enc,url,headers_enc,enabled,created_at,updated_at
		FROM mcp_servers ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]MCPServer{}
	for rows.Next() {
		m, err := s.scanMCP(rows)
		if err != nil {
			return nil, err
		}
		out[m.AgentID] = append(out[m.AgentID], m)
	}
	return out, rows.Err()
}

func (s *Store) GetMCP(ctx context.Context, id string) (MCPServer, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,agent_id,name,transport,command,args_json,env_enc,url,headers_enc,enabled,created_at,updated_at
		FROM mcp_servers WHERE id=?`, id)
	return s.scanMCP(row)
}

func (s *Store) scanMCP(row rowScanner) (MCPServer, error) {
	var m MCPServer
	var argsJSON, envEnc, headersEnc string
	var enabled int
	err := row.Scan(&m.ID, &m.AgentID, &m.Name, &m.Transport, &m.Command, &argsJSON, &envEnc, &m.URL, &headersEnc, &enabled, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return m, ErrNotFound
		}
		return m, err
	}
	m.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(argsJSON), &m.Args)
	m.Env = s.openSecrets(envEnc)
	m.Headers = s.openSecrets(headersEnc)
	return m, nil
}

func (s *Store) CreateMCP(ctx context.Context, in MCPServer) (MCPServer, error) {
	if strings.TrimSpace(in.Name) == "" {
		return MCPServer{}, errors.New("MCP server name is required")
	}
	if in.Transport == "" {
		in.Transport = "stdio"
	}
	if in.Transport != "stdio" && in.Transport != "http" && in.Transport != "sse" {
		return MCPServer{}, errors.New("invalid MCP transport")
	}
	if in.Transport == "stdio" && strings.TrimSpace(in.Command) == "" {
		return MCPServer{}, errors.New("MCP stdio server requires a command")
	}
	if (in.Transport == "http" || in.Transport == "sse") && strings.TrimSpace(in.URL) == "" {
		return MCPServer{}, errors.New("MCP HTTP/SSE server requires a URL")
	}
	envEnc, err := s.sealSecrets(mergeSecrets(in.Env, nil))
	if err != nil {
		return MCPServer{}, err
	}
	headersEnc, err := s.sealSecrets(mergeSecrets(in.Headers, nil))
	if err != nil {
		return MCPServer{}, err
	}
	now := nowMS()
	id := ids.New("mcp")
	_, err = s.db.ExecContext(ctx, `INSERT INTO mcp_servers
		(id,agent_id,name,transport,command,args_json,env_enc,url,headers_enc,enabled,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, in.AgentID, strings.TrimSpace(in.Name), in.Transport, in.Command, js(in.Args, "[]"),
		envEnc, in.URL, headersEnc, boolInt(in.Enabled), now, now)
	if err != nil {
		return MCPServer{}, err
	}
	return s.GetMCP(ctx, id)
}

func (s *Store) UpdateMCP(ctx context.Context, id string, in MCPServer) (MCPServer, error) {
	cur, err := s.GetMCP(ctx, id)
	if err != nil {
		return MCPServer{}, err
	}
	if in.Name != "" {
		cur.Name = strings.TrimSpace(in.Name)
	}
	if in.Transport != "" {
		if in.Transport != "stdio" && in.Transport != "http" && in.Transport != "sse" {
			return MCPServer{}, errors.New("invalid MCP transport")
		}
		cur.Transport = in.Transport
	}
	if in.Command != "" {
		cur.Command = in.Command
	}
	if in.Args != nil {
		cur.Args = in.Args
	}
	if in.URL != "" {
		cur.URL = in.URL
	}
	if in.Enabled != cur.Enabled || in.EnabledSet {
		cur.Enabled = in.Enabled
	}
	if in.Env != nil {
		cur.Env = mergeSecrets(in.Env, cur.Env)
		if len(in.Env) == 0 {
			cur.Env = nil
		}
	}
	if in.Headers != nil {
		cur.Headers = mergeSecrets(in.Headers, cur.Headers)
		if len(in.Headers) == 0 {
			cur.Headers = nil
		}
	}
	envEnc, err := s.sealSecrets(cur.Env)
	if err != nil {
		return MCPServer{}, err
	}
	headersEnc, err := s.sealSecrets(cur.Headers)
	if err != nil {
		return MCPServer{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE mcp_servers SET name=?,transport=?,command=?,args_json=?,env_enc=?,url=?,headers_enc=?,enabled=?,updated_at=? WHERE id=?`,
		cur.Name, cur.Transport, cur.Command, js(cur.Args, "[]"), envEnc, cur.URL, headersEnc, boolInt(cur.Enabled), nowMS(), id)
	if err != nil {
		return MCPServer{}, err
	}
	return s.GetMCP(ctx, id)
}

func (s *Store) DeleteMCP(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
