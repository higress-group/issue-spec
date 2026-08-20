package commands

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/commentrunner/credentials"
	runnerrepository "github.com/higress-group/issue-spec/internal/commentrunner/repository"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

var runnerProfileScopes = []string{"read:user", "issues:read", "issues:write"}

// runnerProfileCapabilityProbe revalidates the stable profile PAT at the job
// boundary. Native context proves the PAT kind, required scopes and access to
// the job repository; the compatibility API proves the current repository role.
type runnerProfileCapabilityProbe struct {
	native        github.NativeContextOperations
	compatibility github.AgentCapabilityBackend
	runnerLogin   string
	registry      runnerrepository.Registry
	repositories  map[string]models.RepoScope
}

func (p runnerProfileCapabilityProbe) ProbeProfileCredential(ctx context.Context, request credentials.PreflightRequest) capability.Report {
	req := request.Request
	repository, scope, configured, resolveErr := p.configuredRepository(ctx, req.Repository)
	if p.native == nil || p.compatibility == nil || !configured || scope.Validate() != nil || request.Repo != scope ||
		strings.TrimSpace(p.runnerLogin) == "" {
		if resolveErr != nil {
			return runnerProfileProbeError(req, resolveErr, "runner profile repository authority probe failed")
		}
		return runnerProfileProbeFailure(req, "unknown", capability.FailureInvalidRequest,
			"runner profile capability probe is not configured for this repository")
	}
	current, err := p.native.GetNativeContext(ctx)
	if err != nil {
		return runnerProfileProbeError(req, err, "runner profile native context probe failed")
	}
	if !strings.EqualFold(strings.TrimSpace(current.Credential.Kind), "pat") ||
		strings.TrimSpace(current.User.ID) == "" ||
		!strings.EqualFold(strings.TrimSpace(current.User.Login), strings.TrimSpace(p.runnerLogin)) {
		return runnerProfileProbeFailure(req, "reachable", capability.FailureAuthenticationFailed,
			"runner profile PAT does not authenticate as the configured identity")
	}
	if !includesRequiredScopes(current.Credential.Scopes, runnerProfileScopes) {
		return runnerProfileProbeFailure(req, "reachable", capability.FailureInsufficientPermission,
			"runner profile PAT does not include the required scopes")
	}
	repositoryAllowed, err := p.repositoryAllowed(ctx, current, repository, scope)
	if err != nil {
		return runnerProfileProbeError(req, err, "runner profile repository access probe failed")
	}
	if !repositoryAllowed {
		return runnerProfileProbeFailure(req, "reachable", capability.FailureInsufficientPermission,
			"runner profile PAT does not grant access to the configured repository")
	}

	user, scopes, err := p.compatibility.GetUser(ctx)
	if err != nil {
		return runnerProfileProbeError(req, err, "runner profile compatibility identity probe failed")
	}
	if !strings.EqualFold(strings.TrimSpace(user.Login), strings.TrimSpace(p.runnerLogin)) {
		return runnerProfileProbeFailure(req, "reachable", capability.FailureAuthenticationFailed,
			"runner profile PAT does not authenticate as the configured identity")
	}
	if !includesRequiredScopes(scopes, runnerProfileScopes) {
		return runnerProfileProbeFailure(req, "reachable", capability.FailureInsufficientPermission,
			"runner profile PAT does not expose the required scopes")
	}
	backend := cachedAgentCapabilityBackend{AgentCapabilityBackend: p.compatibility, user: user, scopes: scopes}
	return github.ProbeAgentCapabilities(ctx, backend, req, github.AgentCapabilityProbeOptions{
		CredentialSource: "private-file", CodeReviewSurface: false,
	})
}

func (p runnerProfileCapabilityProbe) configuredRepository(ctx context.Context, requested string) (string, models.RepoScope, bool, error) {
	if p.registry != nil {
		entry, err := p.registry.ResolveRepository(ctx, requested)
		if err != nil {
			return "", models.RepoScope{}, false, err
		}
		return entry.Repository, entry.Scope, true, nil
	}
	requested = strings.TrimSpace(requested)
	for repository, scope := range p.repositories {
		if strings.EqualFold(strings.TrimSpace(repository), requested) {
			return repository, scope, true, nil
		}
	}
	return "", models.RepoScope{}, false, nil
}

func (p runnerProfileCapabilityProbe) repositoryAllowed(ctx context.Context, current github.NativeContext,
	repository string, scope models.RepoScope) (bool, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repository), "/")
	if !ok {
		return false, nil
	}
	var organizations []github.NativeOrganizationContext
	for _, organization := range current.Organizations {
		if strings.EqualFold(strings.TrimSpace(organization.Name), owner) {
			organizations = append(organizations, organization)
		}
	}
	if len(organizations) != 1 {
		return false, nil
	}
	organization := organizations[0]
	organizationID, err := uuid.Parse(strings.TrimSpace(organization.ID))
	if err != nil || organizationID != scope.OrgID {
		return false, nil
	}
	page, err := p.native.ListNativeContextRepositories(ctx, organizationID.String())
	if err != nil {
		return false, err
	}
	matches := 0
	for _, item := range page.Repositories {
		candidate := item.Repository
		repositoryID, parseErr := uuid.Parse(strings.TrimSpace(candidate.ID))
		if parseErr == nil && repositoryID == scope.RepoID &&
			strings.EqualFold(strings.TrimSpace(candidate.OrganizationID), organizationID.String()) &&
			strings.EqualFold(strings.TrimSpace(candidate.Name), name) {
			matches++
		}
	}
	return matches == 1, nil
}

type cachedAgentCapabilityBackend struct {
	github.AgentCapabilityBackend
	user   github.User
	scopes []string
}

func (b cachedAgentCapabilityBackend) GetUser(context.Context) (github.User, []string, error) {
	return b.user, append([]string(nil), b.scopes...), nil
}

func includesRequiredScopes(actual, expected []string) bool {
	want := make(map[string]bool, len(expected))
	for _, scope := range expected {
		want[strings.ToLower(strings.TrimSpace(scope))] = true
	}
	seen := make(map[string]bool, len(actual))
	for _, scope := range actual {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
	}
	for scope := range want {
		if !seen[scope] {
			return false
		}
	}
	return true
}

func runnerProfileProbeError(request capability.Request, err error, detail string) capability.Report {
	network, code := "reachable", capability.FailureRepositoryUnreachable
	var networkError net.Error
	if errors.As(err, &networkError) {
		network, code = "unreachable", capability.FailureNetworkUnreachable
	}
	var apiError *github.APIError
	if errors.As(err, &apiError) {
		switch apiError.StatusCode {
		case http.StatusUnauthorized:
			code = capability.FailureAuthenticationFailed
		case http.StatusForbidden:
			code = capability.FailureInsufficientPermission
		case http.StatusNotFound:
			code = capability.FailureRepositoryUnreachable
		}
	}
	return runnerProfileProbeFailure(request, network, code, detail)
}

func runnerProfileProbeFailure(request capability.Request, network string, code capability.FailureCode, detail string) capability.Report {
	return capability.FailureReport(request, "private-file", "profile-credential", network,
		capability.DecisionDenied, code, detail)
}

var _ credentials.ProfileCapabilityProbe = runnerProfileCapabilityProbe{}
