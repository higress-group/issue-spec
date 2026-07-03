package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	ProviderBubblewrap = "bubblewrap"
	ProviderNone       = "none"

	FSBoundaryWorkspace = "workspace"
	FSBoundaryDisabled  = "disabled"

	BwrapPathEnv = "ISSUE_SPEC_BWRAP_PATH"

	defaultMinBwrapVersion = "0.5.0"
)

var (
	ErrSandboxUnsupported     = errors.New("sandbox unsupported on this platform")
	ErrBubblewrapUnavailable  = errors.New("bubblewrap unavailable")
	ErrBubblewrapUnsupported  = errors.New("bubblewrap unsupported")
	ErrSandboxConfigInvalid   = errors.New("sandbox config invalid")
	ErrSandboxPreflightFailed = errors.New("sandbox preflight failed")
)

var defaultEnvAllowlist = []string{"PATH", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO", "CURL_CA_BUNDLE"}
var proxyEnvNames = []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY", "no_proxy", "NO_PROXY"}

type Config struct {
	UnsafeNoSandbox bool

	BwrapPath       string
	MinBwrapVersion string

	WorkspacePath     string
	TempHome          string
	TempGHConfigDir   string
	TempXDGConfigHome string
	TempCodexHome     string
	HostGHConfigDir   string

	HostEnv             []string
	EnvAllowlist        []string
	ExtraEnv            map[string]string
	DisableProxyEnv     bool
	SystemReadOnlyBinds []string
	ReadOnlyBinds       []string
}

type Command struct {
	Binary string
	Args   []string
	Env    []string
	Dir    string
	Stdin  []byte
}

type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type Runner interface {
	Run(context.Context, Command) (Result, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command Command) (Result, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, command.Binary, command.Args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Dir = command.Dir
	if command.Env != nil {
		cmd.Env = append([]string(nil), command.Env...)
	}
	if command.Stdin != nil {
		cmd.Stdin = bytes.NewReader(command.Stdin)
	}
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCode}, err
}

type Dependencies struct {
	LookPath func(string) (string, error)
	Runner   Runner
}

func (d Dependencies) withDefaults() Dependencies {
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.Runner == nil {
		d.Runner = ExecRunner{}
	}
	return d
}

type PreparedCommand struct {
	Command  Command
	Metadata Metadata
}

type Metadata struct {
	SandboxEnabled  bool
	UnsafeNoSandbox bool
	SandboxProvider string
	FSBoundary      string

	Platform          string
	PlatformSupported bool

	BwrapPath           string
	BwrapPathSource     string
	BwrapVersion        string
	BwrapPermsSupported bool
	BwrapSmokeTest      bool

	Env         EnvMetadata
	Mounts      []Mount
	Diagnostics []string
}

type EnvMetadata struct {
	ProxyInherited []string
	TokenUnset     []string
	Set            []string

	Home          string
	GHConfigDir   string
	XDGConfigHome string
	CodexHome     string
}

type Mount struct {
	Source      string
	Destination string
	Mode        string
}

func Preflight(ctx context.Context, cfg Config, deps Dependencies) (Metadata, error) {
	envMeta := scrubEnvironment(cfg, envPaths{}, false).metadata
	if cfg.UnsafeNoSandbox {
		return unsafeMetadata(cfg, envMeta), nil
	}
	return preflightBwrap(ctx, cfg, envMeta, deps)
}

func Prepare(ctx context.Context, cfg Config, target Command, deps Dependencies) (PreparedCommand, error) {
	if strings.TrimSpace(target.Binary) == "" {
		return PreparedCommand{}, fmt.Errorf("%w: target binary is required", ErrSandboxConfigInvalid)
	}
	if cfg.UnsafeNoSandbox {
		env := scrubEnvironment(cfg, hostEnvPaths(cfg), true)
		meta := unsafeMetadata(cfg, env.metadata)
		if env.err != nil {
			return PreparedCommand{Metadata: meta}, env.err
		}
		target.Env = mergeCommandEnv(env.entries, target.Env, cfg, &meta.Env)
		if target.Dir == "" {
			target.Dir = cfg.WorkspacePath
		}
		return PreparedCommand{Command: target, Metadata: meta}, nil
	}

	meta, err := Preflight(ctx, cfg, deps)
	if err != nil {
		return PreparedCommand{Metadata: meta}, err
	}
	env := scrubEnvironment(cfg, sandboxEnvPaths(), true)
	meta.Env = env.metadata
	if env.err != nil {
		return PreparedCommand{Metadata: meta}, env.err
	}
	commandEnv := mergeCommandEnv(env.entries, target.Env, cfg, &meta.Env)
	command, mounts, err := buildBwrapCommand(cfg, target, commandEnv, meta.BwrapPath)
	meta.Mounts = mounts
	if err != nil {
		return PreparedCommand{Metadata: meta}, err
	}
	return PreparedCommand{Command: command, Metadata: meta}, nil
}

type envPaths struct {
	home          string
	ghConfigDir   string
	xdgConfigHome string
	codexHome     string
}

func hostEnvPaths(cfg Config) envPaths {
	return envPaths{home: cfg.TempHome, ghConfigDir: cfg.TempGHConfigDir, xdgConfigHome: cfg.TempXDGConfigHome, codexHome: cfg.TempCodexHome}
}

func sandboxEnvPaths() envPaths {
	return envPaths{home: "/tmp/issue-spec-home", ghConfigDir: "/tmp/issue-spec-gh", xdgConfigHome: "/tmp/issue-spec-xdg", codexHome: "/tmp/issue-spec-codex"}
}

type envBuildResult struct {
	entries  []string
	metadata EnvMetadata
	err      error
}

func scrubEnvironment(cfg Config, paths envPaths, requireTempPaths bool) envBuildResult {
	if requireTempPaths && (strings.TrimSpace(paths.home) == "" || strings.TrimSpace(paths.ghConfigDir) == "" || strings.TrimSpace(paths.xdgConfigHome) == "") {
		return envBuildResult{err: fmt.Errorf("%w: temporary HOME, GH_CONFIG_DIR, and XDG_CONFIG_HOME paths are required", ErrSandboxConfigInvalid)}
	}

	hostEnv := cfg.HostEnv
	if hostEnv == nil {
		hostEnv = os.Environ()
	}
	allowlist := cfg.EnvAllowlist
	if len(allowlist) == 0 {
		allowlist = defaultEnvAllowlist
	}
	allowed := stringSet(allowlist)
	proxies := stringSet(proxyEnvNames)
	codexHome := ""
	if strings.TrimSpace(cfg.TempCodexHome) != "" {
		codexHome = paths.codexHome
	}

	values := map[string]string{}
	meta := EnvMetadata{
		Home:          paths.home,
		GHConfigDir:   paths.ghConfigDir,
		XDGConfigHome: paths.xdgConfigHome,
		CodexHome:     codexHome,
	}
	for _, entry := range hostEnv {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		if isTokenEnv(name) {
			meta.TokenUnset = append(meta.TokenUnset, name)
			continue
		}
		if proxies[name] {
			if !cfg.DisableProxyEnv {
				values[name] = value
				meta.ProxyInherited = append(meta.ProxyInherited, name)
			}
			continue
		}
		if allowed[name] {
			values[name] = value
		}
	}
	for name, value := range cfg.ExtraEnv {
		if name == "" {
			continue
		}
		if isTokenEnv(name) {
			meta.TokenUnset = append(meta.TokenUnset, name)
			continue
		}
		values[name] = value
	}
	if paths.home != "" {
		values["HOME"] = paths.home
	}
	if paths.ghConfigDir != "" {
		values["GH_CONFIG_DIR"] = paths.ghConfigDir
	}
	if paths.xdgConfigHome != "" {
		values["XDG_CONFIG_HOME"] = paths.xdgConfigHome
	}
	if codexHome != "" {
		values["CODEX_HOME"] = codexHome
	}

	meta.ProxyInherited = sortedUnique(meta.ProxyInherited)
	meta.TokenUnset = sortedUnique(meta.TokenUnset)
	meta.Set = sortedKeys(values)

	entries := make([]string, 0, len(values))
	for _, name := range meta.Set {
		entries = append(entries, name+"="+values[name])
	}
	return envBuildResult{entries: entries, metadata: meta}
}

func mergeCommandEnv(baseEntries, commandEntries []string, cfg Config, meta *EnvMetadata) []string {
	values := envMapFromEntries(baseEntries)
	hostValues := cfg.HostEnv
	if hostValues == nil {
		hostValues = os.Environ()
	}
	host := envMapFromEntries(hostValues)
	protected := map[string]bool{
		"HOME":            true,
		"GH_CONFIG_DIR":   true,
		"XDG_CONFIG_HOME": true,
		"CODEX_HOME":      true,
	}
	for _, entry := range commandEntries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" || protected[name] {
			continue
		}
		if isTokenEnv(name) {
			if meta != nil {
				meta.TokenUnset = append(meta.TokenUnset, name)
			}
			continue
		}
		if trustedCommandEnvName(name) {
			values[name] = value
			continue
		}
		if hostValue, ok := host[name]; ok && hostValue == value {
			continue
		}
		values[name] = value
	}
	if meta != nil {
		meta.TokenUnset = sortedUnique(meta.TokenUnset)
		meta.Set = sortedKeys(values)
	}
	entries := make([]string, 0, len(values))
	for _, name := range sortedKeys(values) {
		entries = append(entries, name+"="+values[name])
	}
	return entries
}

func trustedCommandEnvName(name string) bool {
	return strings.HasPrefix(name, "ACPX_")
}

func envMapFromEntries(entries []string) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name != "" {
			out[name] = value
		}
	}
	return out
}

func unsafeMetadata(cfg Config, env EnvMetadata) Metadata {
	return Metadata{
		SandboxEnabled:    false,
		UnsafeNoSandbox:   true,
		SandboxProvider:   ProviderNone,
		FSBoundary:        FSBoundaryDisabled,
		Platform:          runtime.GOOS,
		PlatformSupported: true,
		Env:               env,
		Diagnostics:       []string{"unsafe no-sandbox mode explicitly selected; local filesystem access is not constrained to the workspace"},
	}
}

func configMinVersion(cfg Config) string {
	if strings.TrimSpace(cfg.MinBwrapVersion) != "" {
		return strings.TrimSpace(cfg.MinBwrapVersion)
	}
	return defaultMinBwrapVersion
}

func installOrUnsafeHint() string {
	return "install or upgrade bubblewrap, or explicitly rerun with --unsafe-no-sandbox to disable the filesystem boundary"
}

func commandOutputSummary(result Result, err error) string {
	var b strings.Builder
	if len(result.Stdout) > 0 {
		fmt.Fprintf(&b, "stdout=%q ", limitString(string(result.Stdout), 300))
	}
	if len(result.Stderr) > 0 {
		fmt.Fprintf(&b, "stderr=%q ", limitString(string(result.Stderr), 300))
	}
	if result.ExitCode != 0 {
		fmt.Fprintf(&b, "exit=%d ", result.ExitCode)
	}
	if err != nil {
		fmt.Fprintf(&b, "error=%v", err)
	}
	return strings.TrimSpace(b.String())
}

func limitString(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "...(truncated)"
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = true
		}
	}
	return set
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = true
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func isTokenEnv(name string) bool {
	upper := strings.ToUpper(name)
	if upper == "GH_TOKEN" || upper == "GITHUB_TOKEN" || upper == "ISSUE_SPEC_TOKEN" {
		return true
	}
	return strings.Contains(upper, "TOKEN")
}

func parseBwrapVersion(output string) (string, bool) {
	for _, field := range strings.Fields(output) {
		field = strings.Trim(field, " ,;()[]")
		field = strings.TrimPrefix(strings.ToLower(field), "v")
		if field == "" || field[0] < '0' || field[0] > '9' {
			continue
		}
		parts := strings.Split(field, ".")
		if len(parts) < 2 {
			continue
		}
		ok := true
		for _, part := range parts {
			if part == "" {
				ok = false
				break
			}
			for _, ch := range part {
				if ch < '0' || ch > '9' {
					ok = false
					break
				}
			}
			if !ok {
				break
			}
		}
		if ok {
			return field, true
		}
	}
	return "", false
}

func versionAtLeast(got, want string) bool {
	gotParts := versionParts(got)
	wantParts := versionParts(want)
	maxLen := len(gotParts)
	if len(wantParts) > maxLen {
		maxLen = len(wantParts)
	}
	for len(gotParts) < maxLen {
		gotParts = append(gotParts, 0)
	}
	for len(wantParts) < maxLen {
		wantParts = append(wantParts, 0)
	}
	for i := 0; i < maxLen; i++ {
		if gotParts[i] > wantParts[i] {
			return true
		}
		if gotParts[i] < wantParts[i] {
			return false
		}
	}
	return true
}

func versionParts(version string) []int {
	fields := strings.Split(version, ".")
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		part, err := strconv.Atoi(field)
		if err != nil {
			parts = append(parts, 0)
			continue
		}
		parts = append(parts, part)
	}
	return parts
}
