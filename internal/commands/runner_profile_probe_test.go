package commands

import (
	"context"
	"net"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/commentrunner/credentials"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestRunnerProfileCapabilityProbeValidatesLivePATForEachConfiguredRepository(t *testing.T) {
	orgID, repoID, otherRepoID := uuid.New(), uuid.New(), uuid.New()
	scope := models.RepoScope{OrgID: orgID, RepoID: repoID}
	otherScope := models.RepoScope{OrgID: orgID, RepoID: otherRepoID}
	native := &fakeRunnerProfileNative{current: github.NativeContext{
		User: github.NativeContextUser{ID: uuid.NewString(), Login: "runner"},
		Credential: github.NativeContextCredential{Kind: "pat", Scopes: append(append([]string(nil), runnerProfileScopes...), "admin:repo"),
			RepositoryRestricted: true, RepositoryCount: 2},
		Organizations: []github.NativeOrganizationContext{{ID: orgID.String(), Name: "owner"}},
	}, page: github.NativeRepositoriesContext{Repositories: []github.NativeRepositoryContext{
		{Repository: github.NativeRepositorySummary{ID: repoID.String(), OrganizationID: orgID.String(), Name: "repo"}},
		{Repository: github.NativeRepositorySummary{ID: otherRepoID.String(), OrganizationID: orgID.String(), Name: "other"}},
	}}}
	compatibility := &fakeRunnerProfileCompatibility{user: github.User{Login: "runner"},
		scopes: append(append([]string(nil), runnerProfileScopes...), "admin:repo"), permission: "write"}
	probe := runnerProfileCapabilityProbe{native: native, compatibility: compatibility, runnerLogin: "runner",
		repositories: map[string]models.RepoScope{"owner/repo": scope, "owner/other": otherScope}}
	report := probe.ProbeProfileCredential(t.Context(), runnerProfileRequest("owner/repo", scope))
	if !report.OK || report.Network.Status != "reachable" || native.currentCalls != 1 || native.repositoryCalls != 1 ||
		compatibility.userCalls != 1 || compatibility.permissionCalls != 1 {
		t.Fatalf("report=%+v native=%+v compatibility=%+v", report, native, compatibility)
	}
	report = probe.ProbeProfileCredential(t.Context(), runnerProfileRequest("owner/other", otherScope))
	if !report.OK || native.currentCalls != 2 || native.repositoryCalls != 2 || compatibility.permissionCalls != 2 {
		t.Fatalf("second repository report=%+v native=%+v compatibility=%+v", report, native, compatibility)
	}
	compatibility.permission = "read"
	report = probe.ProbeProfileCredential(t.Context(), runnerProfileRequest("owner/repo", scope))
	results := make(map[capability.Operation]capability.OperationResult, len(report.Operations))
	for _, result := range report.Operations {
		results[result.Operation] = result
	}
	if report.OK || results[capability.OperationIssueRead].Decision != capability.DecisionAllowed ||
		results[capability.OperationIssueCommentWrite].Decision != capability.DecisionDenied ||
		results[capability.OperationArtifactWrite].Decision != capability.DecisionDenied || compatibility.permissionCalls != 3 {
		t.Fatalf("permission drift report=%+v compatibility=%+v", report, compatibility)
	}
}

func TestRunnerProfileCapabilityProbeFailsClosedOnPATDrift(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	scope := models.RepoScope{OrgID: orgID, RepoID: repoID}
	validContext := github.NativeContext{
		User: github.NativeContextUser{ID: uuid.NewString(), Login: "runner"},
		Credential: github.NativeContextCredential{Kind: "pat", Scopes: append([]string(nil), runnerProfileScopes...),
			RepositoryRestricted: true, RepositoryCount: 1},
		Organizations: []github.NativeOrganizationContext{{ID: orgID.String(), Name: "owner"}},
	}
	validPage := github.NativeRepositoriesContext{Repositories: []github.NativeRepositoryContext{{
		Repository: github.NativeRepositorySummary{ID: repoID.String(), OrganizationID: orgID.String(), Name: "repo"},
	}}}
	tests := []struct {
		name        string
		mutate      func(*fakeRunnerProfileNative, *fakeRunnerProfileCompatibility)
		wantCode    capability.FailureCode
		wantNetwork string
	}{
		{name: "identity changed", mutate: func(native *fakeRunnerProfileNative, _ *fakeRunnerProfileCompatibility) {
			native.current.User.Login = "other"
		}, wantCode: capability.FailureAuthenticationFailed, wantNetwork: "reachable"},
		{name: "scope removed", mutate: func(native *fakeRunnerProfileNative, _ *fakeRunnerProfileCompatibility) {
			native.current.Credential.Scopes = runnerProfileScopes[:len(runnerProfileScopes)-1]
		}, wantCode: capability.FailureInsufficientPermission, wantNetwork: "reachable"},
		{name: "configured repository removed", mutate: func(native *fakeRunnerProfileNative, _ *fakeRunnerProfileCompatibility) {
			native.page.Repositories = []github.NativeRepositoryContext{{Repository: github.NativeRepositorySummary{
				ID: uuid.NewString(), OrganizationID: orgID.String(), Name: "other"}}}
		}, wantCode: capability.FailureInsufficientPermission, wantNetwork: "reachable"},
		{name: "native network unavailable", mutate: func(native *fakeRunnerProfileNative, _ *fakeRunnerProfileCompatibility) {
			native.currentErr = &net.DNSError{Err: "unreachable", Name: "issues.example.test"}
		}, wantCode: capability.FailureNetworkUnreachable, wantNetwork: "unreachable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			native := &fakeRunnerProfileNative{current: validContext, page: validPage}
			compatibility := &fakeRunnerProfileCompatibility{user: github.User{Login: "runner"},
				scopes: append([]string(nil), runnerProfileScopes...), permission: "write"}
			tt.mutate(native, compatibility)
			probe := runnerProfileCapabilityProbe{native: native, compatibility: compatibility, runnerLogin: "runner",
				repositories: map[string]models.RepoScope{"owner/repo": scope}}
			report := probe.ProbeProfileCredential(t.Context(), runnerProfileRequest("owner/repo", scope))
			if report.OK || report.Network.Status != tt.wantNetwork || len(report.Operations) != 3 {
				t.Fatalf("report=%+v", report)
			}
			for _, operation := range report.Operations {
				if operation.Decision != capability.DecisionDenied || operation.Code != tt.wantCode {
					t.Fatalf("operation=%+v report=%+v", operation, report)
				}
			}
		})
	}
}

func runnerProfileRequest(repository string, scope models.RepoScope) credentials.PreflightRequest {
	return credentials.PreflightRequest{Request: capability.Request{Host: "issues.example.test", Repository: repository,
		Operations: []capability.Operation{capability.OperationIssueRead, capability.OperationIssueCommentWrite,
			capability.OperationArtifactWrite}}, Repo: scope, JobID: "job-profile-probe"}
}

type fakeRunnerProfileNative struct {
	current         github.NativeContext
	page            github.NativeRepositoriesContext
	currentErr      error
	repositoryErr   error
	currentCalls    int
	repositoryCalls int
}

func (f *fakeRunnerProfileNative) GetNativeContext(context.Context) (github.NativeContext, error) {
	f.currentCalls++
	return f.current, f.currentErr
}

func (f *fakeRunnerProfileNative) ListNativeContextRepositories(context.Context, string) (github.NativeRepositoriesContext, error) {
	f.repositoryCalls++
	return f.page, f.repositoryErr
}

type fakeRunnerProfileCompatibility struct {
	user            github.User
	scopes          []string
	permission      string
	userErr         error
	permissionErr   error
	userCalls       int
	permissionCalls int
}

func (f *fakeRunnerProfileCompatibility) GetUser(context.Context) (github.User, []string, error) {
	f.userCalls++
	return f.user, append([]string(nil), f.scopes...), f.userErr
}

func (f *fakeRunnerProfileCompatibility) GetCollaboratorPermission(context.Context, string, string) (github.CollaboratorPermissionResult, error) {
	f.permissionCalls++
	return github.CollaboratorPermissionResult{Permission: github.CollaboratorPermission{Permission: f.permission}}, f.permissionErr
}

func (*fakeRunnerProfileCompatibility) BackendInfo() github.BackendInfo {
	return github.BackendInfo{Name: "rest", Kind: "rest", Host: "issues.example.test"}
}
