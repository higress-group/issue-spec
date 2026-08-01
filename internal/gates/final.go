package gates

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/finalization"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/relationships"
)

const (
	CodeFinalSubjectUnknown       = "final.subject.unknown"
	CodeFinalSubjectInvalid       = "final.subject.invalid"
	CodeFinalSelectionInvalid     = "final.selection.invalid"
	CodeFinalPlanningInvalid      = "final.planning.invalid"
	CodeFinalEvidenceInvalid      = "final.evidence.invalid"
	CodeFinalVerificationRequired = "final.verification.required"
	CodeFinalRequiredTestMissing  = "final.test.missing_or_failed"
	CodeFinalRequiredCheckMissing = "final.check.missing_or_failed"
)

type finalPlanning struct {
	specs        map[string]model.Artifact
	tasks        map[string]model.Artifact
	processes    map[string]model.Artifact
	processSpecs map[string][]string
}

func evaluateMinimalFinal(snapshot Snapshot) Report {
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
	for _, diagnostic := range selection.Diagnostics {
		e.add(CodeFinalSelectionInvalid, fmt.Sprintf("PROCESS selection %s: %s", diagnostic.Code, diagnostic.Message),
			ArtifactRef{Type: "PROCESS", ID: diagnostic.ProcessID, URL: diagnostic.URL}, "invalid", "one unambiguous active-carrier set", "finalize detail")
	}
	planning := e.evaluateFinalPlanning()
	e.evaluateFinalSubject(planning)
	e.evaluateFinalEvidence(planning)
	e.sort()
	ready := true
	for _, diagnostic := range e.diagnostics {
		if diagnostic.Blocking {
			ready = false
			break
		}
	}
	return Report{Ready: ready, Target: TargetFinal, Mode: snapshot.Mode, PointInTime: snapshot.Mode == ModeForecast,
		Diagnostics: e.diagnostics, Processes: e.processes}
}

func (e *evaluator) evaluateFinalPlanning() finalPlanning {
	result := finalPlanning{specs: map[string]model.Artifact{}, tasks: map[string]model.Artifact{},
		processes: map[string]model.Artifact{}, processSpecs: map[string][]string{}}
	if e.snapshot.Relationships.Required && (!e.snapshot.Relationships.Observed || e.snapshot.Relationships.Error != "") {
		current := e.snapshot.Relationships.Error
		if current == "" {
			current = "not observed"
		}
		e.add(CodeFinalPlanningInvalid, "canonical relationship index: "+current,
			ArtifactRef{}, "invalid", "bounded canonical relationship index", "relationship-detail")
	}
	seen := map[string]model.Artifact{}
	for _, artifact := range e.snapshot.Artifacts {
		tc := artifact.Comment
		if _, choice, _ := model.ParseChoiceModel(tc.Body); tc.Type == "QUESTION" && choice &&
			tc.Status != "superseded" && !model.QuestionIsSatisfied(tc, e.snapshot.Answers) {
			e.add(CodeQuestionBlocked, fmt.Sprintf("%s has no effective ANSWER", tc.ID), artifactRef(artifact),
				tc.Status, "effective append-only ANSWER", "question answer", "--question-id", tc.ID)
		}
		if tc.Type != "SPEC" && tc.Type != "TASK" && tc.Type != "PROCESS" {
			continue
		}
		if tc.ID == "" || (tc.Type == "PROCESS" && !e.processIsActive(tc.ID)) ||
			(tc.Type != "PROCESS" && tc.Status == "superseded") {
			continue
		}
		if previous, duplicate := seen[tc.ID]; duplicate {
			e.add(CodeFinalPlanningInvalid, fmt.Sprintf("duplicate logical id %s on %s and %s", tc.ID, previous.URL, artifact.URL),
				ArtifactRef{Type: tc.Type, ID: tc.ID, URL: artifact.URL}, "duplicate", "unique typed identity", "comment get", "--id", tc.ID)
		}
		seen[tc.ID] = artifact
		if err := model.ValidateTypedIdentity(tc.Type, tc.ID); err != nil {
			e.add(CodeArtifactNoncanonical, err.Error(), artifactRef(artifact), tc.ID, "canonical typed identity",
				"comment generate", "--type", tc.Type, "--id", tc.ID)
		}
		for _, parseError := range tc.Errors {
			e.add(CodeArtifactNoncanonical, parseError, artifactRef(artifact), "parse error", "canonical typed artifact",
				"comment generate", "--type", tc.Type, "--id", tc.ID)
		}
		for _, diagnostic := range model.ValidateArtifact(artifact) {
			e.add(CodeArtifactNoncanonical, diagnostic.Message,
				ArtifactRef{Type: diagnostic.Type, ID: diagnostic.ID, URL: diagnostic.URL}, diagnostic.Element, "canonical", "comment generate", "--type", diagnostic.Type, "--id", diagnostic.ID)
		}
		switch tc.Type {
		case "SPEC":
			result.specs[tc.ID] = artifact
		case "TASK":
			result.tasks[tc.ID] = artifact
		case "PROCESS":
			result.processes[tc.ID] = artifact
		}
	}
	if len(result.specs) == 0 {
		e.add(CodeSpecRequired, "at least one active SPEC is required", ArtifactRef{}, "0", ">=1", "comment generate", "--type", "SPEC")
	}
	if len(result.tasks) == 0 {
		e.add(CodeTaskRequired, "at least one active TASK is required", ArtifactRef{}, "0", ">=1", "comment generate", "--type", "TASK")
	}
	if len(result.processes) == 0 {
		e.add(CodeProcessRequired, "at least one active PROCESS is required", ArtifactRef{}, "0", ">=1", "comment generate", "--type", "PROCESS")
	}
	taskSpecs := map[string]map[string]bool{}
	for taskID, task := range result.tasks {
		covered := model.TypedSectionList(task.Comment.Body, "### Covers")
		if len(covered) == 0 {
			e.add(CodeFinalPlanningInvalid, fmt.Sprintf("%s must cover at least one active SPEC", taskID), artifactRef(task), "empty Covers", ">=1 active SPEC", "comment generate", "--type", "TASK", "--id", taskID)
			continue
		}
		taskSpecs[taskID] = map[string]bool{}
		for _, specID := range covered {
			spec, ok := result.specs[specID]
			if !ok || taskSpecs[taskID][specID] {
				e.add(CodeFinalPlanningInvalid, fmt.Sprintf("%s has invalid or duplicate SPEC coverage %s", taskID, specID), artifactRef(task), specID, "one active SPEC", "comment generate", "--type", "TASK", "--id", taskID)
				continue
			}
			linked := linksIdentifyArtifact(task.Comment.Links["Related Comments"], spec)
			if e.snapshot.Relationships.Required {
				linked = false
			}
			if e.snapshot.Relationships.Required && e.snapshot.Relationships.Observed && e.snapshot.Relationships.Error == "" {
				linked = relationshipIndexHas(e.snapshot.Relationships.Index, relationships.TaskCoversSpec, taskID, specID)
			}
			if !linked {
				e.add(CodeFinalPlanningInvalid, fmt.Sprintf("%s coverage for %s lacks its exact SPEC URL", taskID, specID),
					artifactRef(task), "missing", spec.URL, "comment upsert", "--type", "TASK", "--id", taskID)
				continue
			}
			taskSpecs[taskID][specID] = true
		}
	}
	for processID, process := range result.processes {
		parents := model.TypedSectionList(process.Comment.Body, "### Parent TASK")
		if len(parents) != 1 || result.tasks[parents[0]].Comment.ID == "" {
			e.add(CodeFinalPlanningInvalid, fmt.Sprintf("%s must name exactly one active Parent TASK", processID), artifactRef(process), strings.Join(parents, ","), "one active TASK", "comment generate", "--type", "PROCESS", "--id", processID)
			continue
		}
		parentID := parents[0]
		linked := linksIdentifyArtifact(process.Comment.Links["Related Comments"], result.tasks[parentID])
		if e.snapshot.Relationships.Required {
			linked = false
		}
		if e.snapshot.Relationships.Required && e.snapshot.Relationships.Observed && e.snapshot.Relationships.Error == "" {
			linked = relationshipIndexHas(e.snapshot.Relationships.Index, relationships.ProcessParentTask, processID, parentID)
		}
		if !linked {
			e.add(CodeFinalPlanningInvalid, fmt.Sprintf("%s Parent TASK %s lacks its exact TASK URL", processID, parentID),
				artifactRef(process), "missing", result.tasks[parentID].URL, "comment upsert", "--type", "PROCESS", "--id", processID)
			continue
		}
		selectors := map[string]bool{}
		for _, id := range model.TypedSectionList(process.Comment.Body, "### Covers") {
			if strings.HasPrefix(id, "SPEC-") {
				selectors[id] = true
			}
		}
		if process.Comment.Assignment != nil {
			for _, selector := range process.Comment.Assignment.ScenarioSelectors {
				selectors[strings.TrimSpace(selector.SpecID)] = true
			}
		}
		if len(selectors) == 0 {
			for specID := range taskSpecs[parentID] {
				selectors[specID] = true
			}
		}
		for specID := range selectors {
			if !taskSpecs[parentID][specID] {
				e.add(CodeFinalPlanningInvalid, fmt.Sprintf("%s selector %s is outside Parent TASK %s coverage", processID, specID, parentID), artifactRef(process), specID, "selector contained by Parent TASK", "comment generate", "--type", "PROCESS", "--id", processID)
				continue
			}
			result.processSpecs[processID] = append(result.processSpecs[processID], specID)
		}
		sort.Strings(result.processSpecs[processID])
	}
	return result
}

func relationshipIndexHas(index relationships.Index, kind relationships.Kind, ownerID, targetID string) bool {
	for _, edge := range index.Edges {
		if edge.Kind == kind && edge.Owner.ID == ownerID && edge.Target.ID == targetID {
			return true
		}
	}
	return false
}

func (e *evaluator) evaluateFinalSubject(planning finalPlanning) {
	subject := e.snapshot.FinalEvidence.Subject
	if !e.snapshot.FinalEvidence.Observed || !subject.Required || !subject.Known {
		e.add(CodeFinalSubjectUnknown, "current code subject identity and revision were not collected", ArtifactRef{}, "unknown", "one authoritative exact-current subject", "verify")
		e.diagnostics[len(e.diagnostics)-1].Freshness = FreshnessUnknown
		return
	}
	kind := strings.TrimSpace(subject.Kind)
	validSource := (kind == "pull_request" && strings.HasPrefix(subject.Source, "github-pull-request-head:")) ||
		(kind == "code_change" && strings.HasPrefix(subject.Source, "native-authoritative-ledger:"))
	if !subject.Trusted || (kind != "pull_request" && kind != "code_change") || !validSource || strings.TrimSpace(subject.URL) == "" ||
		strings.TrimSpace(subject.Revision) == "" || strings.TrimSpace(subject.Source) == "" {
		e.add(CodeFinalSubjectInvalid, "current code subject identity or revision is incomplete or untrusted", ArtifactRef{}, "invalid", "authoritative exact-current subject", "verify")
		return
	}
	for processID, process := range planning.processes {
		values := nonEmptyLinks(process.Comment.Links["PR"])
		if len(values) != 1 || model.NormalizeURL(values[0]) != model.NormalizeURL(subject.URL) {
			e.add(CodeProcessPRLinkMissing, fmt.Sprintf("%s does not carry the exact current code subject", processID), artifactRef(process), strings.Join(values, ","), subject.URL, "pr link-process", "--process", processID)
		}
	}
}

func (e *evaluator) evaluateFinalEvidence(planning finalPlanning) {
	facts := e.snapshot.FinalEvidence
	if !facts.Observed || !facts.Index.Required || !facts.Index.Known {
		e.add(CodeFinalEvidenceInvalid, "canonical evidence index was not collected", ArtifactRef{}, "unknown", "validated bounded exact-current index", "verify")
		e.diagnostics[len(e.diagnostics)-1].Freshness = FreshnessUnknown
		return
	}
	if !facts.Index.Passed {
		e.add(CodeFinalEvidenceInvalid, "canonical evidence index is invalid: "+expectedOr(facts.Index.Current, "validation failed"), ArtifactRef{}, expectedOr(facts.Index.Current, "invalid"), "valid exact-current index", "verify")
		return
	}
	revision := strings.TrimSpace(facts.Subject.Revision)
	inputs := map[string]ProcessEvidenceInput{}
	for _, input := range e.snapshot.ProcessEvidence {
		if planning.processes[input.Process.Comment.ID].Comment.ID == "" {
			continue
		}
		inputs[input.Process.Comment.ID] = input
		report := EvaluateProcessEvidence(input, TargetFinal, e.snapshot.Mode)
		e.processes = append(e.processes, report)
	}
	sort.Slice(e.processes, func(i, j int) bool { return e.processes[i].ProcessID < e.processes[j].ProcessID })
	type evidenceKey struct {
		process, spec string
		kind          FinalEvidenceKind
	}
	indexed := map[evidenceKey][]FinalEvidenceRecord{}
	activeReceipts := map[string]string{}
	for _, record := range facts.Records {
		validKind := record.Kind == FinalEvidenceReview || record.Kind == FinalEvidenceVerification ||
			record.Kind == FinalEvidenceTest || record.Kind == FinalEvidenceCheck
		if planning.processes[record.ProcessID].Comment.ID == "" || planning.specs[record.SpecID].Comment.ID == "" ||
			!validKind || record.EvidenceID == "" || record.Source == "" || record.SubjectRevision != revision {
			e.add(CodeFinalEvidenceInvalid, fmt.Sprintf("evidence %s has stale, unknown, or conflicting identity", record.EvidenceID),
				ArtifactRef{Type: "PROCESS", ID: record.ProcessID}, record.SubjectRevision, revision, "verify")
			continue
		}
		if err := validateFinalEvidenceAssignment(record, inputs, revision, activeReceipts); err != nil {
			e.add(CodeFinalEvidenceInvalid, fmt.Sprintf("evidence %s has invalid active assignment identity: %v", record.EvidenceID, err),
				ArtifactRef{Type: "PROCESS", ID: record.ProcessID}, err.Error(), "active exact assignment generation", "verify")
			continue
		}
		key := evidenceKey{record.ProcessID, record.SpecID, record.Kind}
		indexed[key] = append(indexed[key], record)
	}

	changePairs := map[string][]string{}
	for processID, process := range planning.processes {
		class := model.ParseProcessExecutionClass(process.Comment.ID, process.URL, process.Comment.Body)
		if class.Blocking() {
			e.add(CodeProcessExecutionClassInvalid, fmt.Sprintf("%s has invalid execution class", processID), artifactRef(process), "invalid", "known class", "comment generate", "--type", "PROCESS", "--id", processID)
			continue
		}
		if class.Class != model.ProcessExecutionChangeBearing && class.Class != model.ProcessExecutionExternal {
			continue
		}
		report := processReport(e.processes, processID)
		for _, specID := range planning.processSpecs[processID] {
			if report == nil || !stringIn(report.SatisfiedSpecs, specID) {
				e.add(CodeProcessCarrierMissing, fmt.Sprintf("%s lacks exact-current code evidence for %s", processID, specID), artifactRef(process), "missing or stale", revision, "pr rationale")
				continue
			}
			changePairs[processID] = append(changePairs[processID], specID)
		}
	}
	for processID, specs := range changePairs {
		process := planning.processes[processID]
		for _, specID := range specs {
			reviews := independentEvidence(indexed[evidenceKey{processID, specID, FinalEvidenceReview}])
			if len(reviews) == 0 {
				e.add(CodeProcessReviewRequired, fmt.Sprintf("%s/%s lacks exact-current independent REVIEW evidence", processID, specID), artifactRef(process), "missing or stale", revision, "review sync")
			}
			verifications := independentEvidence(indexed[evidenceKey{processID, specID, FinalEvidenceVerification}])
			if len(verifications) == 0 {
				e.add(CodeFinalVerificationRequired, fmt.Sprintf("%s/%s lacks exact-current independent VERIFY evidence", processID, specID), artifactRef(process), "missing or stale", revision, "verify submit")
			}
		}
	}
	for processID, process := range planning.processes {
		if process.Comment.Assignment == nil {
			continue
		}
		for _, specID := range planning.processSpecs[processID] {
			for _, required := range process.Comment.Assignment.RequiredTests {
				matched, sameID := matchingRequiredTestEvidence(indexed[evidenceKey{processID, specID, FinalEvidenceTest}], required, revision)
				if !matched {
					current := "missing or failed"
					if sameID {
						current = "stable selector mismatch"
					}
					e.add(CodeFinalRequiredTestMissing, fmt.Sprintf("%s/%s is missing successful assigned test %s", processID, specID, required.ID), artifactRef(process), current, "exact selector "+required.ID, "verify submit")
				}
			}
			for _, required := range process.Comment.Assignment.RequiredChecks {
				name := required.Provider + "\x00" + required.Name
				if !evidenceNamed(indexed[evidenceKey{processID, specID, FinalEvidenceCheck}], name) {
					e.add(CodeFinalRequiredCheckMissing, fmt.Sprintf("%s/%s is missing successful assigned check %s/%s", processID, specID, required.Provider, required.Name), artifactRef(process), "missing or failed", required.Provider+"/"+required.Name, "verify submit")
				}
			}
		}
	}
	e.evaluateFact(e.snapshot.Remote.ReviewFindings, CodeReviewFindingsUnknown, CodeReviewFindingsOpen,
		"blocking review findings are unknown", "blocking review findings remain open", "review sync", ArtifactRef{})
	// Self-hosted subjects expose the canonical VERIFY carrier revision as a
	// scoped fact. It is part of exact-current verification identity, not a
	// lifecycle/status requirement, and remains optional for provider adapters
	// whose accepted receipt already supplies the immutable revision.
	e.evaluateFact(e.snapshot.Remote.VerifyRevision.Fact, CodeVerifyRevisionUnknown, CodeVerifyRevisionInvalid,
		"verification subject revision is unknown", "verification subject revision does not match the current code subject",
		"comment upsert", e.snapshot.Remote.VerifyRevision.Artifact, "--type", "VERIFY")
}

func validateFinalEvidenceAssignment(record FinalEvidenceRecord, inputs map[string]ProcessEvidenceInput,
	expectedRevision string, activeReceipts map[string]string) error {
	hasAssignment := record.AssignmentProcessID != "" || record.ReceiptID != "" || record.ReceiptDigest != "" ||
		record.AssignmentID != "" || record.AssignmentDigest != "" || record.AssignmentGeneration != 0 || record.AssignmentRole != ""
	if !hasAssignment {
		if record.Kind == FinalEvidenceTest || record.Kind == FinalEvidenceVerification {
			return errors.New("role-owned evidence lacks explicit assignment authority")
		}
		if record.AssignedSelector != nil || record.ResolvedRevision != "" || record.ExecutedCommand != "" ||
			record.CheckSelector != nil {
			return errors.New("selector execution identity lacks accepted assignment authority")
		}
		return nil
	}
	if record.AssignmentProcessID == "" || record.ReceiptID == "" || record.ReceiptDigest == "" ||
		record.AssignmentID == "" || record.AssignmentDigest == "" || record.AssignmentGeneration == 0 {
		return errors.New("accepted assignment or receipt identity is incomplete")
	}
	if err := validateFinalRoleSource(record); err != nil {
		return err
	}
	input, found := inputs[record.AssignmentProcessID]
	if !found || input.ActiveAssignment == nil {
		return errors.New("source PROCESS has no active assignment authority")
	}
	active := input.ActiveAssignment
	if active.ProcessID != record.AssignmentProcessID || active.AssignmentID != record.AssignmentID ||
		active.AssignmentDigest != record.AssignmentDigest || active.Generation != record.AssignmentGeneration ||
		active.SubjectRevision != record.SubjectRevision || record.SubjectRevision != expectedRevision {
		return errors.New("receipt does not join to the active assignment id, digest, generation, and subject")
	}
	if active.Role != record.AssignmentRole {
		return fmt.Errorf("%s evidence cannot use %s assignment", record.Kind, active.Role)
	}
	slot := strings.Join([]string{record.AssignmentProcessID, record.AssignmentID, record.AssignmentDigest,
		fmt.Sprint(record.AssignmentGeneration)}, "\x00")
	receipt := record.ReceiptID + "\x00" + record.ReceiptDigest
	if previous, exists := activeReceipts[slot]; exists && previous != receipt {
		return errors.New("duplicate active assignment generation has conflicting receipt identities")
	}
	activeReceipts[slot] = receipt
	if record.Kind != FinalEvidenceCheck && record.CheckSelector != nil {
		return errors.New("non-check evidence carries check selector identity")
	}
	if record.Kind != FinalEvidenceTest {
		if record.AssignedSelector != nil || record.ResolvedRevision != "" || record.ExecutedCommand != "" {
			return errors.New("non-test evidence carries test execution identity")
		}
		if record.Kind == FinalEvidenceCheck {
			if record.CheckSelector == nil || strings.TrimSpace(record.CheckSelector.Provider) == "" ||
				strings.TrimSpace(record.CheckSelector.Name) == "" ||
				record.Name != record.CheckSelector.Provider+"\x00"+record.CheckSelector.Name {
				return errors.New("check evidence does not preserve its exact accepted selector identity")
			}
		}
		return nil
	}
	selector, err := finalEvidenceTestSelector(record, expectedRevision)
	if err != nil {
		return err
	}
	assigned := false
	for _, required := range active.RequiredTests {
		if required.ID != selector.ID {
			continue
		}
		assigned = true
		if !assignment.TestSelectorIdentityEqual(required, selector) {
			return errors.New("stable selector differs from the active assignment")
		}
	}
	if !assigned {
		return errors.New("test selector is not required by the active assignment")
	}
	return nil
}

func validateFinalRoleSource(record FinalEvidenceRecord) error {
	wantRole, wantPrefix := assignment.RoleVerification, "accepted-verification-receipt:"
	switch record.Kind {
	case FinalEvidenceReview:
		wantRole, wantPrefix = assignment.RoleReview, "accepted-review-receipt:"
	case FinalEvidenceVerification, FinalEvidenceCheck:
	case FinalEvidenceTest:
		if record.AssignmentRole == assignment.RoleReview {
			wantRole, wantPrefix = assignment.RoleReview, "accepted-review-receipt:"
		}
	default:
		return fmt.Errorf("role-owned evidence has unsupported kind %q", record.Kind)
	}
	if record.AssignmentRole != wantRole || !strings.HasPrefix(record.Source, wantPrefix) {
		return fmt.Errorf("evidence has incompatible assignment role %q, kind %q, or source %q",
			record.AssignmentRole, record.Kind, record.Source)
	}
	return nil
}

func finalEvidenceTestSelector(record FinalEvidenceRecord, expectedRevision string) (assignment.TestSelector, error) {
	selector := assignment.TestSelector{ID: record.Name, Command: record.ExecutedCommand}
	if record.AssignedSelector == nil {
		if record.ResolvedRevision != "" {
			return assignment.TestSelector{}, errors.New("literal selector carries resolved revision")
		}
		if err := assignment.ValidateTestSelectorRevisionContract(record.AssignmentRole, expectedRevision, selector); err != nil {
			return assignment.TestSelector{}, err
		}
		return selector, nil
	}
	selector = *record.AssignedSelector
	if selector.ID != record.Name {
		return assignment.TestSelector{}, errors.New("assigned selector id differs from evidence name")
	}
	if record.ResolvedRevision == "" {
		return assignment.TestSelector{}, errors.New("bound selector lacks resolved revision")
	}
	if err := assignment.ValidateTestSelectorRevisionContract(record.AssignmentRole, expectedRevision, selector); err != nil {
		return assignment.TestSelector{}, err
	}
	resolved, err := assignment.ResolveTestSelector(selector, record.ResolvedRevision)
	if err != nil || record.ResolvedRevision != expectedRevision || record.ExecutedCommand != resolved.Command {
		return assignment.TestSelector{}, errors.New("bound selector does not reproduce the exact executed command")
	}
	return selector, nil
}

func matchingRequiredTestEvidence(records []FinalEvidenceRecord, required assignment.TestSelector,
	expectedRevision string) (bool, bool) {
	sameID := false
	for _, record := range records {
		if record.Name != required.ID {
			continue
		}
		sameID = true
		selector, err := finalEvidenceTestSelector(record, expectedRevision)
		if err == nil && assignment.TestSelectorIdentityEqual(selector, required) {
			return true, true
		}
	}
	return false, sameID
}

func processReport(reports []ProcessEvidenceReport, processID string) *ProcessEvidenceReport {
	for index := range reports {
		if reports[index].ProcessID == processID {
			return &reports[index]
		}
	}
	return nil
}

func independentEvidence(records []FinalEvidenceRecord) []FinalEvidenceRecord {
	var result []FinalEvidenceRecord
	for _, record := range records {
		if record.Independent {
			result = append(result, record)
		}
	}
	return result
}

func evidenceNamed(records []FinalEvidenceRecord, name string) bool {
	for _, record := range records {
		if record.Name == name {
			return true
		}
	}
	return false
}

func nonEmptyLinks(values []string) []string {
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !strings.EqualFold(value, "N/A") {
			result = append(result, value)
		}
	}
	return result
}

func linksIdentifyArtifact(values []string, artifact model.Artifact) bool {
	want := map[string]bool{}
	for _, value := range []string{artifact.URL, artifact.APIURL} {
		if normalized := model.NormalizeURL(value); normalized != "" {
			want[normalized] = true
		}
	}
	for _, value := range values {
		if want[model.NormalizeURL(value)] {
			return true
		}
	}
	return false
}
