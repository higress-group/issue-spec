package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
	runnerserver "github.com/higress-group/issue-spec/internal/commentrunner/server"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
)

var runnerServeRun = func(ctx context.Context, service *runnerserver.Service) error {
	return service.Run(ctx)
}

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (a *app) runRunnerServe(ctx context.Context, args []string) int {
	fs := newFlagSet("runner serve", a.err)
	var repos, previousFiles, previousEnvs stringListFlag
	listen := fs.String("listen", "127.0.0.1:9876", "dedicated webhook listen address")
	runner := fs.String("runner", "", "self-hosted runner identity")
	statePath := fs.String("state", "", "durable runner state path")
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
	fs.Var(&repos, "repo", "repository owner/name; repeat for every repository served by this subscription")
	fs.Var(&previousFiles, "previous-secret-file", "0600 previous secret file; repeat up to four times")
	fs.Var(&previousEnvs, "previous-secret-env", "previous secret environment variable; repeat up to four times")
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
	if len(repos.Values()) == 0 || strings.TrimSpace(*runner) == "" {
		a.errorf("runner serve requires at least one --repo and --runner\n")
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
	clear(current)
	clearSecrets(previous)
	if err != nil {
		a.errorf("runner serve credentials: %v\n", err)
		return 2
	}
	scopeProfile := profile.Name
	if profile.Ephemeral {
		scopeProfile = ""
	}
	runnerConfig := commentrunner.Config{Profile: scopeProfile, Hostname: profile.Hostname,
		Repositories: repos.Values(), RunnerIdentity: *runner, StatePath: strings.TrimSpace(*statePath)}
	runnerConfig, err = commentrunner.ApplyDefaultRunnerScopePaths(runnerConfig, seen["state"], false)
	if err != nil {
		a.errorf("runner serve state scope: %v\n", err)
		return 2
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
		MaxConcurrentRequests: *maxRequests, RetryAfter: *retryAfter})
	if err != nil {
		a.errorf("runner serve handler: %v\n", err)
		return 2
	}
	service, err := runnerserver.New(runnerserver.Config{ListenAddress: *listen, TLSCertFile: *tlsCert,
		TLSKeyFile: *tlsKey, Production: *production, ReadHeaderTimeout: *readHeaderTimeout,
		ReadTimeout: *readTimeout, WriteTimeout: *writeTimeout, IdleTimeout: *idleTimeout,
		ShutdownTimeout: *shutdownTimeout, MaxHeaderBytes: *maxHeader, MaxConnections: *maxConnections}, handler)
	if err != nil {
		a.errorf("runner serve configuration: %v\n", err)
		return 2
	}
	serveContext, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(a.out, "runner serve: profile=%s subscription=%s listen=%s endpoint=%s state=%s\n",
		profile.Name, credentials.SubscriptionID(), *listen, webhook.Endpoint, runnerConfig.StatePath)
	if err := runnerServeRun(serveContext, service); err != nil && !errors.Is(err, context.Canceled) {
		a.errorf("runner serve: %v\n", err)
		return 1
	}
	return 0
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
