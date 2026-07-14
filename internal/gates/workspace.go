package gates

import (
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

const (
	CodeProcessWorkspaceMigrationWarning        = "process.workspace.migration_warning"
	CodeProcessWorkspaceRequired                = "process.workspace.required"
	CodeProcessWorkspaceInvalid                 = "process.workspace.invalid"
	CodeProcessWorkspaceStateInvalid            = "process.workspace.state_invalid"
	CodeProcessWorkspaceModeInvalid             = "process.workspace.mode_invalid"
	CodeProcessWorkspaceRevisionUnknown         = "process.workspace.revision_unknown"
	CodeProcessWorkspaceRevisionStale           = "process.workspace.revision_stale"
	CodeProcessWorkspaceReviewEvidenceMissing   = "process.workspace.review_evidence_missing"
	CodeProcessWorkspaceVerifyEvidenceMissing   = "process.workspace.verify_evidence_missing"
	CodeProcessWorkspaceProviderEvidenceMissing = "process.workspace.provider_evidence_missing"
)

// WorkspaceEvaluationInput is provider-neutral. ExpectedRevision is normally
// the PR head or external subject revision. IntegrationAncestry lets a collector
// explicitly approve an integrated ancestor when policy permits it; the local
// evaluator never infers ancestry from SHA strings.
type WorkspaceEvaluationInput struct {
	Target              Target
	Mode                Mode
	Artifacts           []model.Artifact
	ExpectedRevision    Fact
	IntegrationAncestry map[string]Fact
	ProcessEvidence     []ProcessEvidenceReport
	CarrierRevisions    map[string]CarrierRevisionFact
}

// CarrierRevisionFact is supplied by a provider/artifact collector. Trusted
// means Revision came from the carrier itself (review, verification, or
// provider evidence), never from a PROCESS body substring or the expected head.
type CarrierRevisionFact struct {
	Known    bool   `json:"known"`
	Revision string `json:"revision,omitempty"`
	Trusted  bool   `json:"trusted"`
	Source   string `json:"source,omitempty"`
}

type WorkspaceEvaluationReport struct {
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// EvaluateWorkspaceEvidence evaluates portable PROCESS Workspace metadata.
// It performs no Git or provider access and is safe for status/verify callers
// that supply facts from their own point-in-time collectors.
func EvaluateWorkspaceEvidence(input WorkspaceEvaluationInput) (WorkspaceEvaluationReport, error) {
	if err := input.Target.validate(); err != nil {
		return WorkspaceEvaluationReport{}, err
	}
	if err := input.Mode.validate(); err != nil {
		return WorkspaceEvaluationReport{}, err
	}
	evidence := map[string]ProcessEvidenceReport{}
	for _, report := range input.ProcessEvidence {
		evidence[report.ProcessID] = report
	}
	var diagnostics []Diagnostic
	for _, process := range input.Artifacts {
		if process.Comment.Type != "PROCESS" || process.Comment.Status == "superseded" {
			continue
		}
		class := model.ParseProcessExecutionClass(process.Comment.ID, process.URL, process.Comment.Body)
		if class.Blocking() {
			continue
		}
		management := model.ParseProcessWorkspaceManagement(process.Comment.ID, process.URL, process.Comment.Body)
		if management.Blocking() {
			continue
		}
		workspace := model.ParseProcessWorkspace(process.Comment.ID, process.URL, process.Comment.Body)
		if workspace.Blocking() {
			blocking := atLeast(input.Target, TargetImplement)
			diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceInvalid,
				severityFor(blocking), blocking, model.CanonicalDiagnosticStrings(workspace.Diagnostics)[0], "invalid", "valid portable Workspace", "workflow workspace inspect"))
			continue
		}
		if workspace.Workspace == nil {
			if management.Explicit && management.Management == model.ProcessWorkspaceIndependent && class.Class == model.ProcessExecutionChangeBearing {
				continue
			}
			blocking := atLeast(input.Target, TargetFinal) && class.Explicit && class.Class == model.ProcessExecutionChangeBearing
			code, message := CodeProcessWorkspaceMigrationWarning, "PROCESS has no portable Workspace metadata; migrate it before managed execution"
			if blocking {
				code, message = CodeProcessWorkspaceRequired, "managed PROCESS requires portable Workspace metadata before final readiness"
			}
			diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, code, severityFor(blocking), blocking,
				message, "missing", "portable Workspace", "workflow workspace prepare"))
			if class.Class == model.ProcessExecutionExternal && atLeast(input.Target, TargetFinal) {
				base := evidence[process.Comment.ID]
				if hasSatisfied(base, "exact-revision external evidence") {
					diagnostics = append(diagnostics, evaluateCarrierRevision(process, input, "")...)
				} else if !hasCarrierMissingDiagnostic(base) {
					diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceProviderEvidenceMissing,
						SeverityError, true, "external PROCESS requires consumed exact-revision provider evidence; local Workspace metadata cannot substitute for it",
						"missing", "exact-revision provider evidence", "evidence explain"))
				}
			}
			continue
		}
		if !atLeast(input.Target, TargetFinal) {
			continue
		}
		portable := processworkspace.PortableLease(*workspace.Workspace)
		switch class.Class {
		case model.ProcessExecutionChangeBearing:
			if portable.Mode != processworkspace.ModeWritable {
				diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceModeInvalid, SeverityError, true,
					"change-bearing PROCESS requires writable Workspace mode", string(portable.Mode), string(processworkspace.ModeWritable), "workflow workspace prepare"))
			}
			// Cleanup deliberately removes the local worktree but retains the
			// immutable worker-result and integration evidence. A successfully
			// cleaned lease therefore remains valid final-gate evidence; requiring
			// the transient integrated state would make the required cleanup step
			// invalidate an otherwise completed PROCESS.
			if (portable.State != processworkspace.StateIntegrated && portable.State != processworkspace.StateCleaned) || portable.ResultCommit == "" || portable.IntegrationSHA == "" {
				diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceStateInvalid, SeverityError, true,
					"change-bearing PROCESS must publish worker result and integrated lifecycle evidence", string(portable.State), "integrated|cleaned", "workflow workspace integrate"))
			} else {
				diagnostics = append(diagnostics, evaluateWorkspaceRevision(process, input, portable.IntegrationSHA, true)...)
			}
		case model.ProcessExecutionReview:
			diagnostics = append(diagnostics, evaluateSnapshotWorkspace(process, input, portable, evidence[process.Comment.ID], "review evidence", CodeProcessWorkspaceReviewEvidenceMissing)...)
		case model.ProcessExecutionVerification:
			diagnostics = append(diagnostics, evaluateSnapshotWorkspace(process, input, portable, evidence[process.Comment.ID], "verification evidence", CodeProcessWorkspaceVerifyEvidenceMissing)...)
		case model.ProcessExecutionOrchestration:
			if portable.Mode != processworkspace.ModeNone {
				diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceModeInvalid, SeverityError, true,
					"orchestration PROCESS must not claim a writable or snapshot checkout", string(portable.Mode), string(processworkspace.ModeNone), "workflow workspace prepare"))
			}
		case model.ProcessExecutionExternal:
			diagnostics = append(diagnostics, evaluateExternalWorkspace(process, input, portable, evidence[process.Comment.ID])...)
		}
	}
	sort.SliceStable(diagnostics, func(i, j int) bool {
		if diagnostics[i].Code != diagnostics[j].Code {
			return diagnostics[i].Code < diagnostics[j].Code
		}
		return diagnostics[i].Artifact.ID < diagnostics[j].Artifact.ID
	})
	return WorkspaceEvaluationReport{Diagnostics: diagnostics}, nil
}

func evaluateExternalWorkspace(process model.Artifact, input WorkspaceEvaluationInput, portable processworkspace.PortableLease, evidence ProcessEvidenceReport) []Diagnostic {
	var diagnostics []Diagnostic
	if portable.Mode != processworkspace.ModeNone {
		diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceModeInvalid, SeverityError, true,
			"external PROCESS must use no-checkout Workspace mode", string(portable.Mode), string(processworkspace.ModeNone), "workflow workspace prepare"))
	}
	if hasSatisfied(evidence, "exact-revision external evidence") {
		diagnostics = append(diagnostics, evaluateCarrierRevision(process, input, "")...)
	} else if !hasCarrierMissingDiagnostic(evidence) {
		diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceProviderEvidenceMissing, SeverityError, true,
			"external PROCESS requires consumed exact-revision provider evidence; local Workspace metadata cannot substitute for it",
			"missing", "exact-revision provider evidence", "evidence explain"))
	}
	return diagnostics
}

func evaluateSnapshotWorkspace(process model.Artifact, input WorkspaceEvaluationInput, portable processworkspace.PortableLease, evidence ProcessEvidenceReport, satisfied, missingCode string) []Diagnostic {
	var diagnostics []Diagnostic
	if portable.Mode != processworkspace.ModeSnapshot {
		diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceModeInvalid, SeverityError, true,
			"review and verification PROCESS nodes require immutable snapshot mode", string(portable.Mode), string(processworkspace.ModeSnapshot), "workflow workspace prepare"))
	}
	if portable.State != processworkspace.StatePrepared && portable.State != processworkspace.StateCleanupPending && portable.State != processworkspace.StateCleaned {
		diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceStateInvalid, SeverityError, true,
			"snapshot PROCESS must be prepared, cleanup-pending, or cleaned after evidence collection", string(portable.State), "prepared|cleanup-pending|cleaned", "workflow workspace reconcile"))
	}
	diagnostics = append(diagnostics, evaluateWorkspaceRevision(process, input, portable.DetachedRevision, false)...)
	if hasSatisfied(evidence, satisfied) {
		if input.ExpectedRevision.Known && strings.TrimSpace(input.ExpectedRevision.Expected) != "" {
			diagnostics = append(diagnostics, evaluateCarrierRevision(process, input, portable.DetachedRevision)...)
		}
	} else if !hasCarrierMissingDiagnostic(evidence) {
		diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, missingCode, SeverityError, true,
			fmt.Sprintf("snapshot PROCESS lacks completed %s at its exact revision", satisfied), "missing", satisfied, "comment upsert"))
	}
	return diagnostics
}

func evaluateCarrierRevision(process model.Artifact, input WorkspaceEvaluationInput, workspaceRevision string) []Diagnostic {
	expected := strings.TrimSpace(input.ExpectedRevision.Expected)
	if !input.ExpectedRevision.Known || expected == "" {
		diagnostic := workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceRevisionUnknown, SeverityError, true,
			"expected revision for carrier binding was not collected", "unknown", "exact carrier revision", "verify")
		diagnostic.Freshness = FreshnessUnknown
		return []Diagnostic{diagnostic}
	}
	fact, ok := input.CarrierRevisions[process.Comment.ID]
	if !ok || !fact.Known || strings.TrimSpace(fact.Revision) == "" {
		diagnostic := workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceRevisionUnknown, SeverityError, true,
			"carrier revision was not collected from a trusted source", "unknown", expected, "verify")
		diagnostic.Freshness = FreshnessUnknown
		return []Diagnostic{diagnostic}
	}
	revision := strings.TrimSpace(fact.Revision)
	if !fact.Trusted || strings.TrimSpace(fact.Source) == "" || !strings.EqualFold(revision, expected) ||
		(workspaceRevision != "" && !strings.EqualFold(revision, workspaceRevision)) {
		return []Diagnostic{workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceRevisionStale, SeverityError, true,
			"carrier evidence is untrusted or not bound to the exact Workspace and expected revision", revision, expected, "verify")}
	}
	return nil
}

func evaluateWorkspaceRevision(process model.Artifact, input WorkspaceEvaluationInput, actual string, allowAncestor bool) []Diagnostic {
	fact := input.ExpectedRevision
	if !fact.Known || strings.TrimSpace(fact.Expected) == "" {
		diagnostic := workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceRevisionUnknown, SeverityError, true,
			"expected PR or provider revision was not collected", "unknown", "exact revision", "verify")
		diagnostic.Freshness = FreshnessUnknown
		return []Diagnostic{diagnostic}
	}
	expected := strings.TrimSpace(fact.Expected)
	if strings.EqualFold(actual, expected) {
		return nil
	}
	if allowAncestor {
		if ancestry, ok := input.IntegrationAncestry[process.Comment.ID]; ok && ancestry.Known && ancestry.Passed &&
			strings.EqualFold(strings.TrimSpace(ancestry.Current), actual) && strings.EqualFold(strings.TrimSpace(ancestry.Expected), expected) {
			return nil
		}
	}
	return []Diagnostic{workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceRevisionStale, SeverityError, true,
		"Workspace evidence is not bound to the expected PR or provider revision", actual, expected, "workflow workspace reconcile")}
}

func workspaceDiagnostic(process model.Artifact, target Target, code string, severity Severity, blocking bool, message, current, expected, command string) Diagnostic {
	return Diagnostic{Code: code, Gate: target, Severity: severity, Blocking: blocking, Message: message,
		Artifact: artifactRef(process), Current: current, Expected: expected,
		Remediation: Remediation{CommandFamily: command}, Freshness: FreshnessLocal}
}

func severityFor(blocking bool) Severity {
	if blocking {
		return SeverityError
	}
	return SeverityWarning
}

func hasSatisfied(report ProcessEvidenceReport, value string) bool {
	for _, candidate := range report.Satisfied {
		if candidate == value {
			return true
		}
	}
	return false
}

func hasCarrierMissingDiagnostic(report ProcessEvidenceReport) bool {
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Code == CodeProcessCarrierMissing && diagnostic.Blocking {
			return true
		}
	}
	return false
}
