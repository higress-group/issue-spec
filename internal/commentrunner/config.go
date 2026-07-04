package commentrunner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
)

const (
	AgentCodex  = "codex"
	AgentClaude = "claude"

	DefaultNotificationTokenEnv = "ISSUE_SPEC_NOTIFICATION_TOKEN"
)

type Duration struct {
	time.Duration
}

type Config struct {
	Hostname             string                 `json:"hostname"`
	Repositories         []string               `json:"repositories"`
	RunnerIdentity       string                 `json:"runner_identity"`
	NotificationIdentity string                 `json:"notification_identity,omitempty"`
	NotificationTokenEnv string                 `json:"notification_token_env,omitempty"`
	AllowedUsers         []string               `json:"allowed_users"`
	GitHubBackend        auth.GitHubBackendMode `json:"github_backend"`
	StatePath            string                 `json:"state_path"`
	PollInterval         Duration               `json:"poll_interval"`
	FallbackInterval     Duration               `json:"fallback_interval"`
	MaxConcurrentJobs    int                    `json:"max_concurrent_jobs"`
	AcpxPath             string                 `json:"acpx_path"`
	Agent                AgentConfig            `json:"agent"`
	WorkspaceRoot        string                 `json:"workspace_root"`
	WorkspaceRetention   Duration               `json:"workspace_retention"`
	BwrapPath            string                 `json:"bwrap_path,omitempty"`
	UnsafeNoSandbox      bool                   `json:"unsafe_no_sandbox"`
	GHConfigDir          string                 `json:"gh_config_dir,omitempty"`
	CancellationEnabled  bool                   `json:"cancellation_enabled"`
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
		MaxConcurrentJobs:   3,
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

func ApplyDefaultRunnerScopePaths(cfg Config, statePathExplicit, workspaceRootExplicit bool) (Config, error) {
	cfg = cfg.Normalized()
	if statePathExplicit && workspaceRootExplicit {
		return cfg, nil
	}
	statePath, workspaceRoot, err := DefaultRunnerScopePaths(cfg)
	if err != nil {
		return Config{}, err
	}
	if !statePathExplicit {
		cfg.StatePath = statePath
	}
	if !workspaceRootExplicit {
		cfg.WorkspaceRoot = workspaceRoot
	}
	return cfg, nil
}

func DefaultRunnerScopePaths(cfg Config) (string, string, error) {
	cfg = cfg.Normalized()
	segments, err := runnerScopeSegments(cfg)
	if err != nil {
		return "", "", err
	}
	stateBase, workspaceBase := defaultRunnerScopeBaseDirs()
	stateRoot := filepath.Join(append([]string{stateBase, "runners"}, segments...)...)
	workspaceRoot := filepath.Join(append([]string{workspaceBase, "runners"}, segments...)...)
	return filepath.Join(stateRoot, "state.json"), filepath.Join(workspaceRoot, "workspaces"), nil
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
	c.NotificationIdentity = strings.TrimSpace(c.NotificationIdentity)
	c.NotificationTokenEnv = strings.TrimSpace(c.NotificationTokenEnv)
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
	c.AllowedUsers = normalizeLoginList(c.AllowedUsers)
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
	if c.NotificationIdentity != "" && c.NotificationTokenEnv == "" {
		return fmt.Errorf("--notification-token-env is required when --notification-runner is set")
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

func runnerScopeSegments(cfg Config) ([]string, error) {
	host := safePathSegment(strings.ToLower(auth.NormalizeHost(cfg.Hostname)))
	runner := safePathSegment(strings.ToLower(strings.TrimSpace(cfg.RunnerIdentity)))
	if strings.TrimSpace(cfg.RunnerIdentity) == "" {
		return nil, fmt.Errorf("--runner is required")
	}
	repos := normalizeStringList(cfg.Repositories)
	if len(repos) == 0 {
		return nil, fmt.Errorf("at least one --repo is required")
	}
	canonicalRepos, err := canonicalReposForScope(repos)
	if err != nil {
		return nil, err
	}
	if len(canonicalRepos) == 1 {
		parts := strings.Split(canonicalRepos[0], "/")
		return []string{host, safePathSegment(parts[0]), safePathSegment(parts[1]), runner}, nil
	}
	return []string{host, "multi", multiRepoScopeSegment(canonicalRepos), runner}, nil
}

func canonicalReposForScope(repos []string) ([]string, error) {
	out := make([]string, 0, len(repos))
	seen := map[string]bool{}
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if _, err := github.ParseRepo(repo); err != nil {
			return nil, err
		}
		parts := strings.Split(repo, "/")
		canonical := strings.ToLower(strings.TrimSpace(parts[0])) + "/" + strings.ToLower(strings.TrimSpace(parts[1]))
		if canonical == "/" || seen[canonical] {
			continue
		}
		out = append(out, canonical)
		seen[canonical] = true
	}
	sort.Strings(out)
	return out, nil
}

func multiRepoScopeSegment(canonicalRepos []string) string {
	var labels []string
	for _, repo := range canonicalRepos {
		parts := strings.Split(repo, "/")
		labels = append(labels, safePathSegment(parts[0]+"-"+parts[1]))
	}
	label := strings.Join(labels, "+")
	if label == "" {
		label = "repos"
	}
	const maxLabelLength = 48
	if len(label) > maxLabelLength {
		label = strings.TrimRight(label[:maxLabelLength], "-_.+")
	}
	if label == "" {
		label = "repos"
	}
	sum := sha256.Sum256([]byte(strings.Join(canonicalRepos, "\n")))
	return label + "-" + hex.EncodeToString(sum[:])[:12]
}

func safePathSegment(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	lastReplacement := false
	for _, r := range value {
		if isSafePathSegmentRune(r) {
			b.WriteRune(r)
			lastReplacement = false
			continue
		}
		if !lastReplacement {
			b.WriteByte('-')
			lastReplacement = true
		}
	}
	segment := strings.Trim(b.String(), "-.")
	if segment == "" || segment == "." || segment == ".." {
		return "default"
	}
	return segment
}

func isSafePathSegmentRune(r rune) bool {
	return r >= 'a' && r <= 'z' ||
		r >= 'A' && r <= 'Z' ||
		r >= '0' && r <= '9' ||
		r == '-' || r == '_' || r == '.'
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

func normalizeLoginList(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			item := strings.TrimSpace(part)
			key := strings.ToLower(item)
			if item == "" || seen[key] {
				continue
			}
			out = append(out, item)
			seen[key] = true
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

func defaultRunnerScopeBaseDirs() (string, string) {
	if dir, ok := defaultIssueSpecDir(); ok {
		return dir, dir
	}
	return legacyDefaultStateDir(), legacyDefaultWorkspaceDir()
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
	return filepath.Join(legacyDefaultStateDir(), "runner-state.json")
}

func legacyDefaultStateDir() string {
	if dir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "issue-spec")
	}
	return ".issue-spec"
}

func legacyDefaultWorkspaceRoot() string {
	return filepath.Join(legacyDefaultWorkspaceDir(), "runner-workspaces")
}

func legacyDefaultWorkspaceDir() string {
	if dir, err := os.UserCacheDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "issue-spec")
	}
	return ".issue-spec"
}
