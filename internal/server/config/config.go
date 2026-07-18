// Package config loads and validates issue-spec server configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	EnvironmentEnv             = "ENVIRONMENT"
	ListenAddrEnv              = "LISTEN_ADDR"
	DatabaseURLEnv             = "DATABASE_URL"
	APIPublicURLEnv            = "API_PUBLIC_URL"
	WebPublicURLEnv            = "WEB_PUBLIC_URL"
	TrustedProxiesEnv          = "TRUSTED_PROXIES"
	BootstrapSecretFileEnv     = "BOOTSTRAP_SECRET_FILE"
	TokenPepperFileEnv         = "TOKEN_PEPPER_FILE"
	EncryptionKeyFileEnv       = "ENCRYPTION_KEY_FILE"
	AuthProvidersFileEnv       = "AUTH_PROVIDERS_FILE"
	WebhookKeysFileEnv         = "WEBHOOK_ENCRYPTION_KEYS_FILE"
	SMTPConfigFileEnv          = "SMTP_CONFIG_FILE"
	MigrationsModeEnv          = "MIGRATIONS_MODE"
	GracefulShutdownTimeoutEnv = "GRACEFUL_SHUTDOWN_TIMEOUT"
	HealthReadTimeoutEnv       = "HEALTH_READ_TIMEOUT"
	HealthWriteTimeoutEnv      = "HEALTH_WRITE_TIMEOUT"
	StaticDirectoryEnv         = "STATIC_DIRECTORY"
	WebhookAllowedPrivateEnv   = "WEBHOOK_ALLOWED_PRIVATE_CIDRS"
	DeliveryConcurrencyEnv     = "DELIVERY_CONCURRENCY"
	DeliveryLeaseDurationEnv   = "DELIVERY_LEASE_DURATION"
	DeliveryPollIntervalEnv    = "DELIVERY_POLL_INTERVAL"
	DelegationAudienceEnv      = "DELEGATION_AUDIENCE"
	DelegationSubjectEnv       = "DELEGATION_SUBJECT"
	TransportPostureEnv        = "TRANSPORT_POSTURE"
	SearchModeEnv              = "SEARCH_MODE"

	DefaultGracefulShutdownTimeout = 30 * time.Second
	DefaultHealthReadTimeout       = 5 * time.Second
	DefaultHealthWriteTimeout      = 5 * time.Second
	DefaultDeliveryLeaseDuration   = 30 * time.Second
	DefaultDeliveryPollInterval    = 100 * time.Millisecond
	DefaultDeliveryConcurrency     = 8
	DefaultDelegationAudience      = "issue-spec-api"
	DefaultDelegationSubject       = "issue-spec-runner"
	MaxSecretFileBytes             = 64 << 10
)

type Environment string

const (
	EnvironmentDevelopment Environment = "development"
	EnvironmentTest        Environment = "test"
	EnvironmentProduction  Environment = "production"
)

type MigrationsMode string

// SearchMode controls the optional PostgreSQL-backed issue search surface.
// Extensions remain an operator responsibility; selecting postgres makes the
// server validate them and reconcile its own indexes before accepting traffic.
type SearchMode string

type TransportPosture string

const (
	TransportHTTPS               TransportPosture = "https"
	TransportTrustedInternalHTTP TransportPosture = "trusted-internal-http"
)

const (
	MigrationsAuto     MigrationsMode = "auto"
	MigrationsValidate MigrationsMode = "validate"
	MigrationsOff      MigrationsMode = "off"
)

const (
	SearchDisabled SearchMode = "disabled"
	SearchPostgres SearchMode = "postgres"
)

// SecretFile holds a secure file reference and its loaded value. Its value is
// deliberately unexported and excluded from JSON and formatted output.
type SecretFile struct {
	path  string
	value []byte
}

func (s SecretFile) Path() string { return s.path }

// Bytes returns a copy so callers cannot mutate the configured value.
func (s SecretFile) Bytes() []byte { return bytes.Clone(s.value) }

func (s SecretFile) IsZero() bool { return s.path == "" }

func (s SecretFile) String() string {
	if s.path == "" {
		return "<unset>"
	}
	return "<redacted>"
}

func (s SecretFile) MarshalJSON() ([]byte, error) {
	if s.path == "" {
		return []byte("null"), nil
	}
	return json.Marshal(struct {
		File string `json:"file"`
	}{File: s.path})
}

// Config is safe to serialize: database credentials and secret values are
// intentionally omitted, while secret file references remain observable.
type Config struct {
	Environment             Environment      `json:"environment"`
	TransportPosture        TransportPosture `json:"transport_posture"`
	ListenAddr              string           `json:"listen_addr"`
	DatabaseURL             string           `json:"-"`
	APIPublicURL            string           `json:"api_public_url,omitempty"`
	WebPublicURL            string           `json:"web_public_url,omitempty"`
	TrustedProxies          []netip.Prefix   `json:"trusted_proxies,omitempty"`
	BootstrapSecret         SecretFile       `json:"bootstrap_secret_file,omitempty"`
	TokenPepper             SecretFile       `json:"token_pepper_file,omitempty"`
	EncryptionKey           SecretFile       `json:"encryption_key_file,omitempty"`
	AuthProviders           SecretFile       `json:"auth_providers_file,omitempty"`
	WebhookKeys             SecretFile       `json:"webhook_encryption_keys_file,omitempty"`
	SMTPConfig              SecretFile       `json:"smtp_config_file,omitempty"`
	MigrationsMode          MigrationsMode   `json:"migrations_mode"`
	SearchMode              SearchMode       `json:"search_mode"`
	GracefulShutdownTimeout time.Duration    `json:"graceful_shutdown_timeout"`
	HealthReadTimeout       time.Duration    `json:"health_read_timeout"`
	HealthWriteTimeout      time.Duration    `json:"health_write_timeout"`
	StaticDirectory         string           `json:"static_directory,omitempty"`
	WebhookAllowedPrivate   []netip.Prefix   `json:"webhook_allowed_private_cidrs,omitempty"`
	DeliveryConcurrency     int              `json:"delivery_concurrency"`
	DeliveryLeaseDuration   time.Duration    `json:"delivery_lease_duration"`
	DeliveryPollInterval    time.Duration    `json:"delivery_poll_interval"`
	DelegationAudience      string           `json:"delegation_audience"`
	DelegationSubject       string           `json:"delegation_subject"`
}

// MailEnabled reports operator capability only. Product controls must still
// apply their authenticated user and repository eligibility checks.
func (c Config) MailEnabled() bool { return !c.SMTPConfig.IsZero() }

func (c Config) MailSettings() (MailSettings, error) { return ParseMailSettings(c.SMTPConfig) }

func (c Config) String() string {
	b, err := json.Marshal(c)
	if err != nil {
		return "<invalid redacted config>"
	}
	return string(b)
}

func (c Config) GoString() string { return c.String() }

// Load reads configuration from the process environment, loads referenced
// secret files, and returns only a fully validated Config.
func Load() (Config, error) {
	cfg := Config{
		Environment:             EnvironmentDevelopment,
		TransportPosture:        TransportHTTPS,
		MigrationsMode:          MigrationsAuto,
		SearchMode:              SearchDisabled,
		GracefulShutdownTimeout: DefaultGracefulShutdownTimeout,
		HealthReadTimeout:       DefaultHealthReadTimeout,
		HealthWriteTimeout:      DefaultHealthWriteTimeout,
		DeliveryConcurrency:     DefaultDeliveryConcurrency,
		DeliveryLeaseDuration:   DefaultDeliveryLeaseDuration,
		DeliveryPollInterval:    DefaultDeliveryPollInterval,
		DelegationAudience:      DefaultDelegationAudience,
		DelegationSubject:       DefaultDelegationSubject,
	}

	if value := env(EnvironmentEnv); value != "" {
		cfg.Environment = Environment(strings.ToLower(value))
	}
	if value := env(TransportPostureEnv); value != "" {
		cfg.TransportPosture = TransportPosture(strings.ToLower(value))
	}
	cfg.ListenAddr = env(ListenAddrEnv)
	cfg.DatabaseURL = env(DatabaseURLEnv)
	cfg.APIPublicURL = env(APIPublicURLEnv)
	cfg.WebPublicURL = env(WebPublicURLEnv)
	cfg.StaticDirectory = env(StaticDirectoryEnv)
	if value := env(DelegationAudienceEnv); value != "" {
		cfg.DelegationAudience = value
	}
	if value := env(DelegationSubjectEnv); value != "" {
		cfg.DelegationSubject = value
	}
	if value := env(MigrationsModeEnv); value != "" {
		cfg.MigrationsMode = MigrationsMode(strings.ToLower(value))
	}
	if value := env(SearchModeEnv); value != "" {
		cfg.SearchMode = SearchMode(strings.ToLower(value))
	}

	var err error
	if cfg.TrustedProxies, err = parseTrustedProxies(env(TrustedProxiesEnv)); err != nil {
		return Config{}, err
	}
	if cfg.WebhookAllowedPrivate, err = parsePrefixes(WebhookAllowedPrivateEnv, env(WebhookAllowedPrivateEnv)); err != nil {
		return Config{}, err
	}
	if cfg.GracefulShutdownTimeout, err = parseDuration(GracefulShutdownTimeoutEnv, cfg.GracefulShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.HealthReadTimeout, err = parseDuration(HealthReadTimeoutEnv, cfg.HealthReadTimeout); err != nil {
		return Config{}, err
	}
	if cfg.HealthWriteTimeout, err = parseDuration(HealthWriteTimeoutEnv, cfg.HealthWriteTimeout); err != nil {
		return Config{}, err
	}
	if cfg.DeliveryLeaseDuration, err = parseDuration(DeliveryLeaseDurationEnv, cfg.DeliveryLeaseDuration); err != nil {
		return Config{}, err
	}
	if cfg.DeliveryPollInterval, err = parseDuration(DeliveryPollIntervalEnv, cfg.DeliveryPollInterval); err != nil {
		return Config{}, err
	}
	if value := env(DeliveryConcurrencyEnv); value != "" {
		cfg.DeliveryConcurrency, err = strconv.Atoi(value)
		if err != nil || cfg.DeliveryConcurrency < 1 || cfg.DeliveryConcurrency > 256 {
			return Config{}, fmt.Errorf("%s must be between 1 and 256", DeliveryConcurrencyEnv)
		}
	}
	if cfg.BootstrapSecret, err = loadSecretFile(BootstrapSecretFileEnv); err != nil {
		return Config{}, err
	}
	if cfg.TokenPepper, err = loadSecretFile(TokenPepperFileEnv); err != nil {
		return Config{}, err
	}
	if cfg.EncryptionKey, err = loadSecretFile(EncryptionKeyFileEnv); err != nil {
		return Config{}, err
	}
	if cfg.AuthProviders, err = loadSecretFile(AuthProvidersFileEnv); err != nil {
		return Config{}, err
	}
	if cfg.WebhookKeys, err = loadSecretFile(WebhookKeysFileEnv); err != nil {
		return Config{}, err
	}
	if cfg.SMTPConfig, err = loadSecretFile(SMTPConfigFileEnv); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	switch c.Environment {
	case EnvironmentDevelopment, EnvironmentTest, EnvironmentProduction:
	default:
		return fmt.Errorf("%s must be development, test, or production", EnvironmentEnv)
	}
	if c.TransportPosture != TransportHTTPS && c.TransportPosture != TransportTrustedInternalHTTP {
		return fmt.Errorf("%s must be https or trusted-internal-http", TransportPostureEnv)
	}
	if c.SearchMode != SearchDisabled && c.SearchMode != SearchPostgres {
		return fmt.Errorf("%s must be disabled or postgres", SearchModeEnv)
	}
	if err := validateListenAddr(c.ListenAddr); err != nil {
		return err
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("%s is required", DatabaseURLEnv)
	}
	if err := validatePublicURL(APIPublicURLEnv, c.APIPublicURL); err != nil {
		return err
	}
	if err := validatePublicURL(WebPublicURLEnv, c.WebPublicURL); err != nil {
		return err
	}
	if c.Environment != EnvironmentProduction && c.APIPublicURL != "" && c.WebPublicURL != "" {
		apiURL, _ := url.Parse(c.APIPublicURL)
		webURL, _ := url.Parse(c.WebPublicURL)
		if apiURL.Scheme != webURL.Scheme {
			return fmt.Errorf("%s and %s must use the same scheme", APIPublicURLEnv, WebPublicURLEnv)
		}
	}
	switch c.MigrationsMode {
	case MigrationsAuto, MigrationsValidate, MigrationsOff:
	default:
		return fmt.Errorf("%s must be auto, validate, or off", MigrationsModeEnv)
	}
	if c.GracefulShutdownTimeout <= 0 {
		return fmt.Errorf("%s must be positive", GracefulShutdownTimeoutEnv)
	}
	if c.HealthReadTimeout <= 0 {
		return fmt.Errorf("%s must be positive", HealthReadTimeoutEnv)
	}
	if c.HealthWriteTimeout <= 0 {
		return fmt.Errorf("%s must be positive", HealthWriteTimeoutEnv)
	}
	if c.DeliveryConcurrency < 1 || c.DeliveryConcurrency > 256 || c.DeliveryLeaseDuration <= 0 || c.DeliveryPollInterval <= 0 {
		return fmt.Errorf("delivery worker configuration is invalid")
	}
	if !validBinding(c.DelegationAudience) || !validBinding(c.DelegationSubject) {
		return fmt.Errorf("delegation audience and subject must be printable values of at most 128 bytes")
	}
	if _, err := ParseMailSettings(c.SMTPConfig); err != nil {
		return err
	}
	if c.Environment == EnvironmentProduction {
		if c.StaticDirectory != "" {
			return fmt.Errorf("%s is forbidden in production", StaticDirectoryEnv)
		}
		if c.APIPublicURL == "" {
			return fmt.Errorf("%s is required in production", APIPublicURLEnv)
		}
		if c.WebPublicURL == "" {
			return fmt.Errorf("%s is required in production", WebPublicURLEnv)
		}
		for _, publicURL := range []struct {
			name  string
			value string
		}{
			{APIPublicURLEnv, c.APIPublicURL},
			{WebPublicURLEnv, c.WebPublicURL},
		} {
			parsed, _ := url.Parse(publicURL.value)
			requiredScheme := "https"
			if c.TransportPosture == TransportTrustedInternalHTTP {
				requiredScheme = "http"
			}
			if parsed.Scheme != requiredScheme {
				return fmt.Errorf("%s must use %s in production for %s posture", publicURL.name, requiredScheme, c.TransportPosture)
			}
		}
		for _, required := range []struct {
			name   string
			secret SecretFile
		}{
			{BootstrapSecretFileEnv, c.BootstrapSecret},
			{TokenPepperFileEnv, c.TokenPepper},
			{EncryptionKeyFileEnv, c.EncryptionKey},
		} {
			if required.secret.IsZero() {
				return fmt.Errorf("%s is required in production", required.name)
			}
		}
	}
	if c.APIPublicURL != "" && c.WebPublicURL != "" {
		apiURL, _ := url.Parse(c.APIPublicURL)
		webURL, _ := url.Parse(c.WebPublicURL)
		if apiURL.Scheme != webURL.Scheme {
			return fmt.Errorf("%s and %s must use the same scheme", APIPublicURLEnv, WebPublicURLEnv)
		}
	}
	return nil
}

func env(name string) string { return strings.TrimSpace(os.Getenv(name)) }

func validateListenAddr(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", ListenAddrEnv)
	}
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("%s must be a host:port address", ListenAddrEnv)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%s must contain a port between 1 and 65535", ListenAddrEnv)
	}
	return nil
}

func validatePublicURL(name, value string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Opaque != "" {
		return fmt.Errorf("%s must be an absolute http(s) URL", name)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use http or https", name)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not contain userinfo", name)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("%s must be a root origin without a path", name)
	}
	if parsed.RawPath != "" && parsed.RawPath != "/" {
		return fmt.Errorf("%s must be a root origin without an encoded path", name)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return fmt.Errorf("%s must not contain a query", name)
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return fmt.Errorf("%s must not contain a fragment", name)
	}
	return nil
}

func parseTrustedProxies(value string) ([]netip.Prefix, error) {
	return parsePrefixes(TrustedProxiesEnv, value)
}

func parsePrefixes(name, value string) ([]netip.Prefix, error) {
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	result := make([]netip.Prefix, 0, len(parts))
	seen := make(map[netip.Prefix]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("%s contains an invalid CIDR", name)
		}
		prefix = prefix.Masked()
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		result = append(result, prefix)
	}
	return result, nil
}

func parseDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := env(name)
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return d, nil
}

func loadSecretFile(name string) (SecretFile, error) {
	path := env(name)
	if path == "" {
		return SecretFile{}, nil
	}
	if !filepath.IsAbs(path) {
		return SecretFile{}, fmt.Errorf("%s must reference an absolute path", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return SecretFile{}, fmt.Errorf("read %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return SecretFile{}, fmt.Errorf("%s must not reference a symbolic link", name)
	}
	if !info.Mode().IsRegular() {
		return SecretFile{}, fmt.Errorf("%s must reference a regular file", name)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return SecretFile{}, fmt.Errorf("%s file permissions must not grant group or other access", name)
	}
	file, err := os.Open(path)
	if err != nil {
		return SecretFile{}, fmt.Errorf("read %s: %w", name, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return SecretFile{}, fmt.Errorf("%s changed while it was being opened", name)
	}
	value, err := io.ReadAll(io.LimitReader(file, MaxSecretFileBytes+1))
	if err != nil {
		return SecretFile{}, fmt.Errorf("read %s: %w", name, err)
	}
	if len(value) > MaxSecretFileBytes {
		return SecretFile{}, fmt.Errorf("%s exceeds %d bytes", name, MaxSecretFileBytes)
	}
	value = bytes.TrimRight(value, "\r\n")
	if len(value) == 0 {
		return SecretFile{}, fmt.Errorf("%s must reference a non-empty file", name)
	}
	return SecretFile{path: path, value: value}, nil
}

func validBinding(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char == 0x7f {
			return false
		}
	}
	return true
}

// RedactError removes configured database and secret values from an error
// before it is written to logs. It intentionally returns a new opaque error.
func RedactError(err error) error {
	values := append([]string{env(DatabaseURLEnv)}, secretValuesFromEnvironment()...)
	return redactError(err, values...)
}

// RedactError removes the sensitive values already loaded into this Config.
// Unlike the package helper it remains effective after a mounted secret file
// is rotated or removed.
func (c Config) RedactError(err error) error {
	values := []string{c.DatabaseURL, string(c.BootstrapSecret.value), string(c.TokenPepper.value),
		string(c.EncryptionKey.value), string(c.AuthProviders.value), string(c.WebhookKeys.value), string(c.SMTPConfig.value)}
	values = append(values, mailSensitiveValues(c.SMTPConfig.value)...)
	return redactError(err, values...)
}

func secretValuesFromEnvironment() []string {
	var values []string
	for _, name := range []string{BootstrapSecretFileEnv, TokenPepperFileEnv, EncryptionKeyFileEnv, AuthProvidersFileEnv, WebhookKeysFileEnv, SMTPConfigFileEnv} {
		path := env(name)
		if path == "" {
			continue
		}
		if value, readErr := os.ReadFile(path); readErr == nil {
			values = append(values, string(value), string(bytes.TrimRight(value, "\r\n")))
			if name == SMTPConfigFileEnv {
				values = append(values, mailSensitiveValues(value)...)
			}
		}
	}
	return values
}

func redactError(err error, values ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[REDACTED]")
		}
	}
	return errors.New(message)
}
