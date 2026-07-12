package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/higress-group/issue-spec/internal/capability"
)

type capabilityBackend struct {
	user       User
	scopes     []string
	permission string
	userErr    error
	permErr    error
}

func (b capabilityBackend) BackendInfo() BackendInfo {
	return BackendInfo{Name: "test", Host: "github.com"}
}
func (b capabilityBackend) GetUser(context.Context) (User, []string, error) {
	return b.user, b.scopes, b.userErr
}
func (b capabilityBackend) GetCollaboratorPermission(context.Context, string, string) (CollaboratorPermissionResult, error) {
	return CollaboratorPermissionResult{Permission: CollaboratorPermission{Permission: b.permission}}, b.permErr
}

func TestProbeAgentCapabilitiesAllowsOnlyProvenOperations(t *testing.T) {
	backend := capabilityBackend{user: User{Login: "alice"}, permission: "write", scopes: []string{"repo"}}
	request := capability.Request{Host: "github.com", Repository: "o/r", Operations: []capability.Operation{
		capability.OperationGitPush, capability.OperationIssueRead, capability.OperationPullRequestReviewWrite,
	}}
	report := ProbeAgentCapabilities(t.Context(), backend, request, AgentCapabilityProbeOptions{CredentialSource: "env:SECRET", CodeReviewSurface: true})
	if report.OK || report.Network.Status != "reachable" || report.Credential.SourceClass != "environment" {
		t.Fatalf("report = %+v", report)
	}
	decisions := map[capability.Operation]capability.Decision{}
	for _, result := range report.Operations {
		decisions[result.Operation] = result.Decision
	}
	if decisions[capability.OperationIssueRead] != capability.DecisionAllowed ||
		decisions[capability.OperationPullRequestReviewWrite] != capability.DecisionAllowed ||
		decisions[capability.OperationGitPush] != capability.DecisionUnknown {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestProbeAgentCapabilitiesDoesNotInferWriteFromRepositoryRole(t *testing.T) {
	backend := capabilityBackend{user: User{Login: "alice"}, permission: "admin"}
	request := capability.Request{Host: "github.com", Repository: "o/r", Operations: []capability.Operation{capability.OperationIssueCommentWrite}}
	report := ProbeAgentCapabilities(t.Context(), backend, request, AgentCapabilityProbeOptions{CodeReviewSurface: true})
	if len(report.Operations) != 1 || report.Operations[0].Decision != capability.DecisionUnknown ||
		report.Operations[0].Code != capability.FailureOperationNotProvable {
		t.Fatalf("report = %+v", report)
	}
}

func TestNativeGHCapabilityWriteProofMatrix(t *testing.T) {
	tests := []struct {
		name       string
		permission string
		scopes     string
		want       capability.Decision
		wantCode   capability.FailureCode
	}{
		{name: "classic repo scope and write role", permission: "write", scopes: "read:org, repo", want: capability.DecisionAllowed},
		{name: "classic public repo scope and admin role", permission: "admin", scopes: "public_repo", want: capability.DecisionAllowed},
		{name: "read role denies scoped credential", permission: "read", scopes: "repo", want: capability.DecisionDenied, wantCode: capability.FailureInsufficientPermission},
		{name: "missing scope metadata remains unknown", permission: "admin", want: capability.DecisionUnknown, wantCode: capability.FailureOperationNotProvable},
		{name: "unrelated scope remains unknown", permission: "write", scopes: "read:org", want: capability.DecisionUnknown, wantCode: capability.FailureOperationNotProvable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userResponse := fmt.Sprintf("HTTP/2.0 200 OK\nX-OAuth-Scopes: %s\n\n{\"login\":\"alice\"}", tt.scopes)
			permissionResponse := fmt.Sprintf("HTTP/2.0 200 OK\n\n{\"permission\":%q}", tt.permission)
			runner := &sequenceCLIRunner{results: []ExternalCLIResult{{Stdout: []byte(userResponse)}, {Stdout: []byte(permissionResponse)}}}
			backend := newTestGHBackend(t, "github.com", runner)
			report := ProbeAgentCapabilities(t.Context(), backend, capability.Request{Host: "github.com", Repository: "o/r", Operations: []capability.Operation{capability.OperationArtifactWrite}}, AgentCapabilityProbeOptions{CredentialSource: "gh", CodeReviewSurface: true})
			if len(report.Operations) != 1 || report.Operations[0].Decision != tt.want || report.Operations[0].Code != tt.wantCode {
				t.Fatalf("report=%+v", report)
			}
			if len(runner.commands) != 2 {
				t.Fatalf("commands=%d, want read-only identity and permission probes", len(runner.commands))
			}
			for _, command := range runner.commands {
				if command.Method != http.MethodGet {
					t.Fatalf("command=%+v, capability proof must not write", command)
				}
			}
		})
	}
}

func TestProbeAgentCapabilitiesRejectsUnsupportedSelfHostedCodeReview(t *testing.T) {
	backend := capabilityBackend{user: User{Login: "alice"}, permission: "admin", scopes: []string{"repo"}}
	request := capability.Request{Host: "server.internal", Repository: "o/r", Operations: []capability.Operation{capability.OperationPullRequestRead}}
	report := ProbeAgentCapabilities(t.Context(), backend, request, AgentCapabilityProbeOptions{})
	if report.Operations[0].Decision != capability.DecisionUnknown || report.Operations[0].Code != capability.FailureUnsupportedOperationSurface {
		t.Fatalf("report = %+v", report)
	}
}

func TestProbeAgentCapabilitiesReturnsStableAuthFailure(t *testing.T) {
	backend := capabilityBackend{userErr: errors.New("bad auth")}
	request := capability.Request{Host: "github.com", Repository: "o/r", Operations: []capability.Operation{capability.OperationIssueRead}}
	report := ProbeAgentCapabilities(t.Context(), backend, request, AgentCapabilityProbeOptions{})
	if report.OK || report.Operations[0].Code != capability.FailureAuthenticationFailed {
		t.Fatalf("report = %+v", report)
	}
}
