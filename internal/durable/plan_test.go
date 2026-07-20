package durable

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/reconcile/filecas"
)

const testBaselineRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCompilePlanIsDeterministicAndBindsExactRepresentations(t *testing.T) {
	firstSource := planSource("SPEC-002", addedIntent("SPEC-002-OP-01", "b", "B"))
	secondSource := planSource("SPEC-001", addedIntent("SPEC-001-OP-01", "a", "A"))
	input := baseCompileInput(firstSource, secondSource)
	first, err := CompilePlan(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Sources[0], input.Sources[1] = input.Sources[1], input.Sources[0]
	second, err := CompilePlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanDigest != second.PlanDigest || !reflect.DeepEqual(first, second) {
		t.Fatalf("nondeterministic plans:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if len(first.Files) != 2 || len(first.Operations) != 2 || len(first.Sources) != 2 || len(first.Blockers) != 0 {
		t.Fatalf("plan=%+v", first)
	}
	for _, operation := range first.Operations {
		if operation.BlockPostimageDigest == "" {
			t.Fatalf("operation did not bind block postimage: %+v", operation)
		}
	}
}

func TestCompilePlanReportsMissingDuplicateAndRenameTargets(t *testing.T) {
	tests := []struct {
		name     string
		intent   Intent
		baseline string
		code     string
	}{
		{name: "missing", intent: modifiedIntent("SPEC-001-OP-01", "cap", "Missing"), baseline: durableBody("cap", requirementBlockForTest("Other")), code: BlockTargetMissing},
		{name: "duplicate", intent: modifiedIntent("SPEC-001-OP-01", "cap", "Old"), baseline: durableBody("cap", requirementBlockForTest("Old"), requirementBlockForTest("Old")), code: BlockTargetAmbiguous},
		{name: "rename collision", intent: renamedIntent("SPEC-001-OP-01", "cap", "Old", "New"), baseline: durableBody("cap", requirementBlockForTest("Old"), requirementBlockForTest("New")), code: BlockRenameCollision},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baseCompileInput(planSource("SPEC-001", test.intent))
			input.BaselineFiles["issue-spec/specs/cap/spec.md"] = BaselineFile{Exists: true, Body: test.baseline}
			plan, err := CompilePlan(input)
			if err != nil {
				t.Fatal(err)
			}
			if !hasBlocker(plan, test.code) || len(plan.Files) != 0 {
				t.Fatalf("blockers=%+v files=%+v", plan.Blockers, plan.Files)
			}
		})
	}
}

func TestCompilePlanRejectsUnsafeLegacyAndTraversalPaths(t *testing.T) {
	unsafe := addedIntent("SPEC-001-OP-01", "cap", "New")
	unsafe.Operations[0].Path = "../issue-spec/specs/cap/spec.md"
	legacy := addedIntent("SPEC-002-OP-01", "cap", "Legacy")
	legacy.Operations[0].Path = "openspec/specs/cap/spec.md"
	plan, err := CompilePlan(baseCompileInput(planSource("SPEC-001", unsafe), planSource("SPEC-002", legacy)))
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(plan, BlockUnsafeTargetPath) || len(plan.Files) != 0 {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestCompilePlanPreservesUntargetedRequirements(t *testing.T) {
	input := baseCompileInput(planSource("SPEC-001", modifiedIntent("SPEC-001-OP-01", "cap", "Old")))
	input.BaselineFiles["issue-spec/specs/cap/spec.md"] = BaselineFile{Exists: true,
		Body: durableBody("cap", requirementBlockForTest("Old"), requirementBlockForTest("Untargeted"))}
	plan, err := CompilePlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || !strings.Contains(plan.Files[0].Postimage.Content, "### Requirement: Untargeted") {
		t.Fatalf("unauthorized removal in postimage: %+v", plan.Files)
	}
	if !strings.Contains(plan.Files[0].Postimage.Content, "### Requirement: Old") {
		t.Fatalf("modified target disappeared: %s", plan.Files[0].Postimage.Content)
	}
}

func TestPlanDigestBindsBaselineConfigSourceBlockAndWholeFile(t *testing.T) {
	base := baseCompileInput(planSource("SPEC-001", modifiedIntent("SPEC-001-OP-01", "cap", "Old")))
	base.BaselineFiles["issue-spec/specs/cap/spec.md"] = BaselineFile{Exists: true,
		Body: durableBody("cap", requirementBlockForTest("Old"), requirementBlockForTest("Other"))}
	want, err := CompilePlan(base)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(CompileInput) CompileInput{
		"baseline": func(value CompileInput) CompileInput { value.BaselineRevision = strings.Repeat("b", 40); return value },
		"config": func(value CompileInput) CompileInput {
			value.Workflow.ConfigDigest = strings.Repeat("c", 64)
			return value
		},
		"source": func(value CompileInput) CompileInput {
			value.Sources[0].RepresentationDigest = strings.Repeat("d", 64)
			return value
		},
		"block": func(value CompileInput) CompileInput {
			value.BaselineFiles = cloneBaselineFiles(value.BaselineFiles)
			value.BaselineFiles["issue-spec/specs/cap/spec.md"] = BaselineFile{Exists: true,
				Body: durableBody("cap", strings.Replace(requirementBlockForTest("Old"), "MUST exist", "MUST remain", 1), requirementBlockForTest("Other"))}
			return value
		},
		"file": func(value CompileInput) CompileInput {
			value.BaselineFiles = cloneBaselineFiles(value.BaselineFiles)
			file := value.BaselineFiles["issue-spec/specs/cap/spec.md"]
			file.Body = strings.Replace(file.Body, "## Purpose\n\nTest.", "## Purpose\n\nChanged exact file prefix.", 1)
			value.BaselineFiles["issue-spec/specs/cap/spec.md"] = file
			return value
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed, err := CompilePlan(mutate(base))
			if err != nil {
				t.Fatal(err)
			}
			if changed.PlanDigest == want.PlanDigest {
				t.Fatalf("%s drift did not change plan digest", name)
			}
		})
	}
}

func TestCompilePlanBindsBoundedBlockersAndSourceAmbiguity(t *testing.T) {
	var sources []SourceInput
	for index := 1; index <= 12; index++ {
		id := "SPEC-" + threeDigits(index)
		source := planSource(id, Intent{Version: 1, Kind: IntentUnchanged})
		source.IntentFound = false
		sources = append(sources, source)
	}
	duplicate := planSource("SPEC-001", Intent{Version: 1, Kind: IntentUnchanged})
	sources = append(sources, duplicate)
	plan, err := CompilePlan(baseCompileInput(sources...))
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(plan, BlockSourceAmbiguous) || !hasBlocker(plan, BlockSourceInvalid) {
		t.Fatalf("blockers=%+v", plan.Blockers)
	}
	for _, blocker := range plan.Blockers {
		if len(blocker.AffectedIDs) > maxAffectedIDs {
			t.Fatalf("unbounded blocker=%+v", blocker)
		}
		if blocker.Code == BlockSourceInvalid && blocker.TruncatedCount == 0 {
			t.Fatalf("expected truncated source blocker: %+v", blocker)
		}
	}
	compact := Compact(plan, "/tmp/durable plan.json")
	if len(compact.Blockers) == 0 || !strings.Contains(compact.Blockers[0].DetailAction, "durable-spec detail") ||
		!strings.Contains(compact.Blockers[0].DetailAction, "'/tmp/durable plan.json'") {
		t.Fatalf("compact=%+v", compact)
	}
}

func TestApplyPlanRequiresEveryFrozenAuthorityBeforeWrites(t *testing.T) {
	input := baseCompileInput(planSource("SPEC-001", addedIntent("SPEC-001-OP-01", "cap", "New")))
	plan, err := CompilePlan(input)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		digest  string
		observe AuthorityObservation
		prepare func(string)
	}{
		{name: "expected digest", digest: strings.Repeat("f", 64), observe: authorityForPlan(plan)},
		{name: "baseline drift", digest: plan.PlanDigest, observe: mutateAuthority(authorityForPlan(plan), func(value *AuthorityObservation) { value.BaselineRevision = strings.Repeat("b", 40) })},
		{name: "config drift", digest: plan.PlanDigest, observe: mutateAuthority(authorityForPlan(plan), func(value *AuthorityObservation) { value.Workflow.ConfigDigest = strings.Repeat("c", 64) })},
		{name: "source drift", digest: plan.PlanDigest, observe: mutateAuthority(authorityForPlan(plan), func(value *AuthorityObservation) { value.Sources[0].RepresentationDigest = strings.Repeat("d", 64) })},
		{name: "file drift", digest: plan.PlanDigest, observe: authorityForPlan(plan), prepare: func(root string) { mustWritePlanFile(t, root, plan.Files[0].Path, "concurrent") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.prepare != nil {
				test.prepare(root)
			}
			if _, err := ApplyPlan(root, plan, test.digest, test.observe); err == nil {
				t.Fatal("apply accepted drift")
			}
			if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(plan.Files[0].Path))); test.prepare == nil && !os.IsNotExist(statErr) {
				t.Fatalf("authority drift wrote target: %v", statErr)
			}
		})
	}
}

func TestApplyPlanRecognizesPartialAndCompleteRetry(t *testing.T) {
	plan, err := CompilePlan(baseCompileInput(
		planSource("SPEC-001", addedIntent("SPEC-001-OP-01", "a", "A")),
		planSource("SPEC-002", addedIntent("SPEC-002-OP-01", "b", "B")),
	))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	mustWritePlanFile(t, root, plan.Files[0].Path, plan.Files[0].Postimage.Content)
	result, err := ApplyPlan(root, plan, plan.PlanDigest, authorityForPlan(plan))
	if err != nil || !result.OK || result.Files.Updated != 1 || result.Files.Unchanged != 1 {
		t.Fatalf("partial result=%+v err=%v", result, err)
	}
	result, err = ApplyPlan(root, plan, plan.PlanDigest, authorityForPlan(plan))
	if err != nil || !result.OK || result.Files.Updated != 0 || result.Files.Unchanged != 2 {
		t.Fatalf("complete result=%+v err=%v", result, err)
	}
}

func TestApplyPlanDoesNotWriteBlockerOnlyPlan(t *testing.T) {
	input := baseCompileInput(planSource("SPEC-001", modifiedIntent("SPEC-001-OP-01", "cap", "Missing")))
	plan, err := CompilePlan(input)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := ApplyPlan(root, plan, plan.PlanDigest, authorityForPlan(plan)); err == nil {
		t.Fatal("apply accepted blocker-only plan")
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("blocker apply changed worktree: entries=%v err=%v", entries, err)
	}
}

func baseCompileInput(sources ...SourceInput) CompileInput {
	return CompileInput{Repository: "o/r", Proposal: 9, ProposalURL: "https://example.test/o/r/issues/9",
		BaselineRevision: testBaselineRevision,
		Workflow:         WorkflowAuthority{ConfigPath: "issue-spec/config.yaml", ConfigDigest: filecas.FileDigest([]byte("durable_specs:\n  mode: repository\n")), Mode: ModeRepository},
		Sources:          sources, BaselineFiles: map[string]BaselineFile{}}
}

func planSource(id string, intent Intent) SourceInput {
	body := "## Requirement: Change " + id + "\n\nThe change MUST be durable.\n\n### Scenario: durable\n\n- **WHEN** it changes\n- **THEN** it remains durable"
	return SourceInput{ID: id, URL: "https://example.test/issues/9#" + strings.ToLower(id), RepresentationVersion: 7,
		RepresentationDigest: filecas.FileDigest([]byte(body)), Body: body, Intent: intent, IntentFound: true}
}

func addedIntent(id, capability, requirement string) Intent {
	return Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{{ID: id, Kind: OperationAdded,
		Capability: capability, Path: "issue-spec/specs/" + capability + "/spec.md", NewRequirement: requirement,
		Projection: &Projection{Source: ProjectionInline, Requirement: &RequirementInput{Title: requirement, Text: requirement + " MUST exist."},
			Scenarios: []ScenarioInput{{Title: "exists", When: "it is projected", Then: "it exists"}}}}}}
}

func modifiedIntent(id, capability, requirement string) Intent {
	intent := addedIntent(id, capability, requirement)
	intent.Operations[0].Kind = OperationModified
	intent.Operations[0].CurrentRequirement = requirement
	intent.Operations[0].NewRequirement = ""
	intent.Operations[0].Projection.Requirement.Text = requirement + " MUST be updated."
	return intent
}

func renamedIntent(id, capability, current, next string) Intent {
	return Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{{ID: id, Kind: OperationRenamed,
		Capability: capability, Path: "issue-spec/specs/" + capability + "/spec.md", CurrentRequirement: current, NewRequirement: next}}}
}

func durableBody(capability string, blocks ...string) string {
	return "# " + capability + "\n\n## Purpose\n\nTest.\n\n## Requirements\n\n" + strings.Join(blocks, "\n\n") + "\n"
}

func requirementBlockForTest(title string) string {
	return "### Requirement: " + title + "\n\n" + title + " MUST exist.\n\n#### Scenario: exists\n\n- **WHEN** observed\n- **THEN** it exists\n\nSource SPEC comments:\n- https://example.test/source"
}

func hasBlocker(plan Plan, code string) bool {
	for _, blocker := range plan.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func threeDigits(value int) string {
	return string(rune('0'+value/100)) + string(rune('0'+(value/10)%10)) + string(rune('0'+value%10))
}

func authorityForPlan(plan Plan) AuthorityObservation {
	return AuthorityObservation{BaselineRevision: plan.BaselineRevision, Workflow: plan.Workflow,
		Sources: append([]SourceAuthority(nil), plan.Sources...)}
}

func mutateAuthority(value AuthorityObservation, mutate func(*AuthorityObservation)) AuthorityObservation {
	value.Sources = append([]SourceAuthority(nil), value.Sources...)
	mutate(&value)
	return value
}

func mustWritePlanFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cloneBaselineFiles(input map[string]BaselineFile) map[string]BaselineFile {
	result := make(map[string]BaselineFile, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
