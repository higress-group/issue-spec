package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
	runnerserver "github.com/higress-group/issue-spec/internal/commentrunner/server"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/storage"
	"github.com/higress-group/issue-spec/internal/gitidentity"
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (a *app) runRunnerServe(ctx context.Context, args []string) (exitCode int) {
	fs := newFlagSet("runner serve", a.err)
	var repos, allowedUsers, previousFiles, previousEnvs, gitCredentialArgs, operatorSkillDirs stringListFlag
	listen := fs.String("listen", "127.0.0.1:9876", "dedicated webhook listen address")
	runner := fs.String("runner", "", "self-hosted runner identity")
	organization := fs.String("organization", "", "organization name dynamically served by this subscription")
	statePath := fs.String("state", "", "durable runner state path")
	workspaceRoot := fs.String("workspace-root", "", "runner workspace root")
	workspaceRetention := fs.Duration("workspace-retention", 7*24*time.Hour, "non-active workspace retention")
	storageMinFree := fs.Int64("storage-min-free-bytes", 0, "minimum free filesystem bytes required before dispatch; 0 disables the statfs admission guard")
	storageOrphanGrace := fs.Duration("storage-orphan-grace", storage.DefaultOrphanGrace, "observation window before an unmatched storage runtime becomes deletion-eligible")
	acpxPath := fs.String("acpx", "acpx", "acpx executable path")
	agentKind := fs.String("agent", commentrunner.AgentCodex, "coordinator agent: codex, claude, or qoder")
	model := fs.String("model", "", "optional coordinator model")
	qoderFullAccess := fs.Bool("qoder-agent-full-access", false, "require Qoder agent-full-access policy for workflow CLI/shell work")
	unsafeNoSandbox := fs.Bool("unsafe-no-sandbox", false, "explicitly disable the filesystem sandbox")
	bwrapPath := fs.String("bwrap", "", "bubblewrap executable path")
	cancellationEnabled := fs.Bool("cancellation-enabled", true, "allow /cancel commands")
	subscriptionID := fs.String("subscription-id", "", "operator-configured webhook subscription UUID")
	secretFile := fs.String("secret-file", "", "0600 file containing the current webhook secret")
	secretEnv := fs.String("secret-env", "", "environment variable containing the current webhook secret")
	previousValidUntil := fs.String("previous-secrets-valid-until", "", "absolute RFC3339 expiry shared by previous secrets")
	timestampWindow := fs.Duration("timestamp-window", 5*time.Minute, "accepted signed request timestamp window")
	maxBody := fs.Int64("max-body-bytes", 1<<20, "maximum webhook request body bytes")
	maxRawComment := fs.Int("max-raw-comment-bytes", 256<<10, "maximum raw issue/comment body bytes inside an envelope")
	maxTotal := fs.Int64("max-queue-bytes", 16<<20, "maximum total pending and processing raw envelope bytes")
	maxActive := fs.Int("max-queue-deliveries", 1000, "maximum pending and processing deliveries")
	maxRequests := fs.Int("max-concurrent-requests", 32, "maximum concurrent webhook requests")
	maxConnections := fs.Int("max-connections", 128, "maximum simultaneous listener connections")
	maxHeader := fs.Int("max-header-bytes", 32<<10, "maximum HTTP request header bytes")
	readHeaderTimeout := fs.Duration("read-header-timeout", 5*time.Second, "HTTP header read timeout")
	readTimeout := fs.Duration("read-timeout", 15*time.Second, "HTTP request read timeout")
	writeTimeout := fs.Duration("write-timeout", 15*time.Second, "HTTP response write timeout")
	idleTimeout := fs.Duration("idle-timeout", time.Minute, "HTTP idle connection timeout")
	shutdownTimeout := fs.Duration("shutdown-timeout", 30*time.Second, "graceful shutdown deadline")
	retryAfter := fs.Duration("retry-after", 5*time.Second, "backpressure Retry-After duration")
	tlsCert := fs.String("tls-cert", "", "TLS certificate PEM file")
	tlsKey := fs.String("tls-key", "", "0600 TLS private key PEM file")
	production := fs.Bool("production", false, "require TLS and an explicit non-loopback bind")
	gitCredentialCommand := fs.String("git-credential-command", "", "absolute operator command implementing issue-spec-git-credential-v1")
	allowHostSSH := fs.Bool("allow-host-ssh", false, "reuse the runner account ~/.ssh inside the sandbox for trusted internal repositories")
	gitAuthorName := fs.String("git-author-name", "", "repo-local Git commit author name; requires --git-author-email")
	gitAuthorEmail := fs.String("git-author-email", "", "repo-local Git commit author email; requires --git-author-name")
	fs.Var(&operatorSkillDirs, "operator-skill-dir", "operator-owned local skill directory, or a directory containing skill directories; repeat as needed")
	gitCredentialTimeout := fs.Duration("git-credential-timeout", 30*time.Second, "operator git credential command timeout")
	gitCredentialMaxOutput := fs.Int64("git-credential-max-output", 1<<20, "maximum operator git credential command output bytes")
	gitCredentialConcurrency := fs.Int("git-credential-concurrency", 4, "maximum concurrent operator git credential command invocations")
	reconcileWorkers := fs.Int("reconcile-workers", 2, "durable webhook reconciliation workers")
	reconcileLease := fs.Duration("reconcile-lease", 2*time.Minute, "durable webhook processing lease")
	maxConcurrentJobs := fs.Int("max-concurrent-jobs", 3, "maximum concurrently dispatched runner jobs")
	logDir := fs.String("log-dir", "", "directory for persistent diagnostic logs; default is a sibling 'logs/' directory beside the state file")
	logMaxSize := fs.Int("log-max-size", 10, "maximum diagnostic log file size in MB before rotation")
	logMaxFiles := fs.Int("log-max-files", 5, "maximum number of rotated diagnostic log files to retain")
	logRetention := fs.Int("log-retention", 30, "diagnostic log retention duration in days")
	logRawCapture := fs.Int("log-raw-capture", 100, "maximum ACPX stdout/stderr capture size in KB per job")
	fs.Var(&repos, "repo", "repository owner/name; repeat for every repository served by this subscription")
	fs.Var(&allowedUsers, "allowed-user", "runner command author allowlist; repeat as needed")
	fs.Var(&previousFiles, "previous-secret-file", "0600 previous secret file; repeat up to four times")
	fs.Var(&previousEnvs, "previous-secret-env", "previous secret environment variable; repeat up to four times")
	fs.Var(&gitCredentialArgs, "git-credential-arg", "trusted operator argument passed directly to the git credential command; repeat as needed")
	if argsContainHelp(args) {
		fs.SetOutput(a.out)
		fs.Usage()
		return 0
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	seen := visitedFlags(fs)
	profile, _, err := auth.ResolveProfile(a.profileName, "github.com")
	if err != nil {
		a.errorf("runner serve profile: %v\n", err)
		return 2
	}
	if profile.Kind != auth.ProfileKindHosted {
		a.errorf("runner serve requires a self-hosted profile; GitHub profiles use runner poll\n")
		return 2
	}
	if *production && !seen["listen"] {
		a.errorf("runner serve production mode requires an explicit --listen address\n")
		return 2
	}
	organizationName := strings.TrimSpace(*organization)
	if (len(repos.Values()) == 0) == (organizationName == "") || strings.TrimSpace(*runner) == "" {
		a.errorf("runner serve requires exactly one of --organization or one-or-more --repo, and --runner\n")
		return 2
	}
	if _, err := gitidentity.Normalize(*gitAuthorName, *gitAuthorEmail); err != nil {
		a.errorf("runner serve --git-author-name and --git-author-email: %v\n", err)
		return 2
	}
	if *allowHostSSH && strings.TrimSpace(*gitCredentialCommand) != "" {
		a.errorf("runner serve requires exactly one of --git-credential-command or --allow-host-ssh\n")
		return 2
	}
	if !*allowHostSSH && strings.TrimSpace(*gitCredentialCommand) == "" {
		a.errorf("runner serve requires --git-credential-command for job-scoped clone credentials\n")
		return 2
	}
	if *allowHostSSH && len(gitCredentialArgs.Values()) > 0 {
		a.errorf("runner serve --git-credential-arg requires --git-credential-command\n")
		return 2
	}
	if (*secretFile == "") == (*secretEnv == "") {
		a.errorf("runner serve requires exactly one of --secret-file or --secret-env\n")
		return 2
	}
	if *production && (*secretEnv != "" || len(previousEnvs.Values()) > 0) {
		a.errorf("runner serve production mode requires webhook secrets from 0600 files\n")
		return 2
	}
	if *secretEnv != "" && !environmentNamePattern.MatchString(*secretEnv) {
		a.errorf("runner serve --secret-env must be an environment variable name\n")
		return 2
	}
	previousCount := len(previousFiles.Values()) + len(previousEnvs.Values())
	if previousCount > 4 {
		a.errorf("runner serve supports at most four previous secrets\n")
		return 2
	}
	for _, name := range previousEnvs.Values() {
		if !environmentNamePattern.MatchString(name) {
			a.errorf("runner serve --previous-secret-env must contain environment variable names\n")
			return 2
		}
	}
	if *timestampWindow <= 0 || *timestampWindow > time.Hour || *maxBody <= 0 || *maxBody > 16<<20 ||
		*maxRawComment <= 0 || int64(*maxRawComment) > *maxBody || *maxTotal < *maxBody || *maxTotal > 256<<20 ||
		*maxActive <= 0 || *maxActive > 10000 || *maxRequests <= 0 || *maxRequests > 1024 ||
		*maxConnections <= 0 || *maxConnections > 4096 || *maxHeader < 1024 || *maxHeader > 1<<20 ||
		*readHeaderTimeout <= 0 || *readTimeout <= 0 || *writeTimeout <= 0 || *idleTimeout <= 0 ||
		*readHeaderTimeout > time.Minute || *readTimeout > 5*time.Minute || *writeTimeout > 5*time.Minute ||
		*idleTimeout > 10*time.Minute || *shutdownTimeout <= 0 || *shutdownTimeout > 5*time.Minute ||
		*retryAfter <= 0 || *retryAfter > time.Hour {
		a.errorf("runner serve limits and timeouts are outside their safe bounds\n")
		return 2
	}
	if *gitCredentialTimeout <= 0 || *gitCredentialTimeout > 2*time.Minute || *gitCredentialMaxOutput < 1024 ||
		*gitCredentialMaxOutput > 4<<20 || *gitCredentialConcurrency < 1 || *gitCredentialConcurrency > 32 ||
		*reconcileWorkers < 1 || *reconcileWorkers > 32 || *reconcileLease < 10*time.Second ||
		*reconcileLease > 10*time.Minute || *maxConcurrentJobs < 1 || *maxConcurrentJobs > 32 {
		a.errorf("runner serve worker and credential provider limits are outside their safe bounds\n")
		return 2
	}
	if *tlsKey != "" {
		if err := runnerserver.ValidatePrivateFile(*tlsKey); err != nil {
			a.errorf("runner serve TLS key: %v\n", err)
			return 2
		}
	}
	now := time.Now().UTC()
	var previousExpiry time.Time
	previousExpiry, err = parsePreviousExpiry(now, *previousValidUntil, previousCount)
	if err != nil {
		if previousCount > 0 {
			a.errorf("runner serve previous secrets require --previous-secrets-valid-until RFC3339 strictly in the future and no more than 24h ahead\n")
		} else {
			a.errorf("runner serve --previous-secrets-valid-until requires at least one previous secret\n")
		}
		return 2
	}
	current, err := (runnerserver.SecretReference{File: *secretFile, Env: *secretEnv}).Load()
	if err != nil {
		a.errorf("runner serve current secret: %v\n", err)
		return 2
	}
	defer clear(current)
	previous := make([]webhook.Secret, 0, len(previousFiles.Values())+len(previousEnvs.Values()))
	for _, file := range previousFiles.Values() {
		value, err := (runnerserver.SecretReference{File: file}).Load()
		if err != nil {
			clearSecrets(previous)
			a.errorf("runner serve previous secret: %v\n", err)
			return 2
		}
		previous = append(previous, webhook.Secret{Value: value, ValidUntil: previousExpiry})
	}
	for _, name := range previousEnvs.Values() {
		value, err := (runnerserver.SecretReference{Env: name}).Load()
		if err != nil {
			clearSecrets(previous)
			a.errorf("runner serve previous secret: %v\n", err)
			return 2
		}
		previous = append(previous, webhook.Secret{Value: value, ValidUntil: previousExpiry})
	}
	defer clearSecrets(previous)
	credentials, err := webhook.NewCredentials(*subscriptionID, webhook.Secret{Value: current}, previous)
	var subscriptionSecret []byte
	if organizationName != "" {
		subscriptionSecret = append([]byte(nil), current...)
	}
	clear(current)
	defer clear(subscriptionSecret)
	clearSecrets(previous)
	if err != nil {
		a.errorf("runner serve credentials: %v\n", err)
		return 2
	}
	scopeProfile := profile.Name
	if profile.Ephemeral {
		scopeProfile = ""
	}
	runnerConfig, err := commentrunner.DefaultConfigFromEnv()
	if err != nil {
		a.errorf("runner serve defaults: %v\n", err)
		return 2
	}
	runnerConfig.Profile, runnerConfig.Hostname = scopeProfile, profile.Hostname
	runnerConfig.Repositories, runnerConfig.Organization, runnerConfig.SubscriptionID, runnerConfig.RunnerIdentity =
		repos.Values(), organizationName, credentials.SubscriptionID(), *runner
	runnerConfig.AllowedUsers, runnerConfig.StatePath, runnerConfig.WorkspaceRoot = allowedUsers.Values(), strings.TrimSpace(*statePath), strings.TrimSpace(*workspaceRoot)
	runnerConfig.MaxConcurrentJobs, runnerConfig.AcpxPath = *maxConcurrentJobs, strings.TrimSpace(*acpxPath)
	runnerConfig.Agent.Kind, runnerConfig.Agent.Model = strings.TrimSpace(*agentKind), strings.TrimSpace(*model)
	if seen["qoder-agent-full-access"] {
		runnerConfig.Agent.QoderAgentFullAccess = *qoderFullAccess
	}
	runnerConfig.WorkspaceRetention = commentrunner.NewDuration(*workspaceRetention)
	runnerConfig.StorageMinFreeBytes = *storageMinFree
	runnerConfig.StorageOrphanGrace = commentrunner.NewDuration(*storageOrphanGrace)
	runnerConfig.UnsafeNoSandbox, runnerConfig.BwrapPath = *unsafeNoSandbox, strings.TrimSpace(*bwrapPath)
	runnerConfig.AllowHostSSH = *allowHostSSH
	runnerConfig.GitAuthorName, runnerConfig.GitAuthorEmail = *gitAuthorName, *gitAuthorEmail
	runnerConfig.OperatorSkillDirs, err = resolveRunnerOperatorSkillDirs(operatorSkillDirs.Values())
	if err != nil {
		a.errorf("runner serve operator skills: %v\n", err)
		return 2
	}
	runnerConfig.CancellationEnabled = *cancellationEnabled
	if seen["log-dir"] {
		runnerConfig.LogDir = strings.TrimSpace(*logDir)
	}
	if seen["log-max-size"] {
		runnerConfig.LogMaxSizeMB = *logMaxSize
	}
	if seen["log-max-files"] {
		runnerConfig.LogMaxFiles = *logMaxFiles
	}
	if seen["log-retention"] {
		runnerConfig.LogRetentionDays = *logRetention
	}
	if seen["log-raw-capture"] {
		runnerConfig.LogRawCaptureKB = *logRawCapture
	}
	runnerConfig, err = commentrunner.ApplyDefaultRunnerScopePaths(runnerConfig, seen["state"], seen["workspace-root"])
	if err != nil {
		a.errorf("runner serve state scope: %v\n", err)
		return 2
	}
	if err := runnerConfig.Validate(); err != nil {
		a.errorf("runner serve runner configuration: %v\n", err)
		return 2
	}
	logger, err := newRunnerLogger(runnerConfig)
	if err != nil {
		a.errorf("runner serve diagnostics: %v\n", err)
		return 1
	}
	a.runnerDiagnostics = logger
	runnerConfig.LogDir = logger.config().LogDir
	defer func() {
		if err := logger.shutdown("serve"); err != nil {
			a.errorf("runner serve diagnostics shutdown: %v\n", err)
			if exitCode == 0 {
				exitCode = 1
			}
		}
		a.runnerDiagnostics = nil
	}()
	if err := logger.logStartup("serve"); err != nil {
		a.errorf("runner serve diagnostics: %v\n", err)
		return 1
	}
	if _, err := logger.newCycle(); err != nil {
		a.errorf("runner serve diagnostics: %v\n", err)
		return 1
	}
	preflight := a.runRunnerPreflight(ctx, runnerConfig)
	if err := logger.logPreflight(preflight); err != nil {
		a.errorf("runner serve diagnostics: %v\n", err)
		return 1
	}
	if !preflight.OK {
		a.errorf("runner serve preflight failed\n")
		a.printPreflightReport(preflight)
		return 1
	}
	// One canonical root has one destructive owner: serve acquires the owner
	// lock before the state flock and holds it for the process lifetime.
	owner, err := storage.AcquireOwner(runnerConfig.WorkspaceRoot)
	if err != nil {
		a.errorf("runner serve storage owner: %v\n", err)
		return 1
	}
	defer owner.Release()
	ctx = storage.WithOwner(ctx, owner)
	if _, err := storage.EnsureRawStateBackup(runnerConfig.WorkspaceRoot, runnerConfig.StatePath); err != nil {
		a.errorf("runner serve storage backup: %v\n", err)
		return 1
	}
	store, err := crstate.OpenFileStore(runnerConfig.StatePath)
	if err != nil {
		a.errorf("runner serve state: %v\n", err)
		return 1
	}
	defer store.Close()
	existing, err := store.Load(ctx)
	if err != nil {
		a.errorf("runner serve state: %v\n", err)
		return 1
	}
	if err := store.Save(ctx, existing); err != nil {
		a.errorf("runner serve state migration: %v\n", err)
		return 1
	}
	queue, err := webhook.NewQueue(store, webhook.QueueConfig{MaxActiveDeliveries: *maxActive,
		MaxItemBytes: *maxBody, MaxTotalBytes: *maxTotal})
	if err != nil {
		a.errorf("runner serve queue: %v\n", err)
		return 2
	}
	handler, err := webhook.NewHandler(webhook.HandlerConfig{Credentials: credentials, Queue: queue,
		TimestampWindow: *timestampWindow, MaxBodyBytes: *maxBody, MaxRawCommentBytes: *maxRawComment,
		MaxConcurrentRequests: *maxRequests, RetryAfter: *retryAfter, Observer: logger})
	if err != nil {
		a.errorf("runner serve handler: %v\n", err)
		return 2
	}
	service, err := runnerserver.New(runnerserver.Config{ListenAddress: *listen, TLSCertFile: *tlsCert,
		TLSKeyFile: *tlsKey, Production: *production,
		ReadHeaderTimeout: *readHeaderTimeout,
		ReadTimeout:       *readTimeout, WriteTimeout: *writeTimeout, IdleTimeout: *idleTimeout,
		ShutdownTimeout: *shutdownTimeout, MaxHeaderBytes: *maxHeader, MaxConnections: *maxConnections}, handler)
	if err != nil {
		a.errorf("runner serve configuration: %v\n", err)
		return 2
	}
	profileToken, err := auth.ResolveProfileToken(ctx, profile)
	if err != nil || strings.TrimSpace(profileToken.Value) == "" {
		a.errorf("runner serve profile credential: origin-bound profile PAT is required\n")
		return 2
	}
	if profileToken.Source == "env:ISSUE_SPEC_TOKEN" {
		_ = os.Unsetenv("ISSUE_SPEC_TOKEN")
	}
	storageLifecycle := runnerStorageLifecycle(ctx, runnerConfig, store)
	runtime, err := runnerServeBuildRuntime(ctx, runnerServeRuntimeInput{Profile: profile, ProfileToken: profileToken.Value,
		SubscriptionSecret: subscriptionSecret,
		Runner:             runnerConfig, Queue: queue, Store: store, HTTP: service, Storage: storageLifecycle, GitCredentialCommand: *gitCredentialCommand,
		GitCredentialArgs: gitCredentialArgs.Values(), GitCredentialTimeout: *gitCredentialTimeout,
		GitCredentialMaxOutput: *gitCredentialMaxOutput, GitCredentialConcurrency: *gitCredentialConcurrency,
		ReconcileWorkers: *reconcileWorkers, ReconcileLease: *reconcileLease, Diagnostics: logger})
	clear(subscriptionSecret)
	if err != nil {
		a.errorf("runner serve runtime: %v\n", err)
		return 2
	}
	serveContext, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(a.out, "runner serve: profile=%s subscription=%s listen=%s endpoint=%s state=%s logs=%s\n",
		profile.Name, credentials.SubscriptionID(), *listen, webhook.Endpoint, runnerConfig.StatePath, logger.config().LogDir)
	if err := runnerServeRun(serveContext, runtime); err != nil && !errors.Is(err, context.Canceled) {
		a.errorf("runner serve: %v\n", err)
		return 1
	}
	return 0
}

func resolveRunnerOperatorSkillDirs(values []string) ([]string, error) {
	seen := map[string]bool{}
	resolved := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("--operator-skill-dir must not be empty")
		}
		path, err := filepath.Abs(value)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", value, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("%s must be a non-symlink directory", path)
		}
		path = filepath.Clean(path)
		if !seen[path] {
			seen[path] = true
			resolved = append(resolved, path)
		}
	}
	return resolved, nil
}

func parsePreviousExpiry(now time.Time, raw string, count int) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if count == 0 {
		if raw != "" {
			return time.Time{}, errors.New("previous expiry without previous secret")
		}
		return time.Time{}, nil
	}
	expiry, err := time.Parse(time.RFC3339, raw)
	if err != nil || expiry.IsZero() || !expiry.After(now) || expiry.After(now.Add(24*time.Hour)) {
		return time.Time{}, errors.New("invalid previous secret expiry")
	}
	return expiry.UTC(), nil
}

func clearSecrets(values []webhook.Secret) {
	for index := range values {
		clear(values[index].Value)
	}
}
