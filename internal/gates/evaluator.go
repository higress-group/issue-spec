package gates

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/finalization"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/relationships"
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
	CodeVerifyRevisionInvalid     = "verify.revision.invalid"
	CodeVerifyRevisionUnknown     = "verify.revision.unknown"
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
	// TargetFinal has one current policy. The explicit, non-serializable bridge
	// keeps pre-snapshot in-process callers working for one compatibility window.
	if snapshot.Target == TargetFinal && !snapshot.LegacyFinalCompatibility {
		return evaluateMinimalFinal(snapshot), nil
	}

	selection := finalization.Select(snapshot.Artifacts)
	e := evaluator{snapshot: snapshot, selection: selection, activeProcesses: map[string]bool{}}
	for _, id := range selection.ActiveProcessIDs {
		e.activeProcesses[id] = true
	}
	for _, artifact := range snapshot.Artifacts {
		if artifact.Comment.Type == "PROCESS" {
			e.processSelectionObserved = true
			break
		}
	}
	e.evaluateSelection()
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
	snapshot                 Snapshot
	selection                finalization.Selection
	activeProcesses          map[string]bool
	processSelectionObserved bool
	diagnostics              []Diagnostic
	processes                []ProcessEvidenceReport
}

func (e *evaluator) evaluateSelection() {
	for _, diagnostic := range e.selection.Diagnostics {
		e.add(CodeTraceabilityInvalid, fmt.Sprintf("PROCESS selection %s: %s", diagnostic.Code, diagnostic.Message),
			ArtifactRef{Type: "PROCESS", ID: diagnostic.ProcessID, URL: diagnostic.URL}, "invalid", "valid explicit supersession graph", "finalize detail")
	}
}

func (e *evaluator) processIsActive(id string) bool {
	return !e.processSelectionObserved || e.activeProcesses[id]
}

func (e *evaluator) currentArtifacts() []model.Artifact {
	current := make([]model.Artifact, 0, len(e.snapshot.Artifacts))
	for _, artifact := range e.snapshot.Artifacts {
		if artifact.Comment.Type != "PROCESS" || e.processIsActive(artifact.Comment.ID) {
			current = append(current, artifact)
		}
	}
	return current
}

func (e *evaluator) evaluateProcessEvidence() {
	if !atLeast(e.snapshot.Target, TargetFinal) {
		return
	}
	for _, input := range e.snapshot.ProcessEvidence {
		if !e.processIsActive(input.Process.Comment.ID) {
			continue
		}
		report := EvaluateProcessEvidence(input, e.snapshot.Target, e.snapshot.Mode)
		e.processes = append(e.processes, report)
		e.diagnostics = append(e.diagnostics, report.Diagnostics...)
	}
	sort.Slice(e.processes, func(i, j int) bool { return e.processes[i].ProcessID < e.processes[j].ProcessID })
	e.requireIndependentReviewPresence()
	e.requireCarrierCompleteVerification()
}

// requireIndependentReviewPresence fails closed when a SPEC has satisfied
// change-bearing code but no satisfied, independent review PROCESS covering it.
// The name-based author-conflict check on a review PROCESS only runs when such a
// PROCESS exists, so without this presence requirement a non-trivial change could
// bypass independent review entirely by never creating the review node.
func (e *evaluator) requireIndependentReviewPresence() {
	reviewed := map[string]bool{}
	changeBearing := map[string][]ProcessEvidenceReport{}
	changeBearingIDs := map[string]bool{}
	for _, report := range e.processes {
		switch report.ExecutionClass {
		case model.ProcessExecutionReview:
			for _, spec := range report.SatisfiedSpecs {
				reviewed[spec] = true
			}
		case model.ProcessExecutionChangeBearing:
			changeBearingIDs[report.ProcessID] = true
			for _, spec := range report.SatisfiedSpecs {
				changeBearing[spec] = append(changeBearing[spec], report)
			}
		}
	}
	specs := make([]string, 0, len(changeBearing))
	for spec := range changeBearing {
		specs = append(specs, spec)
	}
	sort.Strings(specs)
	for _, spec := range specs {
		if len(changeBearingIDs) == 1 {
			if reviewed[spec] {
				continue
			}
			e.add(CodeProcessReviewRequired, fmt.Sprintf("%s has change-bearing code but no independent review PROCESS covering it", spec),
				ArtifactRef{Type: "SPEC", ID: spec}, "missing independent review", "review PROCESS by an agent other than the code author", "comment generate", "--type", "PROCESS")
			continue
		}
		for _, process := range changeBearing[spec] {
			// Existing single-carrier workflows retain their original SPEC-level
			// presence rule. Once a carrier is shared across an active set, each
			// PROCESS/SPEC pair must be enumerated by exact links; coverage of the
			// same SPEC through a sibling PROCESS cannot rescue this carrier.
			if reviewed[spec] && e.reviewPairCovered(process.ProcessID, spec) {
				continue
			}
			e.add(CodeProcessReviewRequired, fmt.Sprintf("%s/%s has change-bearing code but no independent REVIEW carrier enumerating that PROCESS/SPEC pair", process.ProcessID, spec),
				ArtifactRef{Type: "PROCESS", ID: process.ProcessID, URL: process.ProcessURL}, "missing independent review", "review PROCESS and REVIEW linked to the active PROCESS and SPEC", "comment upsert", "--type", "REVIEW")
		}
	}
}

func (e *evaluator) requireCarrierCompleteVerification() {
	bySpec := map[string][]ProcessEvidenceReport{}
	changeBearingIDs := map[string]bool{}
	for _, report := range e.processes {
		if report.ExecutionClass != model.ProcessExecutionChangeBearing {
			continue
		}
		changeBearingIDs[report.ProcessID] = true
		for _, spec := range report.SatisfiedSpecs {
			bySpec[spec] = append(bySpec[spec], report)
		}
	}
	if len(changeBearingIDs) < 2 {
		return
	}
	for _, spec := range sortedReportKeys(bySpec) {
		for _, process := range bySpec[spec] {
			if e.verificationPairCovered(process.ProcessID, spec) {
				continue
			}
			e.add(CodeVerifySpecCoverageMissing, fmt.Sprintf("%s/%s is not enumerated by a done VERIFY carrier with validated independent verifier identity", process.ProcessID, spec),
				ArtifactRef{Type: "PROCESS", ID: process.ProcessID, URL: process.ProcessURL}, "uncovered PROCESS/SPEC pair", "covered by done VERIFY with an independent accepted verifier", "comment upsert", "--type", "VERIFY")
		}
	}
}

func sortedReportKeys(values map[string][]ProcessEvidenceReport) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (e *evaluator) processEvidenceInput(processID string) *ProcessEvidenceInput {
	for index := range e.snapshot.ProcessEvidence {
		if e.snapshot.ProcessEvidence[index].Process.Comment.ID == processID {
			return &e.snapshot.ProcessEvidence[index]
		}
	}
	return nil
}

func (e *evaluator) carrierArtifact(rawURL, artifactType string) *model.Artifact {
	want := model.NormalizeURL(rawURL)
	if want == "" {
		return nil
	}
	for index := range e.snapshot.Artifacts {
		artifact := &e.snapshot.Artifacts[index]
		if artifact.Comment.Type == artifactType && artifact.Comment.Status == "done" && model.NormalizeURL(artifact.URL) == want {
			return artifact
		}
	}
	return nil
}

func exactRelatedLink(artifact model.Artifact, rawURL string) bool {
	want := model.NormalizeURL(rawURL)
	if want == "" {
		return false
	}
	for _, related := range artifact.Comment.Links["Related Comments"] {
		if model.NormalizeURL(related) == want {
			return true
		}
	}
	return false
}

func validReviewPairEvidence(input ProcessEvidenceInput, evidence ReviewEvidence) bool {
	if evidence.ProcessID != input.Process.Comment.ID || !(evidence.Done || evidence.FindingResolved) {
		return false
	}
	reviewer := strings.ToLower(strings.TrimSpace(evidence.ReviewerAgent))
	if reviewer != "" && input.AuthorAgentsBySpec[evidence.SpecID][reviewer] {
		return false
	}
	required := strings.TrimSpace(input.RequiredRevision)
	return required == "" || (evidence.Trusted && strings.EqualFold(strings.TrimSpace(evidence.SubjectRevision), required))
}

func (e *evaluator) reviewPairCovered(processID, specID string) bool {
	change := e.processEvidenceInput(processID)
	if change == nil {
		return false
	}
	specURL := change.ActiveSpecs[specID]
	for _, evidence := range change.Reviews {
		if evidence.SpecID != specID || !validReviewPairEvidence(*change, evidence) {
			continue
		}
		carrier := e.carrierArtifact(evidence.URL, "REVIEW")
		if carrier == nil || !exactRelatedLink(*carrier, change.Process.URL) || !exactRelatedLink(*carrier, specURL) {
			continue
		}
		for _, report := range e.processes {
			if report.ExecutionClass != model.ProcessExecutionReview || !stringIn(report.SatisfiedSpecs, specID) {
				continue
			}
			review := e.processEvidenceInput(report.ProcessID)
			if review == nil || !exactRelatedLink(*carrier, review.Process.URL) {
				continue
			}
			for _, reviewEvidence := range review.Reviews {
				if reviewEvidence.SpecID == specID && model.NormalizeURL(reviewEvidence.URL) == model.NormalizeURL(evidence.URL) &&
					validReviewPairEvidence(*review, reviewEvidence) {
					return true
				}
			}
		}
	}
	return false
}

func (e *evaluator) verificationPairCovered(processID, specID string) bool {
	input := e.processEvidenceInput(processID)
	if input == nil {
		return false
	}
	for _, evidence := range input.Verifications {
		if evidence.ProcessID != processID || evidence.SpecID != specID || !evidence.Done || !evidence.TestEvidence {
			continue
		}
		required := strings.TrimSpace(input.RequiredRevision)
		if required != "" && (!evidence.Trusted || !strings.EqualFold(strings.TrimSpace(evidence.SubjectRevision), required)) {
			continue
		}
		carrier := e.carrierArtifact(evidence.URL, "VERIFY")
		if carrier != nil && exactRelatedLink(*carrier, input.Process.URL) && exactRelatedLink(*carrier, input.ActiveSpecs[specID]) {
			return true
		}
	}
	return false
}

func stringIn(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
		Target: e.snapshot.Target, Mode: e.snapshot.Mode, Artifacts: e.currentArtifacts(),
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
		if comment.Type == "" || (comment.Type == "PROCESS" && !e.processIsActive(comment.ID)) ||
			(comment.Type != "PROCESS" && comment.Status == "superseded") {
			continue
		}
		switch comment.Type {
		case "SPEC":
			activeSpecs = append(activeSpecs, artifact)
			if atLeast(e.snapshot.Target, TargetFinal) && comment.Status != "confirmed" && comment.Status != "done" {
				e.add(CodeSpecStatusInvalid, fmt.Sprintf("%s must be confirmed or done", comment.ID), artifactRef(artifact), comment.Status, "confirmed|done", "comment transition", "--id", comment.ID, "--to", "confirmed")
			}
		case "QUESTION":
			if !model.QuestionIsSatisfied(comment, e.snapshot.Answers) {
				if _, choice, _ := model.ParseChoiceModel(comment.Body); choice {
					e.add(CodeQuestionBlocked, fmt.Sprintf("%s has no effective ANSWER", comment.ID), artifactRef(artifact), comment.Status, "effective append-only ANSWER", "question answer", "--question-id", comment.ID)
				} else {
					e.add(CodeQuestionBlocked, fmt.Sprintf("%s is still blocked", comment.ID), artifactRef(artifact), comment.Status, "confirmed|done|superseded", "question resolve", "--id", comment.ID)
				}
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
			if (artifact.Comment.Type == "PROCESS" && e.processIsActive(artifact.Comment.ID)) ||
				(artifact.Comment.Type != "PROCESS" && artifact.Comment.Status != "superseded") {
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
		if !e.snapshot.Relationships.Observed {
			report = model.VerifyTraceability(e.snapshot.Artifacts)
		} else {
			var indexErr error
			if e.snapshot.Relationships.Error != "" {
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

func (e *evaluator) evaluateRemoteFacts() {
	if !atLeast(e.snapshot.Target, TargetFinal) {
		return
	}
	e.evaluateFact(e.snapshot.Remote.PRChecks, CodePRChecksUnknown, CodePRChecksFailed, "pull request checks are unknown", "pull request checks failed", "checks.read", ArtifactRef{})
	e.evaluateFact(e.snapshot.Remote.ReviewFindings, CodeReviewFindingsUnknown, CodeReviewFindingsOpen, "review findings are unknown", "blocking review findings remain open", "review sync", ArtifactRef{})
	e.evaluateFact(e.snapshot.Remote.ProviderEvidence, CodeProviderEvidenceUnknown, CodeProviderEvidenceMissing, "provider evidence is unknown", "required provider evidence is missing", "evidence explain", ArtifactRef{})
	e.evaluateFact(e.snapshot.Remote.VerifyRevision.Fact, CodeVerifyRevisionUnknown, CodeVerifyRevisionInvalid,
		"VERIFY exact-revision state is unknown", "VERIFY is not bound to the exact external subject revision",
		"comment upsert", e.snapshot.Remote.VerifyRevision.Artifact, "--type", "VERIFY")
	for _, process := range e.snapshot.Remote.Processes {
		ref := ArtifactRef{Type: "PROCESS", ID: process.ProcessID, URL: process.ProcessURL}
		e.evaluateFact(process.PRLink, CodeProcessPRLinkUnknown, CodeProcessPRLinkMissing, "PROCESS PR link is unknown", "PROCESS does not link the required PR", "pr link-process", ref)
		e.evaluateFact(process.Evidence, CodeProcessEvidenceUnknown, CodeProcessEvidenceMissing, "PROCESS evidence is unknown", "PROCESS evidence is missing", "pr rationale", ref)
	}
}

func (e *evaluator) evaluateFact(fact Fact, unknownCode, failedCode, unknownMessage, failedMessage, command string, artifact ArtifactRef, args ...string) {
	if !fact.Required {
		return
	}
	if !fact.Known {
		e.add(unknownCode, unknownMessage, artifact, "unknown", expectedOr(fact.Expected, "passed"), command, args...)
		diagnostic := &e.diagnostics[len(e.diagnostics)-1]
		diagnostic.Freshness = FreshnessUnknown
		return
	}
	if fact.Passed {
		return
	}
	e.add(failedCode, failedMessage, artifact, expectedOr(fact.Current, "failed"), expectedOr(fact.Expected, "passed"), command, args...)
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
	order := map[Target]int{TargetProposal: 0, TargetDesign: 1, TargetImplement: 2, TargetFinal: 3}
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
