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
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

var runnerProfileScopes = []string{"read:user", "issues:read", "issues:write", "evidence:write"}

// runnerProfileCapabilityProbe revalidates the stable profile PAT at the job
// boundary. Native context proves the PAT kind, exact scope set and exact
// repository cap; the compatibility API proves the current repository role.
type runnerProfileCapabilityProbe struct {
	native        github.NativeContextOperations
	compatibility github.AgentCapabilityBackend
	runnerLogin   string
	repository    string
	scope         models.RepoScope
}

func (p runnerProfileCapabilityProbe) ProbeProfileCredential(ctx context.Context, request credentials.PreflightRequest) capability.Report {
	req := request.Request
	if p.native == nil || p.compatibility == nil || p.scope.Validate() != nil || request.Repo != p.scope ||
		!strings.EqualFold(strings.TrimSpace(req.Repository), strings.TrimSpace(p.repository)) ||
		strings.TrimSpace(p.runnerLogin) == "" {
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
	if !exactScopeSet(current.Credential.Scopes, runnerProfileScopes) {
		return runnerProfileProbeFailure(req, "reachable", capability.FailureInsufficientPermission,
			"runner profile PAT does not have the exact required scope set")
	}
	exactRepository, err := p.exactRepositoryCap(ctx, current)
	if err != nil {
		return runnerProfileProbeError(req, err, "runner profile repository restriction probe failed")
	}
	if !current.Credential.RepositoryRestricted || current.Credential.RepositoryCount != 1 || !exactRepository {
		return runnerProfileProbeFailure(req, "reachable", capability.FailureInsufficientPermission,
			"runner profile PAT is not restricted to the exact configured repository")
	}

	user, scopes, err := p.compatibility.GetUser(ctx)
	if err != nil {
		return runnerProfileProbeError(req, err, "runner profile compatibility identity probe failed")
	}
	if !strings.EqualFold(strings.TrimSpace(user.Login), strings.TrimSpace(p.runnerLogin)) {
		return runnerProfileProbeFailure(req, "reachable", capability.FailureAuthenticationFailed,
			"runner profile PAT does not authenticate as the configured identity")
	}
	if !exactScopeSet(scopes, runnerProfileScopes) {
		return runnerProfileProbeFailure(req, "reachable", capability.FailureInsufficientPermission,
			"runner profile PAT does not expose the exact required scope set")
	}
	backend := cachedAgentCapabilityBackend{AgentCapabilityBackend: p.compatibility, user: user, scopes: scopes}
	return github.ProbeAgentCapabilities(ctx, backend, req, github.AgentCapabilityProbeOptions{
		CredentialSource: "private-file", CodeReviewSurface: false,
	})
}

func (p runnerProfileCapabilityProbe) exactRepositoryCap(ctx context.Context, current github.NativeContext) (bool, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(p.repository), "/")
	if !ok || len(current.Organizations) != 1 {
		return false, nil
	}
	organization := current.Organizations[0]
	organizationID, err := uuid.Parse(strings.TrimSpace(organization.ID))
	if err != nil || organizationID != p.scope.OrgID || !strings.EqualFold(strings.TrimSpace(organization.Name), owner) {
		return false, nil
	}
	page, err := p.native.ListNativeContextRepositories(ctx, organizationID.String())
	if err != nil {
		return false, err
	}
	if len(page.Repositories) != 1 {
		return false, nil
	}
	repository := page.Repositories[0].Repository
	repositoryID, err := uuid.Parse(strings.TrimSpace(repository.ID))
	return err == nil && repositoryID == p.scope.RepoID &&
		strings.EqualFold(strings.TrimSpace(repository.OrganizationID), organizationID.String()) &&
		strings.EqualFold(strings.TrimSpace(repository.Name), name), nil
}

type cachedAgentCapabilityBackend struct {
	github.AgentCapabilityBackend
	user   github.User
	scopes []string
}

func (b cachedAgentCapabilityBackend) GetUser(context.Context) (github.User, []string, error) {
	return b.user, append([]string(nil), b.scopes...), nil
}

func exactScopeSet(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	want := make(map[string]bool, len(expected))
	for _, scope := range expected {
		want[strings.ToLower(strings.TrimSpace(scope))] = true
	}
	seen := make(map[string]bool, len(actual))
	for _, scope := range actual {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope == "" || seen[scope] || !want[scope] {
			return false
		}
		seen[scope] = true
	}
	return len(seen) == len(want)
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
