package durable

import (
	"bytes"
	"strings"
	"testing"
)

func TestCheckAcceptsAllExactAuthorizedOperationKinds(t *testing.T) {
	tests := []struct {
		name     string
		intent   Intent
		baseline BaselineFile
	}{
		{name: "added", intent: addedIntent("SPEC-001-OP-01", "cap", "New")},
		{name: "modified", intent: modifiedIntent("SPEC-001-OP-01", "cap", "Old"),
			baseline: BaselineFile{Exists: true, Body: durableBody("cap", requirementBlockForTest("Old"), requirementBlockForTest("Other"))}},
		{name: "removed", intent: removedIntentForCheck("SPEC-001-OP-01", "cap", "Old"),
			baseline: BaselineFile{Exists: true, Body: durableBody("cap", requirementBlockForTest("Old"), requirementBlockForTest("Other"))}},
		{name: "renamed", intent: renamedIntent("SPEC-001-OP-01", "cap", "Old", "New"),
			baseline: BaselineFile{Exists: true, Body: durableBody("cap", requirementBlockForTest("Old"), requirementBlockForTest("Other"))}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baseCompileInput(planSource("SPEC-001", test.intent))
			if test.baseline.Exists {
				input.BaselineFiles["issue-spec/specs/cap/spec.md"] = test.baseline
			}
			checkInput := realizedCheckInput(t, input)
			result, err := Check(checkInput)
			if err != nil || !result.OK || len(result.Blockers) != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestCheckRejectsOmittedExtraStaleMalformedAmbiguousAndUnauthorizedProjection(t *testing.T) {
	compile := baseCompileInput(planSource("SPEC-001", modifiedIntent("SPEC-001-OP-01", "cap", "Old")))
	path := "issue-spec/specs/cap/spec.md"
	baseline := durableBody("cap", requirementBlockForTest("Old"), requirementBlockForTest("Other"))
	compile.BaselineFiles[path] = BaselineFile{Exists: true, Body: baseline}
	valid := realizedCheckInput(t, compile)
	plan, err := CompilePlan(compile)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*CheckInput)
		code   string
	}{
		{name: "omitted", code: BlockProjectionMismatch, mutate: func(value *CheckInput) {
			value.SubjectFiles[path] = BaselineFile{Exists: true, Body: baseline}
		}},
		{name: "extra target rewrite", code: BlockProjectionMismatch, mutate: func(value *CheckInput) {
			file := value.SubjectFiles[path]
			file.Body = strings.Replace(file.Body, "### Requirement: Other", "### Requirement: Unauthorized", 1)
			value.SubjectFiles[path] = file
		}},
		{name: "stale source", code: BlockProjectionMismatch, mutate: func(value *CheckInput) {
			value.Sources[0].URL = "https://example.test/issues/9#new-source"
			value.Sources[0].RepresentationDigest = strings.Repeat("e", 64)
		}},
		{name: "malformed", code: BlockSubjectMalformed, mutate: func(value *CheckInput) {
			value.SubjectFiles[path] = BaselineFile{Exists: true, Body: "not a durable spec\n"}
		}},
		{name: "ambiguous", code: BlockSubjectMalformed, mutate: func(value *CheckInput) {
			value.SubjectFiles[path] = BaselineFile{Exists: true, Body: durableBody("cap", requirementBlockForTest("Old"), requirementBlockForTest("Old"))}
		}},
		{name: "missing", code: BlockSubjectMissing, mutate: func(value *CheckInput) {
			value.SubjectFiles[path] = BaselineFile{}
		}},
		{name: "unauthorized path", code: BlockUnauthorizedChange, mutate: func(value *CheckInput) {
			value.ChangedDurablePaths = append(value.ChangedDurablePaths, "issue-spec/specs/unowned/spec.md")
		}},
		{name: "baseline block drift", code: BlockTargetMissing, mutate: func(value *CheckInput) {
			value.BaselineFiles[path] = BaselineFile{Exists: true, Body: durableBody("cap", requirementBlockForTest("Different"))}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneCheckInput(valid)
			test.mutate(&candidate)
			result, err := Check(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if result.OK || !hasCheckBlocker(result, test.code) {
				t.Fatalf("want %s result=%+v expected=%+v", test.code, result, plan.Files)
			}
		})
	}
}

func TestCheckRejectsInvalidSubjectRevisionAndIsIndependentOfPlanSidecars(t *testing.T) {
	compile := baseCompileInput(planSource("SPEC-001", addedIntent("SPEC-001-OP-01", "cap", "New")))
	input := realizedCheckInput(t, compile)
	input.SubjectRevision = "subject"
	if _, err := Check(input); err == nil || !strings.Contains(err.Error(), "exact lowercase subject revision") {
		t.Fatalf("revision error=%v", err)
	}
	// CheckInput contains only explicit tree observations and source intent; no
	// preview plan path or digest can influence the recomputation.
	input.SubjectRevision = strings.Repeat("b", 40)
	result, err := Check(input)
	if err != nil || !result.OK {
		t.Fatalf("independent check result=%+v err=%v", result, err)
	}
}

func TestCheckResultIsDeterministicBoundedAndStrictlyReadable(t *testing.T) {
	compile := baseCompileInput(planSource("SPEC-001", Intent{Version: 1, Kind: IntentUnchanged}))
	input := CheckInput{CompileInput: compile, SubjectRevision: strings.Repeat("b", 40)}
	for index := 0; index < 12; index++ {
		input.ChangedDurablePaths = append(input.ChangedDurablePaths,
			"issue-spec/specs/unowned-"+threeDigits(index+1)+"/spec.md")
	}
	result, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || len(result.Blockers) != 1 || len(result.Blockers[0].AffectedIDs) != maxAffectedIDs || result.Blockers[0].TruncatedCount != 4 {
		t.Fatalf("result=%+v", result)
	}
	data, err := CanonicalCheckResultJSON(result)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ReadCheckResult(bytes.NewReader(data))
	if err != nil || parsed.ResultDigest != result.ResultDigest {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
	compact := CompactCheckResult(result, "/tmp/check result.json")
	if len(compact.Blockers) != 1 || !strings.Contains(compact.Blockers[0].DetailAction,
		"durable-spec detail --result '/tmp/check result.json' --code unauthorized_durable_change") {
		t.Fatalf("compact=%+v", compact)
	}
	tampered := bytes.Replace(data, []byte(`"operation_count": 0`), []byte(`"operation_count": 1`), 1)
	if _, err := ReadCheckResult(bytes.NewReader(tampered)); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tampered result error=%v", err)
	}
}

func realizedCheckInput(t *testing.T, compile CompileInput) CheckInput {
	t.Helper()
	plan, err := CompilePlan(compile)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) != 0 {
		t.Fatalf("compile blockers=%+v", plan.Blockers)
	}
	input := CheckInput{CompileInput: compile, SubjectRevision: strings.Repeat("b", 40),
		SubjectFiles: map[string]BaselineFile{}}
	for _, mutation := range plan.Files {
		input.SubjectFiles[mutation.Path] = BaselineFile{Exists: true, Body: mutation.Postimage.Content}
		input.ChangedDurablePaths = append(input.ChangedDurablePaths, mutation.Path)
	}
	return input
}

func removedIntentForCheck(id, capability, requirement string) Intent {
	return Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{{ID: id, Kind: OperationRemoved,
		Capability: capability, Path: "issue-spec/specs/" + capability + "/spec.md", CurrentRequirement: requirement}}}
}

func cloneCheckInput(input CheckInput) CheckInput {
	result := input
	result.Sources = append([]SourceInput(nil), input.Sources...)
	result.BaselineFiles = cloneBaselineFiles(input.BaselineFiles)
	result.SubjectFiles = cloneBaselineFiles(input.SubjectFiles)
	result.ChangedDurablePaths = append([]string(nil), input.ChangedDurablePaths...)
	return result
}

func hasCheckBlocker(result CheckResult, code string) bool {
	for _, blocker := range result.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}
