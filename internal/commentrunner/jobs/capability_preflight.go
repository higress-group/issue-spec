package jobs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/commentrunner/credentials"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func (d *Dispatcher) preflightRequiredOperations(ctx context.Context, job state.Job) error {
	operations := normalizedRequiredOperations(d.RequiredOperations)
	if len(operations) == 0 {
		return nil
	}
	request := capability.Request{Host: strings.TrimSpace(d.CapabilityHost), Repository: job.Repo, Operations: operations}
	var report capability.Report
	if d.CapabilityPreflight == nil {
		report = capability.FailureReport(request, "", "", "unknown", capability.DecisionDenied,
			capability.FailureOperationNotProvable, "operator credential issuer preflight is not configured")
	} else {
		scope, ok := d.CredentialScopes[job.Repo]
		if !ok || scope.Validate() != nil {
			report = capability.FailureReport(request, "", "", "unknown", capability.DecisionDenied,
				capability.FailureInvalidRequest, "repository credential scope is unavailable")
		} else {
			report = d.CapabilityPreflight.Probe(ctx, credentials.PreflightRequest{Request: request, Repo: scope, JobID: job.ID})
		}
	}
	if !sameCapabilityReportScope(report, request) {
		report = capability.FailureReport(request, "", "", "unknown", capability.DecisionDenied,
			capability.FailureInvalidRequest, "credential issuer returned a mismatched capability report")
	}
	report = redactedCapabilityReport(report)
	if err := d.Store.Update(ctx, func(current *state.RunnerState) error {
		stored, ok := current.Jobs[job.ID]
		if !ok {
			return fmt.Errorf("job %q not found", job.ID)
		}
		copy := report
		stored.CapabilityPreflight = &copy
		return current.UpsertJob(stored)
	}); err != nil {
		return fmt.Errorf("persist redacted capability preflight: %w", err)
	}
	if report.OK {
		return nil
	}
	var failures []string
	for _, result := range report.Operations {
		if result.Decision != capability.DecisionAllowed {
			failures = append(failures, fmt.Sprintf("%s=%s/%s", result.Operation, result.Decision, result.Code))
		}
	}
	return fmt.Errorf("required operation preflight failed: %s", strings.Join(failures, ", "))
}

func redactedCapabilityReport(report capability.Report) capability.Report {
	redacted := capability.Report{OK: report.OK, Host: report.Host, Repository: report.Repository,
		Backend: safeCapabilityLabel(report.Backend), Credential: report.Credential,
		Network: capability.NetworkSummary{Status: safeCapabilityNetwork(report.Network.Status)}}
	redacted.Credential.SourceClass = safeCapabilitySourceClass(report.Credential.SourceClass)
	for _, result := range report.Operations {
		decision, code := safeCapabilityDecision(result.Decision), safeCapabilityFailureCode(result.Code)
		if decision == capability.DecisionDenied && code == "" {
			code = capability.FailureInvalidRequest
		}
		redacted.Operations = append(redacted.Operations, capability.OperationResult{Operation: result.Operation,
			Decision: decision, Code: code})
	}
	redacted.Finish()
	return redacted
}

func safeCapabilityNetwork(value string) string {
	switch strings.TrimSpace(value) {
	case "reachable", "unreachable", "configured", "unknown":
		return strings.TrimSpace(value)
	default:
		return "unknown"
	}
}

func safeCapabilityDecision(value capability.Decision) capability.Decision {
	switch value {
	case capability.DecisionAllowed, capability.DecisionDenied, capability.DecisionUnknown:
		return value
	default:
		return capability.DecisionDenied
	}
}

func safeCapabilityFailureCode(value capability.FailureCode) capability.FailureCode {
	switch value {
	case "", capability.FailureInvalidRequest, capability.FailureAuthenticationFailed,
		capability.FailureNetworkUnreachable, capability.FailureRepositoryUnreachable,
		capability.FailureInsufficientPermission, capability.FailureOperationNotProvable,
		capability.FailureUnsupportedOperationSurface:
		return value
	default:
		return capability.FailureInvalidRequest
	}
}

func safeCapabilityLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return "unknown"
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._-", char) {
			continue
		}
		return "unknown"
	}
	return value
}

func safeCapabilitySourceClass(value string) string {
	switch strings.TrimSpace(value) {
	case "environment", "private-file", "keyring", "config", "external-cli", "delegated", "unknown":
		return strings.TrimSpace(value)
	default:
		return "unknown"
	}
}

func normalizedRequiredOperations(operations []capability.Operation) []capability.Operation {
	seen := map[capability.Operation]bool{}
	var result []capability.Operation
	for _, operation := range operations {
		if operation != "" && !seen[operation] {
			seen[operation] = true
			result = append(result, operation)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func sameCapabilityReportScope(report capability.Report, request capability.Request) bool {
	if !strings.EqualFold(strings.TrimSpace(report.Host), strings.TrimSpace(request.Host)) ||
		strings.TrimSpace(report.Repository) != strings.TrimSpace(request.Repository) {
		return false
	}
	if report.OK {
		if report.Credential.ExpiryKnown {
			if report.Credential.ExpiresAt == nil || !time.Now().UTC().Before(report.Credential.ExpiresAt.UTC()) {
				return false
			}
		} else if report.Credential.SourceClass != "private-file" {
			return false
		}
	}
	want := normalizedRequiredOperations(request.Operations)
	got := make([]capability.Operation, 0, len(report.Operations))
	for _, result := range report.Operations {
		got = append(got, result.Operation)
	}
	if len(got) != len(want) {
		return false
	}
	got = normalizedRequiredOperations(got)
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
