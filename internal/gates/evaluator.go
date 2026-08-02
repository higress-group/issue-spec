package gates

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/relationships"
)

const (
	CodeSpecRequired         = "spec.required"
	CodeSpecStatusInvalid    = "spec.status.invalid"
	CodeQuestionBlocked      = "question.blocked"
	CodeTaskRequired         = "task.required"
	CodeProcessRequired      = "process.required"
	CodeArtifactNoncanonical = "artifact.noncanonical"
	CodeTraceabilityInvalid  = "traceability.invalid"
	CodeWorkflowInvalid      = "workflow.invalid"
	CodeWorkflowUnknown      = "workflow.unknown"
)

var artifactIDBoundary = regexp.MustCompile(`[A-Z]+-[0-9]+`)

// Evaluate applies optional planning policy locally and deterministically. It
// never evaluates merge readiness or reads provider review/check authority.
func Evaluate(snapshot Snapshot) (Report, error) {
	if err := snapshot.Target.validate(); err != nil {
		return Report{}, err
	}
	if err := snapshot.Mode.validate(); err != nil {
		return Report{}, err
	}
	snapshot.Artifacts = planningArtifacts(snapshot.Artifacts)
	snapshot.Canonical.Diagnostics = planningCanonicalDiagnostics(snapshot.Canonical.Diagnostics)
	e := evaluator{snapshot: snapshot}
	e.evaluateArtifacts()
	e.evaluateCanonical()
	e.evaluateTraceability()
	e.evaluateWorkflow()
	if snapshot.Target == TargetImplement && snapshot.Remote.Workspace.Observed {
		report, err := EvaluateWorkspaceSafety(WorkspaceEvaluationInput{Target: snapshot.Target, Mode: snapshot.Mode, Artifacts: snapshot.Artifacts})
		if err != nil {
			return Report{}, err
		}
		e.diagnostics = append(e.diagnostics, report.Diagnostics...)
	}
	e.sort()
	ready := true
	for _, diagnostic := range e.diagnostics {
		if diagnostic.Blocking {
			ready = false
			break
		}
	}
	return Report{Ready: ready, Target: snapshot.Target, Mode: snapshot.Mode,
		PointInTime: snapshot.Mode == ModeForecast, Diagnostics: e.diagnostics}, nil
}

func planningArtifacts(values []model.Artifact) []model.Artifact {
	result := make([]model.Artifact, 0, len(values))
	for _, artifact := range values {
		switch artifact.Comment.Type {
		case "SPEC", "QUESTION", "TASK", "PROCESS":
			result = append(result, artifact)
		}
	}
	return result
}

func planningCanonicalDiagnostics(values []model.CanonicalDiagnostic) []model.CanonicalDiagnostic {
	result := make([]model.CanonicalDiagnostic, 0, len(values))
	for _, diagnostic := range values {
		switch diagnostic.Type {
		case "SPEC", "QUESTION", "TASK", "PROCESS":
			result = append(result, diagnostic)
		}
	}
	return result
}

type evaluator struct {
	snapshot    Snapshot
	diagnostics []Diagnostic
}

func (e *evaluator) add(code, message string, artifact ArtifactRef, current, expected, command string, args ...string) {
	e.diagnostics = append(e.diagnostics, Diagnostic{Code: code, Gate: e.snapshot.Target, Severity: SeverityError,
		Blocking: true, Message: message, Artifact: artifact, Current: current, Expected: expected,
		Remediation: Remediation{CommandFamily: command, Arguments: args}, Freshness: FreshnessLocal})
}

func (e *evaluator) evaluateArtifacts() {
	activeSpecs, activeTasks, activeProcesses := 0, 0, 0
	for _, artifact := range e.snapshot.Artifacts {
		comment := artifact.Comment
		if comment.Type == "" || comment.Status == "superseded" {
			continue
		}
		switch comment.Type {
		case "SPEC":
			activeSpecs++
			if comment.Status != "confirmed" && comment.Status != "done" {
				e.add(CodeSpecStatusInvalid, fmt.Sprintf("%s must be confirmed or done", comment.ID), artifactRef(artifact),
					comment.Status, "confirmed|done", "comment transition", "--id", comment.ID, "--to", "confirmed")
			}
		case "QUESTION":
			if !model.QuestionIsSatisfied(comment, e.snapshot.Answers) {
				e.add(CodeQuestionBlocked, fmt.Sprintf("%s remains unanswered", comment.ID), artifactRef(artifact),
					comment.Status, "effective answer or resolution", "question answer", "--question-id", comment.ID)
			}
		case "TASK":
			activeTasks++
		case "PROCESS":
			activeProcesses++
		}
	}
	if activeSpecs == 0 {
		e.add(CodeSpecRequired, "at least one active SPEC is required", ArtifactRef{}, "0", ">=1", "comment generate", "--type", "SPEC")
	}
	if e.snapshot.Target == TargetImplement && activeTasks == 0 {
		e.add(CodeTaskRequired, "Implement planning requires at least one active TASK", ArtifactRef{}, "0", ">=1", "comment generate", "--type", "TASK")
	}
	if e.snapshot.Target == TargetImplement && activeProcesses == 0 {
		e.add(CodeProcessRequired, "Implement planning requires at least one active PROCESS", ArtifactRef{}, "0", ">=1", "comment generate", "--type", "PROCESS")
	}
}

func (e *evaluator) evaluateCanonical() {
	diagnostics := e.snapshot.Canonical.Diagnostics
	if !e.snapshot.Canonical.Observed {
		diagnostics = nil
		for _, artifact := range e.snapshot.Artifacts {
			if artifact.Comment.Status != "superseded" {
				diagnostics = append(diagnostics, model.ValidateArtifact(artifact)...)
			}
		}
	}
	for _, diagnostic := range diagnostics {
		e.add(CodeArtifactNoncanonical, diagnostic.Message,
			ArtifactRef{Type: diagnostic.Type, ID: diagnostic.ID, URL: diagnostic.URL}, diagnostic.Element,
			"canonical", "comment generate", "--type", diagnostic.Type, "--id", diagnostic.ID)
	}
}

func (e *evaluator) evaluateTraceability() {
	report := e.snapshot.Traceability.Report
	if !e.snapshot.Traceability.Observed {
		if !e.snapshot.Relationships.Required {
			report = model.VerifyTraceability(e.snapshot.Artifacts)
		} else {
			var indexErr error
			if !e.snapshot.Relationships.Observed {
				indexErr = errors.New("relationship index not observed")
			} else if e.snapshot.Relationships.Error != "" {
				indexErr = errors.New(e.snapshot.Relationships.Error)
			}
			report = model.VerifyTraceabilityWithRelationships(e.snapshot.Artifacts,
				traceabilityEdges(e.snapshot.Relationships.Index), indexErr)
		}
	}
	for _, message := range report.Errors {
		e.add(CodeTraceabilityInvalid, message, ArtifactRef{}, "invalid", "valid", "link")
	}
}

func traceabilityEdges(index relationships.Index) []model.TraceabilityEdge {
	result := make([]model.TraceabilityEdge, 0, len(index.Edges))
	for _, edge := range index.Edges {
		result = append(result, model.TraceabilityEdge{Kind: string(edge.Kind), OwnerID: edge.Owner.ID, TargetID: edge.Target.ID})
	}
	return result
}

func (e *evaluator) evaluateWorkflow() {
	fact := e.snapshot.Workflow
	if !fact.Required {
		return
	}
	if !fact.Known {
		e.add(CodeWorkflowUnknown, "workflow schema state was not collected", ArtifactRef{}, "unknown", "valid", "workflow validate")
		e.diagnostics[len(e.diagnostics)-1].Freshness = FreshnessUnknown
		return
	}
	if fact.Valid {
		return
	}
	if len(fact.Errors) == 0 {
		fact.Errors = []string{"workflow schema is invalid"}
	}
	for _, message := range fact.Errors {
		e.add(CodeWorkflowInvalid, message, ArtifactRef{}, "invalid", "valid", "workflow validate")
	}
}

func (e *evaluator) sort() {
	sort.SliceStable(e.diagnostics, func(i, j int) bool {
		a, b := e.diagnostics[i], e.diagnostics[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Artifact.ID != b.Artifact.ID {
			return a.Artifact.ID < b.Artifact.ID
		}
		return a.Message < b.Message
	})
}

// ReferencesArtifactID performs an exact typed-artifact token match for
// planning/navigation helpers.
func ReferencesArtifactID(body, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, candidate := range artifactIDBoundary.FindAllString(body, -1) {
		if candidate == want {
			return true
		}
	}
	return false
}
