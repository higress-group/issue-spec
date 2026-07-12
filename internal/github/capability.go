package github

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/capability"
)

// AgentCapabilityBackend is intentionally read-only. Capability preflight
// must never prove a write by creating, editing, or deleting remote state.
type AgentCapabilityBackend interface {
	GetUser(context.Context) (User, []string, error)
	GetCollaboratorPermission(context.Context, string, string) (CollaboratorPermissionResult, error)
	BackendInfo() BackendInfo
}

type AgentCapabilityProbeOptions struct {
	CredentialSource string
	// CodeReviewSurface is true only when the selected issue backend is also
	// authoritative for GitHub pull requests and checks. Self-hosted issue
	// profiles must leave it false even if they implement compatibility stubs.
	CodeReviewSurface bool
}

func ProbeAgentCapabilities(ctx context.Context, backend AgentCapabilityBackend, request capability.Request, options AgentCapabilityProbeOptions) capability.Report {
	report := capability.Report{Host: strings.TrimSpace(request.Host), Repository: strings.TrimSpace(request.Repository),
		Credential: capability.CredentialSummary{SourceClass: capability.SourceClass(options.CredentialSource)},
		Network:    capability.NetworkSummary{Status: "unknown"}}
	if backend != nil {
		report.Backend = backend.BackendInfo().Name
	}
	operations := normalizedCapabilityOperations(request.Operations)
	if err := request.Validate(); err != nil || backend == nil {
		detail := "capability request is invalid"
		if backend == nil {
			detail = "capability backend is unavailable"
		}
		report.Operations = capabilityFailures(operations, capability.DecisionDenied, capability.FailureInvalidRequest, detail)
		report.Finish()
		return report
	}

	user, scopes, err := backend.GetUser(ctx)
	if err != nil || strings.TrimSpace(user.Login) == "" {
		code := capability.FailureAuthenticationFailed
		detail := "authenticated identity probe failed"
		if isNetworkError(err) {
			code, detail = capability.FailureNetworkUnreachable, "backend network probe failed"
		}
		report.Network.Status = "reachable"
		if code == capability.FailureNetworkUnreachable {
			report.Network.Status = "unreachable"
		}
		report.Operations = capabilityFailures(operations, capability.DecisionDenied, code, detail)
		report.Finish()
		return report
	}
	report.Network.Status = "reachable"

	permission, err := backend.GetCollaboratorPermission(ctx, request.Repository, user.Login)
	if err != nil {
		decision, code, detail := capability.DecisionUnknown, capability.FailureRepositoryUnreachable, "repository permission probe failed"
		if isNetworkError(err) {
			report.Network.Status = "unreachable"
			decision, code, detail = capability.DecisionDenied, capability.FailureNetworkUnreachable, "backend network probe failed"
		} else if status := capabilityHTTPStatus(err); status == http.StatusUnauthorized || status == http.StatusForbidden {
			decision, code, detail = capability.DecisionDenied, capability.FailureInsufficientPermission, "credential cannot read repository permission"
		} else if status == http.StatusNotFound {
			decision = capability.DecisionDenied
		}
		report.Operations = capabilityFailures(operations, decision, code, detail)
		report.Finish()
		return report
	}

	role := strings.ToLower(strings.TrimSpace(permission.Permission.Permission))
	canRead := role == "read" || role == "triage" || permissionAllowsWrite(role)
	canWrite := permissionAllowsWrite(role)
	scopeSet := normalizedScopeSet(scopes)
	for _, operation := range operations {
		report.Operations = append(report.Operations, evaluateCapabilityOperation(operation, canRead, canWrite, scopeSet, options))
	}
	report.Finish()
	return report
}

func evaluateCapabilityOperation(operation capability.Operation, canRead, canWrite bool, scopes map[string]bool, options AgentCapabilityProbeOptions) capability.OperationResult {
	result := capability.OperationResult{Operation: operation}
	switch operation {
	case capability.OperationIssueRead:
		if canRead {
			result.Decision = capability.DecisionAllowed
			return result
		}
		return deniedCapability(operation, "repository role does not allow reads")
	case capability.OperationPullRequestRead, capability.OperationChecksRead:
		if !options.CodeReviewSurface {
			return unknownCapability(operation, capability.FailureUnsupportedOperationSurface, "selected issue backend does not prove a code-review surface")
		}
		if canRead {
			result.Decision = capability.DecisionAllowed
			return result
		}
		return deniedCapability(operation, "repository role does not allow reads")
	case capability.OperationIssueCommentWrite, capability.OperationArtifactWrite:
		return evaluateProvenWrite(operation, canWrite, scopeAllowsIssueWrite(scopes))
	case capability.OperationPullRequestReviewWrite, capability.OperationPullRequestUpdate:
		if !options.CodeReviewSurface {
			return unknownCapability(operation, capability.FailureUnsupportedOperationSurface, "selected issue backend does not prove a code-review surface")
		}
		return evaluateProvenWrite(operation, canWrite, scopeAllowsCodeReviewWrite(scopes))
	case capability.OperationGitClone, capability.OperationGitPush, capability.OperationExternalChangeComment:
		return unknownCapability(operation, capability.FailureOperationNotProvable, "issue-backend read probes cannot prove this operation")
	default:
		return unknownCapability(operation, capability.FailureUnsupportedOperationSurface, "operation is not supported by this probe")
	}
}

func evaluateProvenWrite(operation capability.Operation, roleAllows, scopeAllows bool) capability.OperationResult {
	if !roleAllows {
		return deniedCapability(operation, "repository role does not allow writes")
	}
	if !scopeAllows {
		return unknownCapability(operation, capability.FailureOperationNotProvable, "read-only probes found no scope evidence proving this write")
	}
	return capability.OperationResult{Operation: operation, Decision: capability.DecisionAllowed}
}

func deniedCapability(operation capability.Operation, detail string) capability.OperationResult {
	return capability.OperationResult{Operation: operation, Decision: capability.DecisionDenied,
		Code: capability.FailureInsufficientPermission, Detail: detail}
}

func unknownCapability(operation capability.Operation, code capability.FailureCode, detail string) capability.OperationResult {
	return capability.OperationResult{Operation: operation, Decision: capability.DecisionUnknown, Code: code, Detail: detail}
}

func capabilityFailures(operations []capability.Operation, decision capability.Decision, code capability.FailureCode, detail string) []capability.OperationResult {
	results := make([]capability.OperationResult, 0, len(operations))
	for _, operation := range operations {
		results = append(results, capability.OperationResult{Operation: operation, Decision: decision, Code: code, Detail: detail})
	}
	return results
}

func normalizedCapabilityOperations(operations []capability.Operation) []capability.Operation {
	seen := map[capability.Operation]bool{}
	result := make([]capability.Operation, 0, len(operations))
	for _, operation := range operations {
		if operation != "" && !seen[operation] {
			seen[operation] = true
			result = append(result, operation)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func normalizedScopeSet(scopes []string) map[string]bool {
	result := map[string]bool{}
	for _, scope := range scopes {
		if scope = strings.ToLower(strings.TrimSpace(scope)); scope != "" {
			result[scope] = true
		}
	}
	return result
}

func scopeAllowsIssueWrite(scopes map[string]bool) bool {
	return scopes["repo"] || scopes["public_repo"] || scopes["issues:write"]
}

func scopeAllowsCodeReviewWrite(scopes map[string]bool) bool {
	return scopes["repo"] || scopes["public_repo"] || scopes["pull_requests:write"]
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func capabilityHTTPStatus(err error) int {
	var apiError *APIError
	if errors.As(err, &apiError) {
		return apiError.StatusCode
	}
	var runnerError *GHRunnerError
	if errors.As(err, &runnerError) {
		return runnerError.StatusCode
	}
	return 0
}
