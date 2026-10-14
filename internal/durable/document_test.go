package durable

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Frozen, unmodified public HiRoute document:
// https://github.com/higress-group/HiRoute/blob/381c994d4a9e0d8fe8492bba3ba672be98947b0f/issue-spec/specs/request-routing-history/spec.md
func TestHiRouteDocumentProjectionPreservesTitleAndReferences(t *testing.T) {
	data, err := os.ReadFile("../../testdata/durable/hiroute-request-routing-history.md")
	if err != nil {
		t.Fatal(err)
	}
	const capability = "request-routing-history"
	const target = "issue-spec/specs/request-routing-history/spec.md"
	const requirement = "Request summaries preserve admitted routing identity safely"
	const opID = "SPEC-001-OP-01"
	for _, slugTitle := range []bool{false, true} {
		baseline := string(data)
		name := "readable title"
		if slugTitle {
			name = "slug title"
			baseline = strings.Replace(baseline, "# Recorded route identity", "# "+capability, 1)
		}
		prefix := baseline[:strings.Index(baseline, "### Requirement:")]
		suffix := baseline[strings.Index(baseline, "## Implementation references\n"):]
		originalBlock := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(baseline, suffix), prefix))
		tests := []struct {
			name   string
			intent Intent
		}{
			{"added", addedIntent(opID, capability, "New routing rule")},
			{"modified last", modifiedIntent(opID, capability, requirement)},
			{"removed last", removedIntentForCheck(opID, capability, requirement)},
			{"renamed last", renamedIntent(opID, capability, requirement, "Recorded request identity")},
		}
		for _, test := range tests {
			t.Run(name+"/"+test.name, func(t *testing.T) {
				input := baseCompileInput(planSource("SPEC-001", test.intent))
				input.BaselineFiles[target] = BaselineFile{Exists: true, Body: baseline}
				plan, err := CompilePlan(input)
				if err != nil || len(plan.Blockers) != 0 || len(plan.Files) != 1 {
					t.Fatalf("compile err=%v blockers=%+v files=%d", err, plan.Blockers, len(plan.Files))
				}
				root := t.TempDir()
				mustWritePlanFile(t, root, target, baseline)
				result, err := ApplyPlan(root, plan, plan.PlanDigest, authorityForPlan(plan))
				if err != nil || !result.OK {
					t.Fatalf("apply=%+v err=%v", result, err)
				}
				written, err := os.ReadFile(filepath.Join(root, target))
				if err != nil {
					t.Fatal(err)
				}
				body := string(written)
				if !strings.HasPrefix(body, prefix) || !strings.HasSuffix(body, suffix) || strings.Count(body, suffix) != 1 {
					t.Fatalf("title, purpose or trailing references changed:\n%s", body)
				}
				section := strings.TrimSuffix(strings.TrimPrefix(body, prefix), suffix)
				switch test.name {
				case "added":
					if !strings.Contains(section, originalBlock) || !strings.Contains(section, "### Requirement: New routing rule") {
						t.Fatal("added requirement is not inside Requirements or existing contract was changed")
					}
				case "modified last":
					if !strings.Contains(section, requirement+" MUST be updated.") || strings.Contains(section, "Each ObservedRequestSummary") {
						t.Fatal("last requirement was not replaced")
					}
				case "removed last":
					if strings.TrimSpace(section) != "" {
						t.Fatalf("removed requirement remains: %s", section)
					}
				case "renamed last":
					if !strings.Contains(section, "### Requirement: Recorded request identity") || !strings.Contains(section, "Each ObservedRequestSummary") {
						t.Fatal("rename lost the original behavior")
					}
				}
				checkInput := CheckInput{CompileInput: input, SubjectRevision: strings.Repeat("b", 40),
					SubjectFiles: map[string]BaselineFile{target: {Exists: true, Body: body}}, ChangedDurablePaths: []string{target}}
				checked, err := Check(checkInput)
				if err != nil || !checked.OK {
					t.Fatalf("check=%+v err=%v", checked, err)
				}
				retry, err := ApplyPlan(root, plan, plan.PlanDigest, authorityForPlan(plan))
				if err != nil || !retry.OK || retry.Files.Unchanged != 1 || retry.Files.Updated != 0 {
					t.Fatalf("retry=%+v err=%v", retry, err)
				}
				checkInput.SubjectFiles[target] = BaselineFile{Exists: true, Body: strings.Replace(body, "relation.rs", "unapproved.rs", 1)}
				checked, err = Check(checkInput)
				if err != nil || checked.OK {
					t.Fatalf("check accepted an unauthorized reference edit: %+v err=%v", checked, err)
				}
			})
		}
	}
}

func TestDurableDocumentIgnoresHeadingsInsideFencedExamples(t *testing.T) {
	example := "\n\n```markdown\n## Requirements\n\n### Requirement: Example only\n## Implementation references\n```"
	first := requirementBlockForTest("Keep") + example
	suffix := "## Implementation references\n\n- Keep this link.\n\n## Notes\n\n~~~md\n## Purpose\n~~~\n"
	baseline := durableBody("cap", first, requirementBlockForTest("Replace")) + "\n" + suffix
	input := baseCompileInput(planSource("SPEC-001", modifiedIntent("SPEC-001-OP-01", "cap", "Replace")))
	input.BaselineFiles["issue-spec/specs/cap/spec.md"] = BaselineFile{Exists: true, Body: baseline}
	plan, err := CompilePlan(input)
	if err != nil || len(plan.Blockers) != 0 || len(plan.Files) != 1 {
		t.Fatalf("compile err=%v blockers=%+v", err, plan.Blockers)
	}
	result := plan.Files[0].Postimage.Content
	if !strings.Contains(result, first) || !strings.Contains(result, "Replace MUST be updated.") || !strings.HasSuffix(result, suffix) {
		t.Fatalf("example headings confused document boundaries:\n%s", result)
	}
}

func TestDurableDocumentRejectsAmbiguousStructure(t *testing.T) {
	valid := durableBody("cap", requirementBlockForTest("Old"))
	tests := map[string]string{
		"missing title":          strings.TrimPrefix(valid, "# cap\n"),
		"empty title":            strings.Replace(valid, "# cap", "# ", 1),
		"multiple titles":        valid + "\n# Another document\n",
		"duplicate requirements": valid + "\n## Requirements\n\n",
		"duplicate purpose":      valid + "\n## Purpose\n\n",
		"wrong order":            "# Title\n\n## Requirements\n\n## Purpose\n\n",
		"unowned text":           strings.Replace(valid, "## Requirements\n\n", "## Requirements\n\nUnowned text.\n\n", 1),
		"malformed identity":     strings.Replace(valid, "### Requirement: Old", "### Requirement: ", 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			input := baseCompileInput(planSource("SPEC-001", modifiedIntent("SPEC-001-OP-01", "cap", "Old")))
			input.BaselineFiles["issue-spec/specs/cap/spec.md"] = BaselineFile{Exists: true, Body: body}
			plan, err := CompilePlan(input)
			if err != nil || !hasBlocker(plan, BlockTargetFileInvalid) || len(plan.Files) != 0 {
				t.Fatalf("unsafe document accepted: err=%v blockers=%+v files=%d", err, plan.Blockers, len(plan.Files))
			}
		})
	}
}
