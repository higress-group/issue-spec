package durable

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sectionTarget = "issue-spec/specs/cap/spec.md"
const sectionReferences = "- [Implementation](https://example.test/old)"
const sectionNotes = "## Notes\n\nLeave these bytes unchanged.  \n"

func sectionOperation(id, section, before, after string) Operation {
	return Operation{ID: id, Kind: OperationSectionModified, Capability: "cap", Path: sectionTarget,
		Section: section, Before: &before, Projection: &Projection{Source: ProjectionInline, Text: &after}}
}

func sectionCompileInput(operations ...Operation) CompileInput {
	input := baseCompileInput(planSource("SPEC-001", Intent{Version: 1, Kind: IntentOperations, Operations: operations}))
	input.BaselineFiles[sectionTarget] = BaselineFile{Exists: true, Body: durableBody("cap", requirementBlockForTest("Keep")) + "\n## Implementation references\n\n" + sectionReferences + "\n\n" + sectionNotes}
	return input
}

func TestSectionIntentStrictShapeAndBodies(t *testing.T) {
	valid := sectionOperation("SPEC-001-OP-01", "Purpose", "Test.", "A precise purpose.")
	tests := map[string]func(*Operation){
		"unknown kind":             func(op *Operation) { op.Kind = "SECTION_ADDED" },
		"arbitrary section":        func(op *Operation) { op.Section = "Notes" },
		"requirement identity":     func(op *Operation) { op.CurrentRequirement = "Keep" },
		"new requirement identity": func(op *Operation) { op.NewRequirement = "Keep" },
		"missing before":           func(op *Operation) { op.Before = nil },
		"missing projection":       func(op *Operation) { op.Projection = nil },
		"missing text":             func(op *Operation) { op.Projection.Text = nil },
		"current spec":             func(op *Operation) { op.Projection.Source = ProjectionCurrentSpec },
		"inline requirement":       func(op *Operation) { op.Projection.Requirement = &RequirementInput{Title: "Keep", Text: "MUST keep"} },
		"inline scenario":          func(op *Operation) { op.Projection.Scenarios = []ScenarioInput{{Title: "unexpected"}} },
		"old operation before": func(op *Operation) {
			op.Kind = OperationRemoved
			op.Section = ""
			op.CurrentRequirement = "Keep"
			op.Projection = nil
		},
		"old operation section": func(op *Operation) {
			op.Kind = OperationRemoved
			op.Before = nil
			op.CurrentRequirement = "Keep"
			op.Projection = nil
		},
		"old projection text": func(op *Operation) {
			*op = modifiedIntent(op.ID, "cap", "Keep").Operations[0]
			s := "forbidden"
			op.Projection.Text = &s
		},
	}
	for _, body := range []string{"", " ", "\nleading", "trailing\n", "CR\rLF", strings.Repeat("x", 4097), "# Title", "Title\n---", "## Purpose ###", "### Requirement: hidden"} {
		for _, field := range []string{"before", "text"} {
			tests[field+"/"+body] = func(op *Operation) {
				if field == "before" {
					op.Before = &body
				} else {
					op.Projection.Text = &body
				}
			}
		}
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			op := valid
			projection := *valid.Projection
			op.Projection = &projection
			mutate(&op)
			intent := Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{op}}
			data, _ := json.Marshal(intent)
			_, found, err := ParseSpecIntent("## Durable Intent\n\n```json\n"+string(data)+"\n```", ValidationOptions{SpecID: "SPEC-001"})
			if !found || err == nil {
				t.Fatalf("invalid intent accepted: %s err=%v", data, err)
			}
		})
	}
	intent := Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{valid}}
	data, err := CanonicalJSON(intent, ValidationOptions{SpecID: "SPEC-001"})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{strings.Replace(string(data), `"section":`, `"unknown": true, "section":`, 1), string(data) + ` {}`} {
		if _, _, err := ParseSpecIntent("## Durable Intent\n\n```json\n"+payload+"\n```", ValidationOptions{}); err == nil {
			t.Fatal("non-strict JSON accepted")
		}
	}
	normalized, err := NormalizeIntent(intent, ValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	*normalized.Operations[0].Before = "changed"
	*normalized.Operations[0].Projection.Text = "changed"
	if *valid.Before != "Test." || *valid.Projection.Text != "A precise purpose." {
		t.Fatal("normalization aliases caller bodies")
	}
}

func TestSectionOperationsComposeWithRequirementsAndKeepFullFileAuthority(t *testing.T) {
	purpose := "A new purpose with  \nhard-break whitespace.\n\n```markdown\n## Requirements\n```"
	refs := "- [Implementation](https://example.test/new)"
	input := sectionCompileInput(sectionOperation("SPEC-001-OP-01", "Purpose", "Test.", purpose), sectionOperation("SPEC-001-OP-02", "Implementation references", sectionReferences, refs))
	// A Requirement with the same name is a different target namespace.
	input.Sources[0].Intent.Operations = append(input.Sources[0].Intent.Operations, addedIntent("SPEC-001-OP-03", "cap", "Purpose").Operations[0])
	baseline := input.BaselineFiles[sectionTarget].Body
	plan, err := CompilePlan(input)
	if err != nil || len(plan.Blockers) != 0 || len(plan.Files) != 1 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	body := plan.Files[0].Postimage.Content
	if !strings.Contains(body, "## Purpose\n\n"+purpose+"\n\n## Requirements") || !strings.Contains(body, requirementBlockForTest("Keep")) || !strings.Contains(body, "### Requirement: Purpose") || !strings.HasSuffix(body, "## Implementation references\n\n"+refs+"\n\n"+sectionNotes) {
		t.Fatalf("incorrect mixed projection:\n%s", body)
	}
	for _, op := range plan.Operations[:2] {
		if op.Section == "" || op.BlockPreimageDigest == "" || op.BlockPostimageDigest == "" {
			t.Fatalf("section audit missing: %+v", op)
		}
	}
	root := t.TempDir()
	mustWritePlanFile(t, root, sectionTarget, baseline)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := ApplyPlan(root, plan, plan.PlanDigest, authorityForPlan(plan))
		if err != nil || !result.OK || attempt == 1 && result.Files.Unchanged != 1 {
			t.Fatalf("apply %d: %+v %v", attempt, result, err)
		}
	}
	check := CheckInput{CompileInput: input, SubjectRevision: strings.Repeat("b", 40), SubjectFiles: map[string]BaselineFile{sectionTarget: {Exists: true, Body: body}}, ChangedDurablePaths: []string{sectionTarget}}
	if result, err := Check(check); err != nil || !result.OK {
		t.Fatalf("check=%+v err=%v", result, err)
	}
	for name, altered := range map[string]string{"title": strings.Replace(body, "# cap", "# Other", 1), "notes": strings.Replace(body, "Leave these bytes", "Unexpected bytes", 1), "requirement": strings.Replace(body, "Keep MUST exist.", "Keep MUST disappear.", 1), "extra blank": strings.Replace(body, "## Purpose\n\n", "## Purpose\n\n\n", 1), "unauthorized purpose": strings.Replace(body, purpose, "Different purpose.", 1)} {
		t.Run(name, func(t *testing.T) {
			check.SubjectFiles[sectionTarget] = BaselineFile{Exists: true, Body: altered}
			if result, err := Check(check); err != nil || result.OK {
				t.Fatalf("unauthorized edit check=%+v err=%v", result, err)
			}
		})
	}
	for _, drift := range []string{"source digest", "source version", "baseline", "file"} {
		t.Run(drift, func(t *testing.T) {
			root := t.TempDir()
			current := baseline
			observed := authorityForPlan(plan)
			switch drift {
			case "source digest":
				observed.Sources[0].RepresentationDigest = strings.Repeat("d", 64)
			case "source version":
				observed.Sources[0].RepresentationVersion++
			case "baseline":
				observed.BaselineRevision = strings.Repeat("c", 40)
			case "file":
				current = strings.Replace(baseline, "Keep MUST exist.", "Concurrent edit.", 1)
			}
			mustWritePlanFile(t, root, sectionTarget, current)
			if _, err := ApplyPlan(root, plan, plan.PlanDigest, observed); err == nil {
				t.Fatal("drift accepted")
			}
			data, err := os.ReadFile(filepath.Join(root, sectionTarget))
			if err != nil || string(data) != current {
				t.Fatalf("drift changed bytes: %v", err)
			}
		})
	}
}

func TestSectionOperationsRejectUnsafeTargetsAndBoundaries(t *testing.T) {
	tests := map[string]func(*CompileInput){
		"missing file":    func(in *CompileInput) { delete(in.BaselineFiles, sectionTarget) },
		"before mismatch": func(in *CompileInput) { s := "Test"; in.Sources[0].Intent.Operations[0].Before = &s },
		"duplicate same source": func(in *CompileInput) {
			op := in.Sources[0].Intent.Operations[0]
			op.ID = "SPEC-001-OP-02"
			in.Sources[0].Intent.Operations = append(in.Sources[0].Intent.Operations, op)
		},
		"duplicate cross source": func(in *CompileInput) {
			op := in.Sources[0].Intent.Operations[0]
			op.ID = "SPEC-002-OP-01"
			in.Sources = append(in.Sources, planSource("SPEC-002", Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{op}}))
		},
	}
	for name, mutation := range map[string]func(string) string{
		"missing references": func(s string) string { return strings.Replace(s, "## Implementation references", "## Other", 1) },
		"nested references": func(s string) string {
			return strings.Replace(s, "## Implementation references", "### Implementation references", 1)
		},
		"fenced references": func(s string) string {
			return strings.Replace(s, "## Implementation references", "```md\n## Implementation references", 1) + "```\n"
		},
		"quoted references": func(s string) string {
			return strings.Replace(s, "## Implementation references", "> ## Implementation references", 1)
		},
		"listed references": func(s string) string {
			return strings.Replace(s, "## Implementation references", "- ## Implementation references", 1)
		},
		"duplicate references":   func(s string) string { return s + "\n## Implementation references\n\nDuplicate.\n" },
		"closing hash duplicate": func(s string) string { return s + "\n## Implementation references ###\n\nDuplicate.\n" },
		"setext duplicate":       func(s string) string { return s + "\nImplementation references\n---\n\nDuplicate.\n" },
		"references before requirements": func(s string) string {
			section := "## Implementation references\n\n" + sectionReferences + "\n\n"
			s = strings.Replace(s, section, "", 1)
			return strings.Replace(s, "## Requirements", section+"## Requirements", 1)
		},
	} {
		tests[name] = func(in *CompileInput) {
			in.Sources[0].Intent.Operations[0] = sectionOperation("SPEC-001-OP-01", "Implementation references", sectionReferences, "Updated.")
			file := in.BaselineFiles[sectionTarget]
			file.Body = mutation(file.Body)
			in.BaselineFiles[sectionTarget] = file
		}
	}
	for _, fragment := range []string{"```", "~~~\nplain text", "<!--", "<script>"} {
		for _, section := range []string{"Purpose", "Implementation references"} {
			tests[section+" swallowed by "+fragment] = func(in *CompileInput) {
				before := "Test."
				if section == "Implementation references" {
					before = sectionReferences
				}
				in.Sources[0].Intent.Operations[0] = sectionOperation("SPEC-001-OP-01", section, before, fragment)
			}
		}
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := sectionCompileInput(sectionOperation("SPEC-001-OP-01", "Purpose", "Test.", "Updated."))
			mutate(&input)
			plan, err := CompilePlan(input)
			if err != nil || len(plan.Blockers) == 0 || len(plan.Files) != 0 {
				t.Fatalf("unsafe plan err=%v blockers=%+v files=%d", err, plan.Blockers, len(plan.Files))
			}
		})
	}
}

func TestSectionBodyWhitespaceIsExactAndOtherSectionsRemainUnchanged(t *testing.T) {
	before := "Original.  \n\nSecond paragraph.\t"
	after := "Changed.  \n\nSecond paragraph.\t"
	input := sectionCompileInput(sectionOperation("SPEC-001-OP-01", "Purpose", before, after))
	baseline := strings.Replace(input.BaselineFiles[sectionTarget].Body, "\n\nTest.\n\n", "\n\n\n"+before+"\n\n\n", 1)
	input.BaselineFiles[sectionTarget] = BaselineFile{Exists: true, Body: baseline}
	plan, err := CompilePlan(input)
	if err != nil || len(plan.Blockers) != 0 || len(plan.Files) != 1 {
		t.Fatalf("compile err=%v blockers=%+v", err, plan.Blockers)
	}
	if got := plan.Files[0].Postimage.Content; got != strings.Replace(baseline, before, after, 1) {
		t.Fatalf("unexpected byte change:\n%s", got)
	}
	trimmed := strings.TrimSpace(before)
	input.Sources[0].Intent.Operations[0].Before = &trimmed
	if plan, err := CompilePlan(input); err != nil || len(plan.Blockers) == 0 {
		t.Fatalf("inexact preimage accepted: %v %+v", err, plan.Blockers)
	}
}

func TestSectionOnlyPreservesNoncanonicalRequirementWhitespace(t *testing.T) {
	input := sectionCompileInput(sectionOperation("SPEC-001-OP-01", "Purpose", "Test.", "Changed purpose."), sectionOperation("SPEC-001-OP-02", "Implementation references", sectionReferences, "New references."))
	baseline := input.BaselineFiles[sectionTarget].Body
	baseline = strings.Replace(baseline, "## Requirements\n\n", "## Requirements\n\n\n", 1)
	baseline = strings.Replace(baseline, "\n\n## Implementation references", "  \n\n\n"+requirementBlockForTest("Other")+"  \n\n\n## Implementation references", 1)
	input.BaselineFiles[sectionTarget] = BaselineFile{Exists: true, Body: baseline}
	plan, err := CompilePlan(input)
	if err != nil || len(plan.Blockers) != 0 || len(plan.Files) != 1 {
		t.Fatalf("compile err=%v blockers=%+v", err, plan.Blockers)
	}
	expected := strings.Replace(strings.Replace(baseline, "Test.", "Changed purpose.", 1), sectionReferences, "New references.", 1)
	if got := plan.Files[0].Postimage.Content; got != expected {
		t.Fatalf("section-only projection changed unrelated bytes:\n%q\nwant:\n%q", got, expected)
	}
}
