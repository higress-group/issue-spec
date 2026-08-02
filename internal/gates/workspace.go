package gates

import (
	"fmt"
	"sort"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

const (
	CodeProcessWorkspaceMigrationWarning = "process.workspace.migration_warning"
	CodeProcessWorkspaceInvalid          = "process.workspace.invalid"
	CodeProcessWorkspaceModeInvalid      = "process.workspace.mode_invalid"
	CodeProcessExecutionClassRemoved     = "process.execution_class.removed"
)

type WorkspaceEvaluationInput struct {
	Target    Target
	Mode      Mode
	Artifacts []model.Artifact
}

type WorkspaceEvaluationReport struct {
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// EvaluateWorkspaceSafety validates only optional execution isolation. It has
// no provider revision, review carrier, verification, or final-gate input.
func EvaluateWorkspaceSafety(input WorkspaceEvaluationInput) (WorkspaceEvaluationReport, error) {
	if err := input.Target.validate(); err != nil {
		return WorkspaceEvaluationReport{}, err
	}
	if err := input.Mode.validate(); err != nil {
		return WorkspaceEvaluationReport{}, err
	}
	var diagnostics []Diagnostic
	for _, process := range input.Artifacts {
		if process.Comment.Type != "PROCESS" || process.Comment.Status == "superseded" {
			continue
		}
		class := model.ParseProcessExecutionClass(process.Comment.ID, process.URL, process.Comment.Body)
		management := model.ParseProcessWorkspaceManagement(process.Comment.ID, process.URL, process.Comment.Body)
		workspace := model.ParseProcessWorkspace(process.Comment.ID, process.URL, process.Comment.Body)
		if class.Blocking() || management.Blocking() || workspace.Blocking() {
			diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceInvalid,
				SeverityError, true, "PROCESS workspace metadata is invalid", "invalid", "valid portable workspace", "workflow workspace inspect"))
			continue
		}
		if class.Class == model.ProcessExecutionReview || class.Class == model.ProcessExecutionVerification {
			diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessExecutionClassRemoved,
				SeverityError, true, "REVIEW/VERIFY PROCESS execution classes were removed; use provider review and configured checks",
				string(class.Class), "change-bearing|orchestration|external", "merge-check"))
			continue
		}
		if management.Explicit && management.Management == model.ProcessWorkspaceIndependent {
			continue
		}
		if workspace.Workspace == nil {
			diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceMigrationWarning,
				SeverityWarning, false, "managed PROCESS has no portable Workspace metadata; prepare it before delegated execution",
				"missing", "portable Workspace", "workflow workspace prepare"))
			continue
		}
		portable := processworkspace.PortableLease(*workspace.Workspace)
		switch class.Class {
		case model.ProcessExecutionChangeBearing:
			if portable.Mode != processworkspace.ModeWritable {
				diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceModeInvalid,
					SeverityError, true, "change-bearing PROCESS requires writable workspace mode", string(portable.Mode),
					string(processworkspace.ModeWritable), "workflow workspace prepare"))
			}
		case model.ProcessExecutionOrchestration, model.ProcessExecutionExternal:
			if portable.Mode != processworkspace.ModeNone {
				diagnostics = append(diagnostics, workspaceDiagnostic(process, input.Target, CodeProcessWorkspaceModeInvalid,
					SeverityError, true, fmt.Sprintf("%s PROCESS must not claim a checkout", class.Class), string(portable.Mode),
					string(processworkspace.ModeNone), "workflow workspace prepare"))
			}
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

func workspaceDiagnostic(process model.Artifact, target Target, code string, severity Severity, blocking bool,
	message, current, expected, command string) Diagnostic {
	return Diagnostic{Code: code, Gate: target, Severity: severity, Blocking: blocking, Message: message,
		Artifact: artifactRef(process), Current: current, Expected: expected,
		Remediation: Remediation{CommandFamily: command}, Freshness: FreshnessLocal}
}
