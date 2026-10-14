package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/durable"
)

func TestDurableSpecHiRouteDocumentRoundTrip(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/durable/hiroute-request-routing-history.md")
	if err != nil {
		t.Fatal(err)
	}
	const target = "issue-spec/specs/request-routing-history/spec.md"
	const original = "Request summaries preserve admitted routing identity safely"
	for _, kind := range []durable.OperationKind{durable.OperationAdded, durable.OperationModified, durable.OperationRemoved, durable.OperationRenamed} {
		t.Run(string(kind), func(t *testing.T) {
			root, _ := durableCommandRepository(t)
			if err := os.MkdirAll(filepath.Join(root, filepath.Dir(target)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, target), fixture, 0o644); err != nil {
				t.Fatal(err)
			}
			durableCommandGit(t, root, "add", target)
			durableCommandGit(t, root, "commit", "-q", "-m", "existing HiRoute contract")
			baseline := durableCommandGitOutput(t, root, "rev-parse", "HEAD")
			op := durable.Operation{ID: "SPEC-001-OP-01", Kind: kind, Capability: "request-routing-history", Path: target}
			title := original
			if kind == durable.OperationAdded {
				title = "Additional history contract"
				op.NewRequirement = title
			} else {
				op.CurrentRequirement = original
			}
			if kind == durable.OperationRenamed {
				op.NewRequirement = "Recorded route identity"
			}
			if kind == durable.OperationAdded || kind == durable.OperationModified {
				op.Projection = &durable.Projection{Source: durable.ProjectionInline,
					Requirement: &durable.RequirementInput{Title: title, Text: "History MUST retain the recorded route."},
					Scenarios:   []durable.ScenarioInput{{Title: "Historical route", When: "a request is inspected", Then: "its recorded route is returned"}}}
			}
			intent, err := json.Marshal(durable.Intent{Version: 1, Kind: durable.IntentOperations, Operations: []durable.Operation{op}})
			if err != nil {
				t.Fatal(err)
			}
			backend := durableCommandBackend(durableCommandSpec(t, string(intent)))
			planPath := filepath.Join(t.TempDir(), "plan.json")
			application, out, errOut := durableCommandApp(backend)
			if code := application.runDurableSpec(context.Background(), []string{"preview", "--repo", "o/r", "--proposal", "9", "--baseline", baseline, "--root", root, "--plan-out", planPath, "--json"}); code != 0 {
				t.Fatalf("preview code=%d stdout=%s stderr=%s", code, out, errOut)
			}
			var plan durable.CompactPlan
			if err := json.Unmarshal(out.Bytes(), &plan); err != nil || !plan.OK {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
			application, out, errOut = durableCommandApp(backend)
			if code := application.runDurableSpec(context.Background(), []string{"apply", "--plan", planPath, "--expected-plan-digest", plan.PlanDigest, "--root", root, "--json"}); code != 0 {
				t.Fatalf("apply code=%d stdout=%s stderr=%s", code, out, errOut)
			}
			written, err := os.ReadFile(filepath.Join(root, target))
			if err != nil {
				t.Fatal(err)
			}
			suffix := string(fixture)[strings.Index(string(fixture), "## Implementation references\n"):]
			if !strings.HasPrefix(string(written), "# Recorded route identity\n") || !strings.HasSuffix(string(written), suffix) {
				t.Fatal("CLI changed the readable title or trailing references")
			}
			durableCommandGit(t, root, "add", target)
			durableCommandGit(t, root, "commit", "-q", "-m", "project contract")
			subject := durableCommandGitOutput(t, root, "rev-parse", "HEAD")
			application, out, errOut = durableCommandApp(backend)
			if code := application.runDurableSpec(context.Background(), []string{"check", "--repo", "o/r", "--proposal", "9", "--baseline", baseline, "--subject", subject, "--root", root, "--json"}); code != 0 {
				t.Fatalf("check code=%d stdout=%s stderr=%s", code, out, errOut)
			}
			var checked durable.CompactCheck
			if err := json.Unmarshal(out.Bytes(), &checked); err != nil || !checked.OK {
				t.Fatalf("check=%+v err=%v", checked, err)
			}
		})
	}
}
