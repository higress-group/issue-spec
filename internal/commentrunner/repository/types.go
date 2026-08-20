// Package repository resolves trusted, credential-free source repository
// bindings for the comment runner. It never derives clone coordinates from an
// issue host, API URL, repository slug, comment or webhook payload.
package repository

import (
	"errors"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/server/models"
)

const (
	SourceOperator = "operator"
	SourceServer   = "server"

	DiagnosticBindingDrift = "repository_binding_drift"
	DiagnosticLegacyState  = "repository_binding_legacy_state"
	DiagnosticNoBinding    = "repository_binding_unavailable"
)

var ErrNoBinding = errors.New("trusted repository binding unavailable")

type Resolution struct {
	Repo          string                          `json:"repo"`
	Scope         models.RepoScope                `json:"scope"`
	CloneURL      string                          `json:"clone_url"`
	DefaultBranch string                          `json:"default_branch"`
	Ref           string                          `json:"ref"`
	Binding       state.RepositoryBindingSnapshot `json:"binding"`
	Diagnostic    string                          `json:"diagnostic"`
}

type DiagnosticError struct {
	Code string
	Err  error
}

func (e *DiagnosticError) Error() string {
	if e == nil {
		return "repository binding failed"
	}
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

func (e *DiagnosticError) Unwrap() error { return e.Err }

func LegacyStateError() error {
	return &DiagnosticError{Code: DiagnosticLegacyState, Err: errors.New("stored session predates repository binding pinning; start a new session")}
}

func DriftError() error {
	return &DiagnosticError{Code: DiagnosticBindingDrift, Err: errors.New("repository binding changed or is no longer available; start a new session")}
}

func NoBindingError() error {
	return &DiagnosticError{Code: DiagnosticNoBinding, Err: ErrNoBinding}
}

func DiagnosticCode(err error) string {
	var diagnostic *DiagnosticError
	if errors.As(err, &diagnostic) {
		return diagnostic.Code
	}
	return ""
}

func ValidatePinned(pinned, current state.RepositoryBindingSnapshot) error {
	if !pinned.Complete() {
		return LegacyStateError()
	}
	if !current.Complete() || !pinned.Equal(current) {
		return DriftError()
	}
	return nil
}

func NormalizeKey(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 512 {
		return "", fmt.Errorf("repository mapping key is required")
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || strings.ContainsRune("._:/-", char) {
			continue
		}
		return "", fmt.Errorf("repository mapping key contains unsupported characters")
	}
	return value, nil
}
