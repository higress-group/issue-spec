package commentrunner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
)

const (
	AgentCodex  = "codex"
	AgentClaude = "claude"
)

type Duration struct {
	time.Duration
}

type Config struct {
	Hostname            string                 `json:"hostname"`
	Repositories        []string               `json:"repositories"`
	RunnerIdentity      string                 `json:"runner_identity"`
	GitHubBackend       auth.GitHubBackendMode `json:"github_backend"`
	StatePath           string                 `json:"state_path"`
	PollInterval        Duration               `json:"poll_interval"`
	FallbackInterval    Duration               `json:"fallback_interval"`
	MaxConcurrentJobs   int                    `json:"max_concurrent_jobs"`
	AcpxPath            string                 `json:"acpx_path"`
	Agent               AgentConfig            `json:"agent"`
	WorkspaceRoot       string                 `json:"workspace_root"`
	WorkspaceRetention  Duration               `json:"workspace_retention"`
	BwrapPath           string                 `json:"bwrap_path,omitempty"`
	UnsafeNoSandbox     bool                   `json:"unsafe_no_sandbox"`
	GHConfigDir         string                 `json:"gh_config_dir,omitempty"`
	CancellationEnabled bool                   `json:"cancellation_enabled"`
}

type AgentConfig struct {
	Kind                      string   `json:"kind"`
	Model                     string   `json:"model,omitempty"`
	CodexAgentFullAccess      bool     `json:"codex_agent_full_access"`
	ClaudeIncludeUserSettings bool     `json:"claude_include_user_settings"`
	ClaudeAllowedTools        []string `json:"claude_allowed_tools,omitempty"`
}

func NewDuration(value time.Duration) Duration {
	return Duration{Duration: value}
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) || len(data) == 0 {
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("parse duration %q: %w", value, err)
		}
		d.Duration = parsed
		return nil
	}
	var nanos int64
	if err := json.Unmarshal(data, &nanos); err == nil {
		d.Duration = time.Duration(nanos)
		return nil
	}
	return fmt.Errorf("duration must be a Go duration string or nanoseconds")
}

func DefaultConfigFromEnv() (Config, error) {
	mode, err := auth.GitHubBackendModeFromEnv()
	if err != nil {
		return Config{}, err
	}
	return Config{
		Hostname:            "github.com",
		GitHubBackend:       mode,
		StatePath:           defaultStatePath(),
		PollInterval:        NewDuration(time.Minute),
		FallbackInterval:    NewDuration(5 * time.Minute),
		MaxConcurrentJobs:   1,
		AcpxPath:            "acpx",
		Agent:               DefaultAgentConfig(),
		WorkspaceRoot:       defaultWorkspaceRoot(),
		WorkspaceRetention:  NewDuration(7 * 24 * time.Hour),
		CancellationEnabled: true,
	}, nil
}

func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		Kind:                      AgentCodex,
		CodexAgentFullAccess:      true,
		ClaudeIncludeUserSettings: true,
		ClaudeAllowedTools:        []string{"Task", "Bash"},
	}
}

func (c Config) Normalized() Config {
	c.Hostname = auth.NormalizeHost(c.Hostname)
	if c.Hostname == "" {
		c.Hostname = "github.com"
	}
	if c.GitHubBackend == "" {
		c.GitHubBackend = auth.GitHubBackendModeAuto
	}
	c.RunnerIdentity = strings.TrimSpace(c.RunnerIdentity)
	c.StatePath = strings.TrimSpace(c.StatePath)
	c.WorkspaceRoot = strings.TrimSpace(c.WorkspaceRoot)
	c.AcpxPath = strings.TrimSpace(c.AcpxPath)
	if c.AcpxPath == "" {
		c.AcpxPath = "acpx"
	}
	c.BwrapPath = strings.TrimSpace(c.BwrapPath)
	c.GHConfigDir = strings.TrimSpace(c.GHConfigDir)
	c.Agent.Kind = strings.ToLower(strings.TrimSpace(c.Agent.Kind))
	if c.Agent.Kind == "" {
		c.Agent.Kind = AgentCodex
	}
	c.Agent.Model = strings.TrimSpace(c.Agent.Model)
	c.Agent.ClaudeAllowedTools = normalizeStringList(c.Agent.ClaudeAllowedTools)
	c.Repositories = normalizeStringList(c.Repositories)
	return c
}

func (c Config) Validate() error {
	c = c.Normalized()
	if _, err := auth.ParseGitHubBackendMode(string(c.GitHubBackend)); err != nil {
		return err
	}
	if len(c.Repositories) == 0 {
		return fmt.Errorf("at least one --repo is required")
	}
	for _, repo := range c.Repositories {
		if _, err := github.ParseRepo(repo); err != nil {
			return err
		}
	}
	if c.RunnerIdentity == "" {
		return fmt.Errorf("--runner is required")
	}
	if c.StatePath == "" {
		return fmt.Errorf("--state is required")
	}
	if c.WorkspaceRoot == "" {
		return fmt.Errorf("--workspace-root is required")
	}
	if c.PollInterval.Duration <= 0 {
		return fmt.Errorf("--poll-interval must be positive")
	}
	if c.FallbackInterval.Duration <= 0 {
		return fmt.Errorf("--fallback-interval must be positive")
	}
	if c.WorkspaceRetention.Duration <= 0 {
		return fmt.Errorf("--workspace-retention must be positive")
	}
	if c.MaxConcurrentJobs <= 0 {
		return fmt.Errorf("--max-concurrency must be positive")
	}
	switch c.Agent.Kind {
	case AgentCodex, AgentClaude:
	default:
		return fmt.Errorf("invalid --agent %q; valid values: codex, claude", c.Agent.Kind)
	}
	return nil
}

func normalizeStringList(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			item := strings.TrimSpace(part)
			if item == "" || seen[item] {
				continue
			}
			out = append(out, item)
			seen[item] = true
		}
	}
	return out
}

func defaultStatePath() string {
	if dir, ok := defaultIssueSpecDir(); ok {
		return filepath.Join(dir, "runner-state.json")
	}
	return legacyDefaultStatePath()
}

func defaultWorkspaceRoot() string {
	if dir, ok := defaultIssueSpecDir(); ok {
		return filepath.Join(dir, "workspaces")
	}
	return legacyDefaultWorkspaceRoot()
}

func defaultIssueSpecDir() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return "", false
	}
	home = filepath.Clean(home)
	if !filepath.IsAbs(home) || isFilesystemRoot(home) {
		return "", false
	}
	return filepath.Join(home, ".issue-spec"), true
}

func isFilesystemRoot(path string) bool {
	clean := filepath.Clean(path)
	return clean == filepath.Dir(clean)
}

func legacyDefaultStatePath() string {
	if dir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "issue-spec", "runner-state.json")
	}
	return filepath.Join(".issue-spec", "runner-state.json")
}

func legacyDefaultWorkspaceRoot() string {
	if dir, err := os.UserCacheDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "issue-spec", "runner-workspaces")
	}
	return filepath.Join(".issue-spec", "runner-workspaces")
}
