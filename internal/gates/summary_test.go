package gates

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestProjectCompactSummaryGroupsAndBoundsRepeatedDiagnostics(t *testing.T) {
	diagnostics := make([]Diagnostic, 0, 101)
	for index := 100; index >= 1; index-- {
		id := fmt.Sprintf("PROCESS-%03d", index)
		diagnostics = append(diagnostics, Diagnostic{
			Code: "process.not_done", Gate: TargetFinal, Severity: SeverityError, Blocking: true,
			Artifact:    ArtifactRef{Type: "PROCESS", ID: id},
			Remediation: Remediation{CommandFamily: "comment transition", Arguments: []string{"--id", id, "--to", "done"}},
		})
	}
	diagnostics = append(diagnostics, Diagnostic{Code: "informational", Blocking: false})
	report := Report{Ready: false, Target: TargetFinal, Mode: ModeAuthoritative, Diagnostics: diagnostics}
	detail := Remediation{CommandFamily: "status", Arguments: []string{"--repo", "组织/仓库", "--gate", "final", "--json"}}
	subject := &CompactSubject{Revision: "head-abc", Evidence: &CompactEvidenceIdentity{
		Kind: "code_change", ID: "change-42", Provider: "code.example", Repository: "组织/仓库",
	}}

	summary := ProjectCompactSummary(report, map[string]map[string]int{"PROCESS": {"in-progress": 100}}, subject, detail)
	if summary.SchemaVersion != 1 || summary.OK || summary.Gate.Target != TargetFinal || len(summary.Blockers) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	blocker := summary.Blockers[0]
	if blocker.Count != 100 || len(blocker.Affected) != 10 || blocker.TruncatedCount != 90 {
		t.Fatalf("blocker bounds = %+v", blocker)
	}
	if blocker.Affected[0].ID != "PROCESS-001" || blocker.Affected[9].ID != "PROCESS-010" {
		t.Fatalf("affected sorting = %+v", blocker.Affected)
	}
	wantAction := Remediation{CommandFamily: "comment transition", Arguments: []string{"--id", "{artifact_id}", "--to", "done"}}
	if !reflect.DeepEqual(blocker.Remediation, wantAction) || !reflect.DeepEqual(blocker.Detail, detail) {
		t.Fatalf("actions remediation=%+v detail=%+v", blocker.Remediation, blocker.Detail)
	}

	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(raw) || len(raw) > 4096 || strings.Contains(string(raw), "evaluation_digest") {
		t.Fatalf("compact UTF-8 budget violated: bytes=%d body=%s", len(raw), raw)
	}

	reversed := append([]Diagnostic(nil), diagnostics...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	second, err := json.Marshal(ProjectCompactSummary(Report{Ready: false, Target: TargetFinal,
		Mode: ModeAuthoritative, Diagnostics: reversed}, map[string]map[string]int{"PROCESS": {"in-progress": 100}}, subject, detail))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(second) {
		t.Fatalf("summary depends on diagnostic order:\n%s\n%s", raw, second)
	}
}

func TestProjectCompactSummaryKeepsDistinctRemediationVariants(t *testing.T) {
	diagnostic := func(id, status string) Diagnostic {
		return Diagnostic{Code: "artifact.status.invalid", Blocking: true,
			Artifact:    ArtifactRef{Type: "PROCESS", ID: id},
			Remediation: Remediation{CommandFamily: "comment transition", Arguments: []string{"--id", id, "--to", status}}}
	}
	report := Report{Target: TargetFinal, Mode: ModeAuthoritative, Diagnostics: []Diagnostic{
		diagnostic("PROCESS-003", "done"), diagnostic("PROCESS-001", "ready"), diagnostic("PROCESS-002", "done"),
	}}
	summary := ProjectCompactSummary(report, nil, nil, Remediation{CommandFamily: "verify", Arguments: []string{"--json"}})
	if len(summary.Blockers) != 2 {
		t.Fatalf("remediation variants collapsed: %+v", summary.Blockers)
	}
	if summary.Blockers[0].Count+summary.Blockers[1].Count != 3 {
		t.Fatalf("diagnostic counts lost: %+v", summary.Blockers)
	}
	for _, blocker := range summary.Blockers {
		if blocker.Remediation.Arguments[1] != "{artifact_id}" {
			t.Fatalf("artifact id was not normalized: %+v", blocker.Remediation)
		}
	}
}

func TestProjectCompactSummarySuccessIgnoresExpandedProcessEvidence(t *testing.T) {
	report := Report{Ready: true, Target: TargetFinal, Mode: ModeAuthoritative,
		Processes: make([]ProcessEvidenceReport, 1000)}
	summary := ProjectCompactSummary(report, map[string]map[string]int{"PROCESS": {"done": 1000}},
		&CompactSubject{Revision: "head-abc"}, Remediation{})
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.OK || len(summary.Blockers) != 0 || len(raw) > 1024 || !utf8.Valid(raw) ||
		strings.Contains(string(raw), "processes") || strings.Contains(string(raw), "satisfied") {
		t.Fatalf("successful summary is not bounded: bytes=%d body=%s", len(raw), raw)
	}
}
