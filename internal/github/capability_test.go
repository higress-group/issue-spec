package github

import (
	"context"
	"errors"
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
