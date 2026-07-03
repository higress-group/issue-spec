package commentrunner

type Config struct {
	BackendMode     string   `json:"backend_mode"`
	Repositories    []string `json:"repositories"`
	StatePath       string   `json:"state_path,omitempty"`
	WorkspaceRoot   string   `json:"workspace_root,omitempty"`
	BwrapPath       string   `json:"bwrap_path,omitempty"`
	UnsafeNoSandbox bool     `json:"unsafe_no_sandbox,omitempty"`
	AcpxPath        string   `json:"acpx_path,omitempty"`
	Agent           string   `json:"agent,omitempty"`
	Model           string   `json:"model,omitempty"`
	Concurrency     int      `json:"concurrency,omitempty"`
	CancelEnabled   bool     `json:"cancel_enabled,omitempty"`
}

type PreflightCategory string

const (
	PreflightGitHubAuth      PreflightCategory = "github-auth"
	PreflightRepositoryWatch PreflightCategory = "repository-watch"
	PreflightBwrap           PreflightCategory = "bwrap"
	PreflightUnsafeMode      PreflightCategory = "unsafe-mode"
	PreflightTempGHConfigDir PreflightCategory = "temp-gh-config-dir"
	PreflightAcpx            PreflightCategory = "acpx"
	PreflightCodex           PreflightCategory = "codex"
	PreflightClaude          PreflightCategory = "claude"
)

type PreflightResult struct {
	Category PreflightCategory `json:"category"`
	Provider string            `json:"provider"`
	OK       bool              `json:"ok"`
	Message  string            `json:"message"`
}

type PreflightProvider interface {
	Category() PreflightCategory
	Check(Config) PreflightResult
}

type PreflightAggregator interface {
	Collect(Config) []PreflightResult
}

type ProviderSet []PreflightProvider

func (p ProviderSet) Collect(cfg Config) []PreflightResult {
	results := make([]PreflightResult, 0, len(p))
	for _, provider := range p {
		results = append(results, provider.Check(cfg))
	}
	return results
}
