package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	HostSSHDirSandboxPath   = "/tmp/issue-spec-home/.ssh"
	HostSSHAgentSandboxPath = "/run/issue-spec/ssh-agent.sock"

	// JobScratchSandboxBase anchors the per-job disposable scratch mounts:
	// TMPDIR, GOTMPDIR, XDG_DATA_HOME, and XDG_STATE_HOME of one job.
	JobScratchSandboxBase  = "/tmp/issue-spec-scratch"
	JobTmpSandboxPath      = JobScratchSandboxBase + "/tmp"
	JobGoTmpSandboxPath    = JobScratchSandboxBase + "/go-tmp"
	JobXDGDataSandboxPath  = JobScratchSandboxBase + "/xdg-data"
	JobXDGStateSandboxPath = JobScratchSandboxBase + "/xdg-state"

	defaultMinBwrapVersion = "0.5.0"
)

var (
	ErrSandboxUnsupported     = errors.New("sandbox unsupported on this platform")
	ErrBubblewrapUnavailable  = errors.New("bubblewrap unavailable")
	ErrBubblewrapUnsupported  = errors.New("bubblewrap unsupported")
	ErrSandboxConfigInvalid   = errors.New("sandbox config invalid")
	ErrSandboxPreflightFailed = errors.New("sandbox preflight failed")
)

var defaultEnvAllowlist = []string{
	"PATH",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"TERM",
	"TZ",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"GIT_SSL_CAINFO",
	"CURL_CA_BUNDLE",
	"CLAUDE_CODE_EFFORT_LEVEL",
	"QODER_PERSONAL_ACCESS_TOKEN",
}
var proxyEnvNames = []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY", "no_proxy", "NO_PROXY"}
var defaultSystemReadOnlyBindPaths = []string{
	"/usr",
	"/bin",
	"/lib",
	"/lib64",
	"/etc/ssl/certs",
	"/etc/pki",
	"/etc/alternatives",
	"/etc/resolv.conf",
	"/etc/hosts",
	"/etc/nsswitch.conf",
}

type Config struct {
	UnsafeNoSandbox bool
	// WorkspaceReadOnly makes the assigned checkout immutable inside the
	// filesystem sandbox. In explicit unsafe-no-sandbox mode this boundary is
	// disabled and Metadata reports that fact; callers must treat dirty evidence
	// as invalid.
	WorkspaceReadOnly bool

	BwrapPath       string
	MinBwrapVersion string

	WorkspacePath     string
	TempHome          string
	TempGHConfigDir   string
	TempXDGConfigHome string
	TempCodexHome     string
	AcpxRuntimeDir    string
	// JobTmpDir, JobGoTmpDir, JobXDGDataHome, and JobXDGStateHome are the
	// job's disposable scratch directories on the host. Empty fields leave the
	// corresponding environment untouched (legacy behavior).
	JobTmpDir       string
	JobGoTmpDir     string
	JobXDGDataHome  string
	JobXDGStateHome string
	HostGHConfigDir string
	// HostSSHDir and HostSSHAgentSocket are explicit opt-ins. In bubblewrap
	// mode the directory is mounted read-only at HOME/.ssh and the optional
	// Unix socket is mounted at a fixed path. In explicit unsafe mode the host
	// home is reused so stock SSH can find this directory without a mount.
	HostSSHDir         string
	HostSSHAgentSocket string

	HostEnv             []string
	EnvAllowlist        []string
	ExtraEnv            map[string]string
	DisableProxyEnv     bool
	SystemReadOnlyBinds []string
	ReadOnlyBinds       []string
	// WritableBinds are narrowly-scoped, existing host directories exposed at
	// the same absolute path. They are validated strictly and must not overlap
	// the coordinator checkout or another declared mount.
	WritableBinds    []string
	FileCapabilities []FileCapability
}

// FileCapability is a broker-owned, read-only file exposed at a fixed path.
// Source is never emitted in persisted sandbox metadata.
type FileCapability struct {
	Source      string
	Destination string
	EnvName     string
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

	TmpDir       string
	GoTmpDir     string
	XDGDataHome  string
	XDGStateHome string
}

type Mount struct {
	Source      string
	Destination string
	Mode        string
}

func Preflight(ctx context.Context, cfg Config, deps Dependencies) (Metadata, error) {
	if err := validateFileCapabilities(cfg.FileCapabilities); err != nil {
		return Metadata{}, err
	}
	if _, err := validatedWritableBinds(cfg); err != nil {
		return Metadata{}, err
	}
	if err := validateHostSSHConfig(cfg); err != nil {
		return Metadata{}, err
	}
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
	if err := validateFileCapabilities(cfg.FileCapabilities); err != nil {
		return PreparedCommand{}, err
	}
	if _, err := validatedWritableBinds(cfg); err != nil {
		return PreparedCommand{}, err
	}
	if err := validateHostSSHConfig(cfg); err != nil {
		return PreparedCommand{}, err
	}
	if cfg.UnsafeNoSandbox {
		env := scrubEnvironment(cfg, hostEnvPaths(cfg), true)
		meta := unsafeMetadata(cfg, env.metadata)
		if env.err != nil {
			return PreparedCommand{Metadata: meta}, env.err
		}
		env.entries = unsafeCapabilityEntries(env.entries, cfg.FileCapabilities)
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
	tmpDir        string
	goTmpDir      string
	xdgDataHome   string
	xdgStateHome  string
}

func hostEnvPaths(cfg Config) envPaths {
	home := cfg.TempHome
	if cfg.UnsafeNoSandbox && strings.TrimSpace(cfg.HostSSHDir) != "" {
		home = filepath.Dir(filepath.Clean(cfg.HostSSHDir))
	}
	return envPaths{
		home:          home,
		ghConfigDir:   cfg.TempGHConfigDir,
		xdgConfigHome: cfg.TempXDGConfigHome,
		codexHome:     cfg.TempCodexHome,
		tmpDir:        cfg.JobTmpDir,
		goTmpDir:      cfg.JobGoTmpDir,
		xdgDataHome:   cfg.JobXDGDataHome,
		xdgStateHome:  cfg.JobXDGStateHome,
	}
}

func sandboxEnvPaths() envPaths {
	return envPaths{
		home:          "/tmp/issue-spec-home",
		ghConfigDir:   "/tmp/issue-spec-gh",
		xdgConfigHome: "/tmp/issue-spec-xdg",
		codexHome:     "/tmp/issue-spec-codex",
		tmpDir:        JobTmpSandboxPath,
		goTmpDir:      JobGoTmpSandboxPath,
		xdgDataHome:   JobXDGDataSandboxPath,
		xdgStateHome:  JobXDGStateSandboxPath,
	}
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
	// Job scratch env is opt-in per field: an unset host dir keeps the
	// inherited/default behavior for that variable.
	tmpDir := ""
	if strings.TrimSpace(cfg.JobTmpDir) != "" {
		tmpDir = paths.tmpDir
	}
	goTmpDir := ""
	if strings.TrimSpace(cfg.JobGoTmpDir) != "" {
		goTmpDir = paths.goTmpDir
	}
	xdgDataHome := ""
	if strings.TrimSpace(cfg.JobXDGDataHome) != "" {
		xdgDataHome = paths.xdgDataHome
	}
	xdgStateHome := ""
	if strings.TrimSpace(cfg.JobXDGStateHome) != "" {
		xdgStateHome = paths.xdgStateHome
	}

	values := map[string]string{}
	meta := EnvMetadata{
		Home:          paths.home,
		GHConfigDir:   paths.ghConfigDir,
		XDGConfigHome: paths.xdgConfigHome,
		CodexHome:     codexHome,
		TmpDir:        tmpDir,
		GoTmpDir:      goTmpDir,
		XDGDataHome:   xdgDataHome,
		XDGStateHome:  xdgStateHome,
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
	if tmpDir != "" {
		values["TMPDIR"] = tmpDir
	}
	if goTmpDir != "" {
		values["GOTMPDIR"] = goTmpDir
	}
	if xdgDataHome != "" {
		values["XDG_DATA_HOME"] = xdgDataHome
	}
	if xdgStateHome != "" {
		values["XDG_STATE_HOME"] = xdgStateHome
	}
	for _, capability := range cfg.FileCapabilities {
		values[capability.EnvName] = capability.Destination
	}
	if strings.TrimSpace(cfg.HostSSHAgentSocket) != "" {
		if cfg.UnsafeNoSandbox {
			values["SSH_AUTH_SOCK"] = filepath.Clean(cfg.HostSSHAgentSocket)
		} else {
			values["SSH_AUTH_SOCK"] = HostSSHAgentSandboxPath
		}
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

func validateFileCapabilities(capabilities []FileCapability) error {
	allowedEnv := map[string]bool{
		"ISSUE_SPEC_TOKEN_FILE": true, "GIT_ASKPASS": true,
		"ISSUE_SPEC_GIT_USERNAME_FILE": true, "ISSUE_SPEC_GIT_SECRET_FILE": true,
	}
	seenDestination := map[string]bool{}
	seenEnv := map[string]bool{}
	for _, capability := range capabilities {
		source := filepath.Clean(strings.TrimSpace(capability.Source))
		destination := filepath.Clean(strings.TrimSpace(capability.Destination))
		if !filepath.IsAbs(source) || !filepath.IsAbs(destination) ||
			(destination != "/run/issue-spec" && !strings.HasPrefix(destination, "/run/issue-spec/")) ||
			!allowedEnv[capability.EnvName] || seenDestination[destination] || seenEnv[capability.EnvName] {
			return fmt.Errorf("%w: invalid or duplicate credential file capability", ErrSandboxConfigInvalid)
		}
		info, err := os.Lstat(source)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%w: credential capability source must be a private regular file", ErrSandboxConfigInvalid)
		}
		if !singleLink(info) {
			return fmt.Errorf("%w: credential capability source must not be hard-linked", ErrSandboxConfigInvalid)
		}
		seenDestination[destination] = true
		seenEnv[capability.EnvName] = true
	}
	return nil
}

func validatedWritableBinds(cfg Config) ([]string, error) {
	workspace := filepath.Clean(strings.TrimSpace(cfg.WorkspacePath))
	if len(cfg.WritableBinds) > 0 && workspace != "." {
		if !filepath.IsAbs(workspace) {
			return nil, fmt.Errorf("%w: workspace path must be absolute when writable binds are declared: %s", ErrSandboxConfigInvalid, workspace)
		}
		canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			return nil, fmt.Errorf("%w: workspace path must be canonicalizable when writable binds are declared: %s", ErrSandboxConfigInvalid, workspace)
		}
		workspace = filepath.Clean(canonicalWorkspace)
	}
	reserved := []string{workspace, cfg.TempHome, cfg.TempGHConfigDir, cfg.TempXDGConfigHome, cfg.TempCodexHome, cfg.AcpxRuntimeDir, cfg.HostSSHDir, cfg.HostSSHAgentSocket,
		cfg.JobTmpDir, cfg.JobGoTmpDir, cfg.JobXDGDataHome, cfg.JobXDGStateHome}
	reserved = append(reserved, cfg.ReadOnlyBinds...)
	systemBinds := cfg.SystemReadOnlyBinds
	if len(systemBinds) == 0 {
		systemBinds = defaultSystemReadOnlyBindPaths
	}
	reserved = append(reserved, systemBinds...)
	for i, path := range reserved {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || !filepath.IsAbs(path) {
			reserved[i] = path
			continue
		}
		if canonical, err := filepath.EvalSymlinks(path); err == nil {
			reserved[i] = filepath.Clean(canonical)
		} else {
			reserved[i] = path
		}
	}
	var out []string
	for _, raw := range cfg.WritableBinds {
		raw = strings.TrimSpace(raw)
		if raw == "" || !filepath.IsAbs(raw) {
			return nil, fmt.Errorf("%w: writable bind must be an absolute path: %q", ErrSandboxConfigInvalid, raw)
		}
		clean := filepath.Clean(raw)
		if clean == string(os.PathSeparator) {
			return nil, fmt.Errorf("%w: filesystem root cannot be a writable bind", ErrSandboxConfigInvalid)
		}
		info, err := os.Lstat(clean)
		if err != nil {
			return nil, fmt.Errorf("%w: writable bind %s must exist: %v", ErrSandboxConfigInvalid, clean, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("%w: writable bind %s must be a canonical directory", ErrSandboxConfigInvalid, clean)
		}
		canonical, err := filepath.EvalSymlinks(clean)
		if err != nil || filepath.Clean(canonical) != clean {
			return nil, fmt.Errorf("%w: writable bind %s must be canonical", ErrSandboxConfigInvalid, clean)
		}
		for _, existing := range append(append([]string{}, reserved...), out...) {
			existing = filepath.Clean(strings.TrimSpace(existing))
			if existing == "." || !filepath.IsAbs(existing) {
				continue
			}
			if pathsOverlap(clean, existing) {
				return nil, fmt.Errorf("%w: writable bind %s overlaps declared path %s", ErrSandboxConfigInvalid, clean, existing)
			}
		}
		out = append(out, clean)
	}
	return out, nil
}

func pathsOverlap(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	return left == right || pathDescendsFrom(left, right) || pathDescendsFrom(right, left)
}

func pathDescendsFrom(root, candidate string) bool {
	if root == string(os.PathSeparator) {
		return candidate != root
	}
	return strings.HasPrefix(candidate, root+string(os.PathSeparator))
}

func validateHostSSHConfig(cfg Config) error {
	directory := strings.TrimSpace(cfg.HostSSHDir)
	socket := strings.TrimSpace(cfg.HostSSHAgentSocket)
	if directory == "" && socket == "" {
		return nil
	}
	if directory == "" || !filepath.IsAbs(directory) || filepath.Base(filepath.Clean(directory)) != ".ssh" {
		return fmt.Errorf("%w: host SSH directory must be an absolute .ssh path", ErrSandboxConfigInvalid)
	}
	directory = filepath.Clean(directory)
	home := filepath.Dir(directory)
	if home == "/" || (!cfg.UnsafeNoSandbox && directory == HostSSHDirSandboxPath) {
		return fmt.Errorf("%w: host SSH home path is unsafe for sandbox mounting", ErrSandboxConfigInvalid)
	}
	for _, reserved := range []string{cfg.WorkspacePath, cfg.TempHome, cfg.TempGHConfigDir, cfg.TempXDGConfigHome, cfg.TempCodexHome} {
		reserved = filepath.Clean(strings.TrimSpace(reserved))
		if reserved == "." || !filepath.IsAbs(reserved) {
			continue
		}
		if directory == reserved || pathUnder(reserved, directory) || pathUnder(directory, reserved) {
			return fmt.Errorf("%w: host SSH directory overlaps a runner-managed path", ErrSandboxConfigInvalid)
		}
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: host SSH directory must be a non-symlink directory", ErrSandboxConfigInvalid)
	}
	if socket == "" {
		return nil
	}
	if !filepath.IsAbs(socket) {
		return fmt.Errorf("%w: host SSH agent socket must be absolute", ErrSandboxConfigInvalid)
	}
	info, err = os.Lstat(socket)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: host SSH agent socket must be a non-symlink Unix socket", ErrSandboxConfigInvalid)
	}
	return nil
}

func pathUnder(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
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
		"SSH_AUTH_SOCK":   true,
		"TMPDIR":          true,
		"GOTMPDIR":        true,
		"XDG_DATA_HOME":   true,
		"XDG_STATE_HOME":  true,
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

// unsafeCapabilityEntries exposes broker-owned private files by their host
// paths only after the operator has explicitly disabled the filesystem
// sandbox. Bubblewrap mode continues to use fixed /run/issue-spec mount
// destinations. Values are deliberately absent from persisted metadata.
func unsafeCapabilityEntries(entries []string, capabilities []FileCapability) []string {
	values := envMapFromEntries(entries)
	for _, capability := range capabilities {
		values[capability.EnvName] = capability.Source
	}
	out := make([]string, 0, len(values))
	for _, name := range sortedKeys(values) {
		out = append(out, name+"="+values[name])
	}
	return out
}

func unsafeMetadata(cfg Config, env EnvMetadata) Metadata {
	diagnostics := []string{"unsafe no-sandbox mode explicitly selected; local filesystem access is not constrained to the workspace"}
	if len(cfg.FileCapabilities) > 0 {
		diagnostics = append(diagnostics, "brokered credential files are exposed by private host path because no filesystem sandbox is active")
	}
	return Metadata{
		SandboxEnabled:    false,
		UnsafeNoSandbox:   true,
		SandboxProvider:   ProviderNone,
		FSBoundary:        FSBoundaryDisabled,
		Platform:          runtime.GOOS,
		PlatformSupported: true,
		Env:               env,
		Diagnostics:       diagnostics,
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
	// QODER_PERSONAL_ACCESS_TOKEN is an explicit qodercli credential source that
	// the runner allowlists into the sandbox for qoder agent jobs.
	if upper == "QODER_PERSONAL_ACCESS_TOKEN" {
		return false
	}
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
