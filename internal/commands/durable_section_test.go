package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/durable"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestDurableSpecSectionGenerationAndRealGitRoundTrip(t *testing.T) {
	root, _ := durableCommandRepository(t)
	const target = "issue-spec/specs/service/spec.md"
	const beforePurpose = "Describe the service."
	const afterPurpose = "Describe the service and its admission policy."
	const beforeReferences = "- `service.go`"
	const afterReferences = "- `service.go`\n- `admission.go`"
	baselineBody := "# Service contract\n\n## Purpose\n\n" + beforePurpose + "\n\n## Requirements\n\n### Requirement: Existing behavior\n\nThe service MUST preserve existing behavior.\n\n#### Scenario: existing caller\n\n- **WHEN** an existing caller connects\n- **THEN** its behavior remains available\n\n## Implementation references\n\n" + beforeReferences + "\n\n## Notes\n\nPreserve this independent section.\n"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, target)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, target), []byte(baselineBody), 0o644); err != nil {
		t.Fatal(err)
	}
	durableCommandGit(t, root, "add", target)
	durableCommandGit(t, root, "commit", "-q", "-m", "existing contract")
	baseline := durableCommandGitOutput(t, root, "rev-parse", "HEAD")
	var input map[string]any
	if err := json.Unmarshal([]byte(specInputJSON), &input); err != nil {
		t.Fatal(err)
	}
	operations := []durable.Operation{}
	for index, section := range []struct{ name, before, after string }{{"Purpose", beforePurpose, afterPurpose}, {"Implementation references", beforeReferences, afterReferences}} {
		operations = append(operations, durable.Operation{ID: []string{"SPEC-001-OP-01", "SPEC-001-OP-02"}[index], Kind: durable.OperationSectionModified, Capability: "service", Path: target, Section: section.name, Before: &section.before, Projection: &durable.Projection{Source: durable.ProjectionInline, Text: &section.after}})
	}
	input["durable"] = durable.Intent{Version: 1, Kind: durable.IntentOperations, Operations: operations}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var generated, generateErr bytes.Buffer
	generator := newApp(strings.NewReader(""), &generated, &generateErr)
	if code := generator.runCommentGenerate(context.Background(), []string{"--type", "SPEC", "--id", "SPEC-001", "--status", "confirmed", "--input-file", writeTempInput(t, string(encoded))}); code != 0 {
		t.Fatalf("generate=%d stderr=%s", code, &generateErr)
	}
	source := generated.String()
	if diagnostics := model.ValidateCanonicalBody("SPEC", "SPEC-001", "", source); len(diagnostics) != 0 {
		t.Fatalf("generated SPEC invalid: %+v", diagnostics)
	}
	if intent, found, err := model.ParseSpecDurableIntent("SPEC-001", source, root); err != nil || !found || len(intent.Operations) != 2 {
		t.Fatalf("parse generated intent: %+v %t %v", intent, found, err)
	}
	backend := durableCommandBackend(source)
	planPath := filepath.Join(t.TempDir(), "plan.json")
	application, out, errOut := durableCommandApp(backend)
	if code := application.runDurableSpec(context.Background(), []string{"preview", "--repo", "o/r", "--proposal", "9", "--baseline", baseline, "--root", root, "--plan-out", planPath, "--json"}); code != 0 {
		t.Fatalf("preview=%d stdout=%s stderr=%s", code, out, errOut)
	}
	var plan durable.CompactPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || !plan.OK || plan.OperationCount != 2 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		application, out, errOut = durableCommandApp(backend)
		if code := application.runDurableSpec(context.Background(), []string{"apply", "--plan", planPath, "--expected-plan-digest", plan.PlanDigest, "--root", root, "--json"}); code != 0 {
			t.Fatalf("apply %d=%d stdout=%s stderr=%s", attempt, code, out, errOut)
		}
	}
	expected := strings.Replace(strings.Replace(baselineBody, beforePurpose, afterPurpose, 1), beforeReferences, afterReferences, 1)
	written, err := os.ReadFile(filepath.Join(root, target))
	if err != nil || string(written) != expected {
		t.Fatalf("unexpected projection: %v\n%s", err, written)
	}
	durableCommandGit(t, root, "add", target)
	durableCommandGit(t, root, "commit", "-q", "-m", "authorized section projections")
	subject := durableCommandGitOutput(t, root, "rev-parse", "HEAD")
	application, out, errOut = durableCommandApp(backend)
	args := []string{"check", "--repo", "o/r", "--proposal", "9", "--baseline", baseline, "--subject", subject, "--root", root, "--json"}
	if code := application.runDurableSpec(context.Background(), args); code != 0 {
		t.Fatalf("check=%d stdout=%s stderr=%s", code, out, errOut)
	}
	var checked durable.CompactCheck
	if err := json.Unmarshal(out.Bytes(), &checked); err != nil || !checked.OK {
		t.Fatalf("check=%+v err=%v", checked, err)
	}
	// A caller's subject must still match every byte, including an unowned section.
	if err := os.WriteFile(filepath.Join(root, target), []byte(strings.Replace(expected, "Preserve this independent section.", "Undeclared change.", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	durableCommandGit(t, root, "add", target)
	durableCommandGit(t, root, "commit", "-q", "-m", "undeclared edit")
	args[8] = durableCommandGitOutput(t, root, "rev-parse", "HEAD")
	application, out, errOut = durableCommandApp(backend)
	if code := application.runDurableSpec(context.Background(), args); code == 0 {
		t.Fatalf("undeclared edit accepted: stdout=%s stderr=%s", out, errOut)
	}
	if err := json.Unmarshal(out.Bytes(), &checked); err != nil || checked.OK || len(checked.Blockers) != 1 || checked.Blockers[0].Code != durable.BlockProjectionMismatch {
		t.Fatalf("wrong undeclared-edit failure: check=%+v err=%v stdout=%s", checked, err, out)
	}
}
