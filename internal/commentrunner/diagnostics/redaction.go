package diagnostics

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Redactor handles sensitive data redaction
type Redactor struct {
	// knownTokens holds values that should be redacted
	knownTokens map[string]string
	// patterns holds regex patterns for redaction
	patterns []*redactionPattern
}

// redactionPattern represents a pattern for redaction
type redactionPattern struct {
	regex   *regexp.Regexp
	marker  string
	example string
}

// NewRedactor creates a new redactor with default patterns
func NewRedactor() *Redactor {
	r := &Redactor{
		knownTokens: make(map[string]string),
		patterns:    defaultPatterns(),
	}

	// Add known environment tokens
	r.addKnownEnvTokens()

	return r
}

// defaultPatterns returns the default redaction patterns
func defaultPatterns() []*redactionPattern {
	return []*redactionPattern{
		// GitHub personal access tokens
		{regexp.MustCompile(`ghp_[a-zA-Z0-9]{36}`), "[REDACTED:github-pat]", "ghp_"},
		// GitHub OAuth tokens
		{regexp.MustCompile(`gho_[a-zA-Z0-9]{36}`), "[REDACTED:github-oauth]", "gho_"},
		// GitHub user tokens
		{regexp.MustCompile(`ghu_[a-zA-Z0-9]{36}`), "[REDACTED:github-user]", "ghu_"},
		// GitHub server tokens
		{regexp.MustCompile(`ghs_[a-zA-Z0-9]{36}`), "[REDACTED:github-server]", "ghs_"},
		// GitHub refresh tokens
		{regexp.MustCompile(`ghr_[a-zA-Z0-9]{36}`), "[REDACTED:github-refresh]", "ghr_"},
		// Bearer tokens
		{regexp.MustCompile(`Bearer [a-zA-Z0-9_-]{30,}`), "[REDACTED:bearer]", "Bearer"},
		// Generic token-like strings
		{regexp.MustCompile(`token["\']?\s*[:=]\s*["\']?[a-zA-Z0-9_-]{20,}["\']?`), "[REDACTED:token]", "token"},
		// Generic secret-like strings
		{regexp.MustCompile(`secret["\']?\s*[:=]\s*["\']?[a-zA-Z0-9_-]{20,}["\']?`), "[REDACTED:secret]", "secret"},
		// Generic key-like strings
		{regexp.MustCompile(`key["\']?\s*[:=]\s*["\']?[a-zA-Z0-9_-]{20,}["\']?`), "[REDACTED:key]", "key"},
		// Generic password-like strings
		{regexp.MustCompile(`password["\']?\s*[:=]\s*["\']?[a-zA-Z0-9_-]{10,}["\']?`), "[REDACTED:password]", "password"},
	}
}

// addKnownEnvTokens adds known environment variable tokens to the redactor
func (r *Redactor) addKnownEnvTokens() {
	// GitHub token
	if val := os.Getenv("GITHUB_TOKEN"); val != "" {
		r.knownTokens[val] = "[REDACTED:github-token]"
	}

	// Issue-spec notification token
	if val := os.Getenv("ISSUE_SPEC_NOTIFICATION_TOKEN"); val != "" {
		r.knownTokens[val] = "[REDACTED:notification-token]"
	}

	// Codex tokens
	for _, prefix := range []string{"CODEX", "CLAUDE"} {
		for _, suffix := range []string{"_TOKEN", "_API_KEY", "_SECRET", "_KEY"} {
			key := prefix + suffix
			if val := os.Getenv(key); val != "" {
				r.knownTokens[val] = fmt.Sprintf("[REDACTED:%s]", strings.ToLower(key))
			}
		}
	}

	// Check for env vars with secret-like names
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]
		val := parts[1]

		if r.isSecretEnvName(key) {
			r.knownTokens[val] = fmt.Sprintf("[REDACTED:%s]", strings.ToLower(key))
		}
	}
}

// isSecretEnvName checks if an environment variable name suggests a secret
func (r *Redactor) isSecretEnvName(name string) bool {
	nameUpper := strings.ToUpper(name)
	secretKeywords := []string{"TOKEN", "SECRET", "KEY", "PASSWORD", "AUTH", "CREDENTIAL"}

	for _, keyword := range secretKeywords {
		if strings.Contains(nameUpper, keyword) {
			return true
		}
	}

	return false
}

// AddToken adds a known token value to the redactor
func (r *Redactor) AddToken(value, marker string) {
	r.knownTokens[value] = marker
}

// RedactString redacts sensitive values from a string
func (r *Redactor) RedactString(s string) string {
	result := s

	// Redact known tokens first
	for token, marker := range r.knownTokens {
		if token != "" && strings.Contains(result, token) {
			result = strings.ReplaceAll(result, token, marker)
		}
	}

	// Apply patterns
	for _, pattern := range r.patterns {
		result = pattern.regex.ReplaceAllString(result, pattern.marker)
	}

	return result
}

// RedactBytes redacts sensitive values from bytes
func (r *Redactor) RedactBytes(data []byte) []byte {
	return []byte(r.RedactString(string(data)))
}

// RedactEvent redacts sensitive values from an event
func (r *Redactor) RedactEvent(event Event) Event {
	// Redact message
	event.Message = r.RedactString(event.Message)

	// Redact details
	if event.Details != nil {
		redactedDetails := make(map[string]interface{})
		for key, value := range event.Details {
			redactedDetails[key] = r.redactValue(value)
		}
		event.Details = redactedDetails
	}

	// Mark as redacted if we changed anything
	event.RedactionStatus = "redacted"

	return event
}

// redactValue recursively redacts a value
func (r *Redactor) redactValue(value interface{}) interface{} {
	switch v := value.(type) {
	case string:
		return r.RedactString(v)
	case map[string]interface{}:
		redacted := make(map[string]interface{})
		for key, val := range v {
			// Skip redacting auth config file contents entirely
			if r.isSensitiveKey(key) {
				redacted[key] = "[REDACTED:sensitive-config]"
				continue
			}
			redacted[key] = r.redactValue(val)
		}
		return redacted
	case []interface{}:
		redacted := make([]interface{}, len(v))
		for i, val := range v {
			redacted[i] = r.redactValue(val)
		}
		return redacted
	default:
		return v
	}
}

// isSensitiveKey checks if a key suggests sensitive data
func (r *Redactor) isSensitiveKey(key string) bool {
	sensitiveKeys := []string{
		"token", "secret", "password", "key", "auth",
		"credential", "bearer", "api_key", "apikey",
	}

	lowerKey := strings.ToLower(key)
	for _, sensitive := range sensitiveKeys {
		if strings.Contains(lowerKey, sensitive) {
			return true
		}
	}

	return false
}

// RedactJSON redacts sensitive values from JSON bytes
func (r *Redactor) RedactJSON(data []byte) ([]byte, error) {
	var value interface{}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}

	redacted := r.redactValue(value)
	return json.Marshal(redacted)
}

// SafePath returns a safe version of a path for logging
func (r *Redactor) SafePath(path string) string {
	// Don't expose home directory contents
	if strings.HasPrefix(path, os.Getenv("HOME")) {
		return "~" + strings.TrimPrefix(path, os.Getenv("HOME"))
	}

	// Check for sensitive paths
	sensitivePaths := []string{
		"/.config/gh",
		"/.git-credentials",
		"/.ssh/",
	}

	for _, sensitive := range sensitivePaths {
		if strings.Contains(path, sensitive) {
			return "[REDACTED:sensitive-path]"
		}
	}

	return path
}

// SanitizeEnvVars returns a sanitized copy of environment variables
func (r *Redactor) SanitizeEnvVars(env []string) []string {
	sanitized := make([]string, len(env))
	for i, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 && r.isSecretEnvName(parts[0]) {
			sanitized[i] = parts[0] + "=[REDACTED]"
		} else {
			sanitized[i] = e
		}
	}
	return sanitized
}

// TokenCount returns the number of known tokens being tracked
func (r *Redactor) TokenCount() int {
	return len(r.knownTokens)
}
