// Package capability defines provider-neutral operations and redacted
// capability-preflight results. It deliberately contains no credential
// material and no provider-specific permission vocabulary.
package capability

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type Operation string

const (
	OperationIssueRead              Operation = "issue.read"
	OperationIssueCommentWrite      Operation = "issue.comment.write"
	OperationArtifactWrite          Operation = "artifact.write"
	OperationPullRequestRead        Operation = "pr.read"
	OperationPullRequestReviewWrite Operation = "pr.review.write"
	OperationPullRequestUpdate      Operation = "pr.update"
	OperationChecksRead             Operation = "checks.read"
	OperationGitClone               Operation = "git.clone"
	OperationGitPush                Operation = "git.push"
	OperationExternalChangeComment  Operation = "external.change.comment"
)

var knownOperations = map[Operation]bool{
	OperationIssueRead: true, OperationIssueCommentWrite: true, OperationArtifactWrite: true,
	OperationPullRequestRead: true, OperationPullRequestReviewWrite: true, OperationPullRequestUpdate: true,
	OperationChecksRead: true, OperationGitClone: true, OperationGitPush: true,
	OperationExternalChangeComment: true,
}

func ParseOperation(value string) (Operation, error) {
	op := Operation(strings.ToLower(strings.TrimSpace(value)))
	if !knownOperations[op] {
		return "", fmt.Errorf("unsupported agent operation %q", value)
	}
	return op, nil
}

func KnownOperations() []Operation {
	result := make([]Operation, 0, len(knownOperations))
	for operation := range knownOperations {
		result = append(result, operation)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

type Request struct {
	Host       string      `json:"host"`
	Repository string      `json:"repository"`
	Operations []Operation `json:"operations"`
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.Host) == "" || strings.TrimSpace(r.Repository) == "" || len(r.Operations) == 0 {
		return fmt.Errorf("host, repository, and at least one operation are required")
	}
	owner, repository, ok := strings.Cut(strings.TrimSpace(r.Repository), "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(repository) == "" || strings.Contains(repository, "/") {
		return fmt.Errorf("repository must be owner/name")
	}
	for _, operation := range r.Operations {
		if !knownOperations[operation] {
			return fmt.Errorf("unsupported agent operation %q", operation)
		}
	}
	return nil
}

type Decision string

const (
	DecisionAllowed Decision = "allowed"
	DecisionDenied  Decision = "denied"
	DecisionUnknown Decision = "unknown"
)

type FailureCode string

const (
	FailureInvalidRequest              FailureCode = "invalid_request"
	FailureAuthenticationFailed        FailureCode = "authentication_failed"
	FailureNetworkUnreachable          FailureCode = "network_unreachable"
	FailureRepositoryUnreachable       FailureCode = "repository_unreachable"
	FailureInsufficientPermission      FailureCode = "insufficient_repository_permission"
	FailureOperationNotProvable        FailureCode = "operation_not_provable"
	FailureUnsupportedOperationSurface FailureCode = "unsupported_operation_surface"
)

type OperationResult struct {
	Operation Operation   `json:"operation"`
	Decision  Decision    `json:"decision"`
	Code      FailureCode `json:"code,omitempty"`
	Detail    string      `json:"detail,omitempty"`
}

type CredentialSummary struct {
	SourceClass string     `json:"source_class"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	ExpiryKnown bool       `json:"expiry_known"`
}

type NetworkSummary struct {
	Status string `json:"status"`
}

type Report struct {
	OK         bool              `json:"ok"`
	Host       string            `json:"host"`
	Repository string            `json:"repository"`
	Backend    string            `json:"backend,omitempty"`
	Credential CredentialSummary `json:"credential"`
	Network    NetworkSummary    `json:"network"`
	Operations []OperationResult `json:"operations"`
}

func (r *Report) Finish() {
	r.OK = len(r.Operations) > 0
	for _, result := range r.Operations {
		if result.Decision != DecisionAllowed {
			r.OK = false
			return
		}
	}
}

// FailureReport builds a bounded, credential-free report for failures that
// happen before a provider probe can run. Callers supply only stable details.
func FailureReport(request Request, sourceClass, backend, network string, decision Decision, code FailureCode, detail string) Report {
	report := Report{Host: strings.TrimSpace(request.Host), Repository: strings.TrimSpace(request.Repository), Backend: strings.TrimSpace(backend),
		Credential: CredentialSummary{SourceClass: SourceClass(sourceClass)}, Network: NetworkSummary{Status: strings.TrimSpace(network)}}
	if report.Network.Status == "" {
		report.Network.Status = "unknown"
	}
	seen := map[Operation]bool{}
	for _, operation := range request.Operations {
		if operation == "" || seen[operation] {
			continue
		}
		seen[operation] = true
		report.Operations = append(report.Operations, OperationResult{Operation: operation, Decision: decision, Code: code, Detail: detail})
	}
	sort.Slice(report.Operations, func(i, j int) bool { return report.Operations[i].Operation < report.Operations[j].Operation })
	report.Finish()
	return report
}

// SourceClass reduces implementation-specific token sources to a stable,
// redacted category. It never returns a credential path or value.
func SourceClass(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	switch {
	case strings.HasPrefix(source, "env:"):
		return "environment"
	case strings.Contains(source, "file"):
		return "private-file"
	case source == "keyring":
		return "keyring"
	case source == "config":
		return "config"
	case source == "gh":
		return "external-cli"
	case strings.Contains(source, "delegat"):
		return "delegated"
	default:
		return "unknown"
	}
}
