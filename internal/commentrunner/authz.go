package commentrunner

import (
	"context"
	"strings"

	"github.com/higress-group/issue-spec/internal/github"
)

type PermissionBackend interface {
	GetUser(context.Context) (github.User, []string, error)
	GetCollaboratorPermission(context.Context, string, string) (github.CollaboratorPermissionResult, error)
}

type AuthorizationPolicy struct {
	RunnerLogin            string
	AllowedUsers           []string
	AllowAuthenticatedUser bool
}

func DefaultAuthorizationPolicy() AuthorizationPolicy {
	return AuthorizationPolicy{AllowAuthenticatedUser: true}
}

type AuthorizationRequest struct {
	Repo            string
	Commenter       string
	Verb            CommandVerb
	PublicSessionID string
}

type AuthorizationReason string

const (
	AuthReasonAllowed                    AuthorizationReason = "allowed"
	AuthReasonInvalidRequest             AuthorizationReason = "invalid_request"
	AuthReasonRunnerIdentityLookupFailed AuthorizationReason = "runner_identity_lookup_failed"
	AuthReasonRunnerIdentityMismatch     AuthorizationReason = "runner_identity_mismatch"
	AuthReasonCommenterNotAllowed        AuthorizationReason = "commenter_not_allowed"
	AuthReasonPermissionLookupFailed     AuthorizationReason = "permission_lookup_failed"
	AuthReasonInsufficientPermission     AuthorizationReason = "insufficient_permission"
)

type AuthorizationResult struct {
	Allowed     bool                `json:"allowed"`
	Reason      AuthorizationReason `json:"reason"`
	Message     string              `json:"message"`
	Repo        string              `json:"repo,omitempty"`
	Commenter   string              `json:"commenter,omitempty"`
	RunnerLogin string              `json:"runner_login,omitempty"`
	Permission  string              `json:"permission,omitempty"`
}

func AuthorizeCandidate(ctx context.Context, backend PermissionBackend, candidate CommandCandidate, policy AuthorizationPolicy) AuthorizationResult {
	return AuthorizeRequest(ctx, backend, AuthorizationRequest{
		Repo:            candidate.Repo,
		Commenter:       candidate.Commenter,
		Verb:            candidate.Verb,
		PublicSessionID: candidate.PublicSessionID,
	}, policy)
}

func AuthorizeCandidateForRepo(ctx context.Context, backend PermissionBackend, candidate CommandCandidate, repo string, policy AuthorizationPolicy) AuthorizationResult {
	return AuthorizeRequest(ctx, backend, AuthorizationRequest{
		Repo:            repo,
		Commenter:       candidate.Commenter,
		Verb:            candidate.Verb,
		PublicSessionID: candidate.PublicSessionID,
	}, policy)
}

func AuthorizeRequest(ctx context.Context, backend PermissionBackend, req AuthorizationRequest, policy AuthorizationPolicy) AuthorizationResult {
	req.Repo = strings.TrimSpace(req.Repo)
	req.Commenter = strings.TrimSpace(req.Commenter)
	if backend == nil || req.Repo == "" || req.Commenter == "" {
		return deny(AuthReasonInvalidRequest, "authorization requires backend, repo, and commenter", req, "", "")
	}

	allowedUsers := loginSet(policy.AllowedUsers)
	allowAuthenticatedUser := policy.AllowAuthenticatedUser || len(allowedUsers) == 0
	runnerLogin := ""
	if allowAuthenticatedUser || strings.TrimSpace(policy.RunnerLogin) != "" {
		user, _, err := backend.GetUser(ctx)
		if err != nil {
			return deny(AuthReasonRunnerIdentityLookupFailed, "runner identity lookup failed", req, "", "")
		}
		runnerLogin = strings.TrimSpace(user.Login)
		if runnerLogin == "" {
			return deny(AuthReasonRunnerIdentityLookupFailed, "runner identity lookup returned an empty login", req, "", "")
		}
		if expected := strings.TrimSpace(policy.RunnerLogin); expected != "" && !sameLogin(expected, runnerLogin) {
			return deny(AuthReasonRunnerIdentityMismatch, "authenticated runner identity does not match configuration", req, runnerLogin, "")
		}
		if allowAuthenticatedUser {
			allowedUsers[strings.ToLower(runnerLogin)] = true
		}
	}

	if !allowedUsers[strings.ToLower(req.Commenter)] {
		return deny(AuthReasonCommenterNotAllowed, "commenter is not allowed by runner policy", req, runnerLogin, "")
	}

	permissionResult, err := backend.GetCollaboratorPermission(ctx, req.Repo, req.Commenter)
	if err != nil {
		return deny(AuthReasonPermissionLookupFailed, "repository permission lookup failed", req, runnerLogin, "")
	}
	perm := strings.ToLower(strings.TrimSpace(permissionResult.Permission.Permission))
	if !writeEquivalentPermission(perm) {
		return deny(AuthReasonInsufficientPermission, "commenter does not have write-equivalent repository permission", req, runnerLogin, perm)
	}

	return AuthorizationResult{
		Allowed:     true,
		Reason:      AuthReasonAllowed,
		Message:     "authorized",
		Repo:        req.Repo,
		Commenter:   req.Commenter,
		RunnerLogin: runnerLogin,
		Permission:  perm,
	}
}

func loginSet(logins []string) map[string]bool {
	out := map[string]bool{}
	for _, login := range logins {
		if trimmed := strings.TrimSpace(login); trimmed != "" {
			out[strings.ToLower(trimmed)] = true
		}
	}
	return out
}

func sameLogin(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func writeEquivalentPermission(permission string) bool {
	switch strings.ToLower(strings.TrimSpace(permission)) {
	case "write", "maintain", "admin":
		return true
	default:
		return false
	}
}

func deny(reason AuthorizationReason, message string, req AuthorizationRequest, runnerLogin, permission string) AuthorizationResult {
	return AuthorizationResult{
		Allowed:     false,
		Reason:      reason,
		Message:     message,
		Repo:        req.Repo,
		Commenter:   req.Commenter,
		RunnerLogin: runnerLogin,
		Permission:  permission,
	}
}
