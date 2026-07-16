package gates

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
)

var (
	processIDPattern    = regexp.MustCompile(`PROCESS-[0-9]{3,}`)
	testEvidencePattern = regexp.MustCompile(`(?i)\btest(s|ing|ed)?\b`)
)

const (
	CodeSpecRequired              = "spec.required"
	CodeSpecStatusInvalid         = "spec.status.invalid"
	CodeQuestionBlocked           = "question.blocked"
	CodeTaskRequired              = "task.required"
	CodeTaskNotDone               = "task.not_done"
	CodeProcessRequired           = "process.required"
	CodeProcessNotDone            = "process.not_done"
	CodeReviewOpen                = "review.open"
	CodeVerifyRequired            = "verify.required"
	CodeVerifyTestEvidenceMissing = "verify.test_evidence_missing"
	CodeVerifySpecCoverageMissing = "verify.spec_coverage_missing"
	CodeProcessHandoffMissing     = "process.handoff_missing"
	CodeArtifactNoncanonical      = "artifact.noncanonical"
	CodeTraceabilityInvalid       = "traceability.invalid"
	CodeWorkflowInvalid           = "workflow.invalid"
	CodeWorkflowUnknown           = "workflow.unknown"
	CodeProcessPRLinkMissing      = "process.pr_link_missing"
	CodeProcessPRLinkUnknown      = "process.pr_link_unknown"
	CodeProcessEvidenceMissing    = "process.evidence_missing"
	CodeProcessEvidenceUnknown    = "process.evidence_unknown"
	CodePRChecksFailed            = "pr.checks.failed"
	CodePRChecksUnknown           = "pr.checks.unknown"
	CodeReviewFindingsOpen        = "review.findings.open"
	CodeReviewFindingsUnknown     = "review.findings.unknown"
	CodeProviderEvidenceMissing   = "provider.evidence.missing"
	CodeProviderEvidenceUnknown   = "provider.evidence.unknown"
	CodeDurableSpecInvalid        = "durable_spec.invalid"
	CodeDurableSpecUnknown        = "durable_spec.unknown"
	CodeProcessReviewRequired     = "process.review.required"
)

// Evaluate applies all policy locally and deterministically. It performs no
// network or filesystem access.
func Evaluate(snapshot Snapshot) (Report, error) {
	if err := snapshot.Target.validate(); err != nil {
		return Report{}, err
	}
	if err := snapshot.Mode.validate(); err != nil {
		return Report{}, err
	}

	e := evaluator{snapshot: snapshot}
	e.evaluateArtifacts()
	e.evaluateCanonical()
	e.evaluateTraceability()
	e.evaluateWorkflow()
	e.evaluateProcessEvidence()
	if err := e.evaluateWorkspaceEvidence(); err != nil {
		return Report{}, err
	}
	e.evaluateRemoteFacts()
	e.sort()

	ready := true
	for _, diagnostic := range e.diagnostics {
		if diagnostic.Blocking {
			ready = false
			break
		}
	}
	return Report{
		Ready: ready, Target: snapshot.Target, Mode: snapshot.Mode,
		PointInTime: snapshot.Mode == ModeForecast,
		Diagnostics: e.diagnostics, Processes: e.processes,
	}, nil
}

type evaluator struct {
	snapshot    Snapshot
	diagnostics []Diagnostic
	processes   []ProcessEvidenceReport
}

func (e *evaluator) evaluateProcessEvidence() {
	if !atLeast(e.snapshot.Target, TargetFinal) {
		return
	}
	for _, input := range e.snapshot.ProcessEvidence {
		report := EvaluateProcessEvidence(input, e.snapshot.Target, e.snapshot.Mode)
		e.processes = append(e.processes, report)
		e.diagnostics = append(e.diagnostics, report.Diagnostics...)
	}
	sort.Slice(e.processes, func(i, j int) bool { return e.processes[i].ProcessID < e.processes[j].ProcessID })
	e.requireIndependentReviewPresence()
}

// requireIndependentReviewPresence fails closed when a SPEC has satisfied
// change-bearing code but no satisfied, independent review PROCESS covering it.
// The name-based author-conflict check on a review PROCESS only runs when such a
// PROCESS exists, so without this presence requirement a non-trivial change could
// bypass independent review entirely by never creating the review node.
func (e *evaluator) requireIndependentReviewPresence() {
	reviewed := map[string]bool{}
	changeBearing := map[string]ArtifactRef{}
	for _, report := range e.processes {
		switch report.ExecutionClass {
		case model.ProcessExecutionReview:
			for _, spec := range report.SatisfiedSpecs {
				reviewed[spec] = true
			}
		case model.ProcessExecutionChangeBearing:
			for _, spec := range report.SatisfiedSpecs {
				if _, seen := changeBearing[spec]; !seen {
					changeBearing[spec] = ArtifactRef{Type: "SPEC", ID: spec}
				}
			}
		}
	}
	specs := make([]string, 0, len(changeBearing))
	for spec := range changeBearing {
		specs = append(specs, spec)
	}
	sort.Strings(specs)
	for _, spec := range specs {
		if reviewed[spec] {
			continue
		}
		e.add(CodeProcessReviewRequired, fmt.Sprintf("%s has change-bearing code but no independent review PROCESS covering it", spec),
			changeBearing[spec], "missing independent review", "review PROCESS by an agent other than the code author", "comment generate", "--type", "PROCESS")
	}
}

func (e *evaluator) evaluateWorkspaceEvidence() error {
	workspace := e.snapshot.Remote.Workspace
	if !workspace.Observed {
		return nil
	}
	carriers := ProcessCarrierRevisionFacts(e.processes)
	for processID, fact := range workspace.CarrierRevisions {
		carriers[processID] = fact
	}
	report, err := EvaluateWorkspaceEvidence(WorkspaceEvaluationInput{
		Target: e.snapshot.Target, Mode: e.snapshot.Mode, Artifacts: e.snapshot.Artifacts,
		ExpectedRevision: workspace.ExpectedRevision, IntegrationAncestry: workspace.IntegrationAncestry,
		ProcessEvidence: e.processes, CarrierRevisions: carriers,
	})
	if err != nil {
		return err
	}
	e.diagnostics = append(e.diagnostics, report.Diagnostics...)
	return nil
}

func (e *evaluator) add(code, message string, artifact ArtifactRef, current, expected, command string, args ...string) {
	e.diagnostics = append(e.diagnostics, Diagnostic{
		Code: code, Gate: e.snapshot.Target, Severity: SeverityError, Blocking: true,
		Message: message, Artifact: artifact, Current: current, Expected: expected,
		Remediation: Remediation{CommandFamily: command, Arguments: args}, Freshness: FreshnessLocal,
	})
}

func (e *evaluator) evaluateArtifacts() {
	var activeSpecs, activeTasks, activeProcesses []model.Artifact
	var doneVerifyBodies []string
	for _, artifact := range e.snapshot.Artifacts {
		comment := artifact.Comment
		if comment.Status == "superseded" || comment.Type == "" {
			continue
		}
		switch comment.Type {
		case "SPEC":
			activeSpecs = append(activeSpecs, artifact)
			if atLeast(e.snapshot.Target, TargetFinal) && comment.Status != "confirmed" && comment.Status != "done" {
				e.add(CodeSpecStatusInvalid, fmt.Sprintf("%s must be confirmed or done", comment.ID), artifactRef(artifact), comment.Status, "confirmed|done", "comment transition", "--id", comment.ID, "--to", "confirmed")
			}
		case "QUESTION":
			if comment.Status == "blocked" {
				e.add(CodeQuestionBlocked, fmt.Sprintf("%s is still blocked", comment.ID), artifactRef(artifact), comment.Status, "confirmed|done|superseded", "question resolve", "--id", comment.ID)
			}
		case "TASK":
			activeTasks = append(activeTasks, artifact)
			if atLeast(e.snapshot.Target, TargetFinal) && comment.Status != "done" {
				e.add(CodeTaskNotDone, fmt.Sprintf("%s must be done", comment.ID), artifactRef(artifact), comment.Status, "done", "comment transition", "--id", comment.ID, "--to", "done")
			}
		case "PROCESS":
			activeProcesses = append(activeProcesses, artifact)
			if atLeast(e.snapshot.Target, TargetFinal) && comment.Status != "done" {
				e.add(CodeProcessNotDone, fmt.Sprintf("%s must be done", comment.ID), artifactRef(artifact), comment.Status, "done", "comment transition", "--id", comment.ID, "--to", "done")
			}
		case "REVIEW":
			if comment.Status != "done" {
				e.add(CodeReviewOpen, fmt.Sprintf("%s must be done or superseded", comment.ID), artifactRef(artifact), comment.Status, "done|superseded", "comment transition", "--id", comment.ID, "--to", "done")
			}
		case "VERIFY":
			if comment.Status == "done" {
				doneVerifyBodies = append(doneVerifyBodies, comment.Body)
			}
		}
	}

	if len(activeSpecs) == 0 {
		e.add(CodeSpecRequired, "at least one active SPEC is required", ArtifactRef{}, "0", ">=1", "comment generate", "--type", "SPEC")
	}
	if atLeast(e.snapshot.Target, TargetDesign) && len(activeTasks) == 0 {
		e.add(CodeTaskRequired, "at least one active TASK is required", ArtifactRef{}, "0", ">=1", "comment generate", "--type", "TASK")
	}
	if atLeast(e.snapshot.Target, TargetImplement) && len(activeProcesses) == 0 {
		e.add(CodeProcessRequired, "at least one active PROCESS is required", ArtifactRef{}, "0", ">=1", "comment generate", "--type", "PROCESS")
	}
	if !atLeast(e.snapshot.Target, TargetFinal) {
		return
	}
	if len(doneVerifyBodies) == 0 {
		e.add(CodeVerifyRequired, "at least one done VERIFY is required", ArtifactRef{}, "0", ">=1 done", "comment generate", "--type", "VERIFY")
	}
	verifyText := strings.Join(doneVerifyBodies, "\n")
	if len(doneVerifyBodies) > 0 && !testEvidencePattern.MatchString(verifyText) {
		e.add(CodeVerifyTestEvidenceMissing, "done VERIFY comments do not reference test evidence", ArtifactRef{}, "missing", "test evidence", "comment upsert", "--type", "VERIFY")
	}
	for _, spec := range activeSpecs {
		if !ReferencesArtifactID(verifyText, spec.Comment.ID) {
			e.add(CodeVerifySpecCoverageMissing, fmt.Sprintf("%s is not referenced by a done VERIFY", spec.Comment.ID), artifactRef(spec), "uncovered", "covered by done VERIFY", "comment upsert", "--type", "VERIFY")
		}
	}
	e.evaluateSerialHandoffs(activeProcesses)
}

func (e *evaluator) evaluateSerialHandoffs(processes []model.Artifact) {
	byID := make(map[string]model.Artifact, len(processes))
	dependedUpon := map[string]bool{}
	for _, process := range processes {
		byID[process.Comment.ID] = process
	}
	for _, process := range processes {
		for _, dependency := range processIDPattern.FindAllString(section(process.Comment.Body, "### Dependencies"), -1) {
			if dependency != process.Comment.ID {
				if _, ok := byID[dependency]; ok {
					dependedUpon[dependency] = true
				}
			}
		}
	}
	for id := range dependedUpon {
		process := byID[id]
		if process.Comment.Status == "done" && emptyOrNA(section(process.Comment.Body, "### Handoff")) {
			e.add(CodeProcessHandoffMissing, fmt.Sprintf("%s is a serial predecessor without handoff evidence", id), artifactRef(process), "missing", "non-empty handoff", "comment transition", "--id", id, "--to", "done", "--handoff-file", "FILE")
		}
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
		e.add(CodeArtifactNoncanonical, diagnostic.Message, ArtifactRef{Type: diagnostic.Type, ID: diagnostic.ID, URL: diagnostic.URL}, diagnostic.Element, "canonical", "comment generate", "--type", diagnostic.Type, "--id", diagnostic.ID)
	}
}

func (e *evaluator) evaluateTraceability() {
	report := e.snapshot.Traceability.Report
	if !e.snapshot.Traceability.Observed {
		report = model.VerifyTraceability(e.snapshot.Artifacts)
	}
	for _, message := range report.Errors {
		e.add(CodeTraceabilityInvalid, message, ArtifactRef{}, "invalid", "valid", "link")
	}
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

func (e *evaluator) evaluateRemoteFacts() {
	if !atLeast(e.snapshot.Target, TargetFinal) {
		return
	}
	e.evaluateFact(e.snapshot.Remote.PRChecks, CodePRChecksUnknown, CodePRChecksFailed, "pull request checks are unknown", "pull request checks failed", "checks.read", ArtifactRef{})
	e.evaluateFact(e.snapshot.Remote.ReviewFindings, CodeReviewFindingsUnknown, CodeReviewFindingsOpen, "review findings are unknown", "blocking review findings remain open", "review sync", ArtifactRef{})
	e.evaluateFact(e.snapshot.Remote.ProviderEvidence, CodeProviderEvidenceUnknown, CodeProviderEvidenceMissing, "provider evidence is unknown", "required provider evidence is missing", "evidence explain", ArtifactRef{})
	for _, process := range e.snapshot.Remote.Processes {
		ref := ArtifactRef{Type: "PROCESS", ID: process.ProcessID, URL: process.ProcessURL}
		e.evaluateFact(process.PRLink, CodeProcessPRLinkUnknown, CodeProcessPRLinkMissing, "PROCESS PR link is unknown", "PROCESS does not link the required PR", "pr link-process", ref)
		e.evaluateFact(process.Evidence, CodeProcessEvidenceUnknown, CodeProcessEvidenceMissing, "PROCESS evidence is unknown", "PROCESS evidence is missing", "pr rationale", ref)
	}
	if e.snapshot.Target == TargetArchive {
		e.evaluateFact(e.snapshot.Remote.DurableSpec, CodeDurableSpecUnknown, CodeDurableSpecInvalid, "durable spec state is unknown", "durable spec is missing or invalid", "archive durable-spec", ArtifactRef{})
	}
}

func (e *evaluator) evaluateFact(fact Fact, unknownCode, failedCode, unknownMessage, failedMessage, command string, artifact ArtifactRef) {
	if !fact.Required {
		return
	}
	if !fact.Known {
		e.add(unknownCode, unknownMessage, artifact, "unknown", expectedOr(fact.Expected, "passed"), command)
		diagnostic := &e.diagnostics[len(e.diagnostics)-1]
		diagnostic.Freshness = FreshnessUnknown
		return
	}
	if fact.Passed {
		return
	}
	e.add(failedCode, failedMessage, artifact, expectedOr(fact.Current, "failed"), expectedOr(fact.Expected, "passed"), command)
	diagnostic := &e.diagnostics[len(e.diagnostics)-1]
	diagnostic.Freshness = FreshnessPointInTime
	diagnostic.ObservedAt = fact.ObservedAt
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
		if a.Artifact.URL != b.Artifact.URL {
			return a.Artifact.URL < b.Artifact.URL
		}
		if a.Current != b.Current {
			return a.Current < b.Current
		}
		if a.Message != b.Message {
			return a.Message < b.Message
		}
		left, right := diagnosticSemanticKey(a), diagnosticSemanticKey(b)
		if left != right {
			return left < right
		}
		// Equivalent point-in-time facts collapse to the latest non-zero
		// observation. This order is independent of collector input order.
		if a.ObservedAt == nil {
			return false
		}
		if b.ObservedAt == nil {
			return true
		}
		return a.ObservedAt.After(*b.ObservedAt)
	})
	result := e.diagnostics[:0]
	for _, diagnostic := range e.diagnostics {
		if len(result) > 0 && diagnosticSemanticKey(result[len(result)-1]) == diagnosticSemanticKey(diagnostic) {
			continue
		}
		result = append(result, diagnostic)
	}
	e.diagnostics = result
}

func diagnosticSemanticKey(diagnostic Diagnostic) string {
	parts := []string{diagnostic.Code, string(diagnostic.Gate), string(diagnostic.Severity), fmt.Sprintf("%t", diagnostic.Blocking),
		diagnostic.Message, diagnostic.Artifact.Type, diagnostic.Artifact.ID, diagnostic.Artifact.URL, diagnostic.Current, diagnostic.Expected,
		diagnostic.Remediation.CommandFamily, string(diagnostic.Freshness), fmt.Sprintf("%d", len(diagnostic.Remediation.Arguments))}
	parts = append(parts, diagnostic.Remediation.Arguments...)
	var key strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&key, "%d:%s", len(part), part)
	}
	return key.String()
}

func atLeast(actual, threshold Target) bool {
	order := map[Target]int{TargetProposal: 0, TargetDesign: 1, TargetImplement: 2, TargetFinal: 3, TargetArchive: 4}
	return order[actual] >= order[threshold]
}

func section(body, heading string) string {
	lines := strings.Split(model.LogicalBody(body), "\n")
	start := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = index + 1
			break
		}
	}
	if start < 0 {
		return ""
	}
	var values []string
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
			break
		}
		values = append(values, line)
	}
	return strings.TrimSpace(strings.Join(values, "\n"))
}

func emptyOrNA(value string) bool {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "-"))
	return value == "" || strings.EqualFold(value, "N/A")
}
