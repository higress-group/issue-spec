package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func mustBody(t *testing.T, typ, id, body, status string) string {
	t.Helper()
	out, err := model.EnsureTypedBody(typ, id, body, model.BodyOptions{Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const canonicalSpec = "## Requirement: r\n\nThe CLI MUST work.\n\n### Scenario: s\n\n- **WHEN** x\n- **THEN** y"

func TestStatusFlagsMalformedTypedComments(t *testing.T) {
	malformed := mustBody(t, "SPEC", "SPEC-001", "# SPEC-001\n\nhand-written", "confirmed")
	summary := summarizeStatus("o/r", 1, 0, 0, []model.Artifact{
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: model.ParseTypedComment(malformed)},
	})
	if summary.OK {
		t.Fatal("malformed SPEC should make status non-OK")
	}
	if len(summary.Malformed) == 0 {
		t.Fatalf("expected malformed diagnostics, got %+v", summary)
	}
	if summary.Malformed[0].ID != "SPEC-001" || summary.Malformed[0].URL == "" {
		t.Fatalf("malformed diagnostic missing type/id/url context: %+v", summary.Malformed[0])
	}
	foundGate := false
	for _, g := range summary.NextGates {
		if strings.Contains(g, "malformed typed comments") {
			foundGate = true
		}
	}
	if !foundGate {
		t.Fatalf("expected malformed gate, got %v", summary.NextGates)
	}
}

func TestStatusReportsDuplicateLogicalIDsDeterministically(t *testing.T) {
	// Migration case: two SPEC comments sharing a logical ID must surface a
	// traceability error deterministically.
	a := mustBody(t, "SPEC", "SPEC-001", canonicalSpec, "confirmed")
	summary := summarizeStatus("o/r", 1, 0, 0, []model.Artifact{
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: model.ParseTypedComment(a)},
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-2", Comment: model.ParseTypedComment(a)},
	})
	if summary.OK {
		t.Fatal("duplicate logical id should make status non-OK")
	}
	joined := strings.Join(summary.Traceability.Errors, "\n")
	if !strings.Contains(joined, "duplicate logical id SPEC-001") {
		t.Fatalf("expected duplicate id error, got %v", summary.Traceability.Errors)
	}
}

func TestFinalVerifyBlocksOnMalformedActiveSpec(t *testing.T) {
	malformed := mustBody(t, "SPEC", "SPEC-001", "# SPEC-001\n\nhand-written", "confirmed")
	verify := mustBody(t, "VERIFY", "VERIFY-001", "## Evidence\n\nSPEC-001 covered by go test.", "done")
	report, err := buildFinalVerifyReport([]model.Artifact{
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: model.ParseTypedComment(malformed)},
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-2", Comment: model.ParseTypedComment(verify)},
	}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("final verify must fail on malformed active SPEC")
	}
	if len(report.Noncanonical) == 0 {
		t.Fatalf("expected noncanonical diagnostics in verify report: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Errors, "\n"), "SPEC-001") {
		t.Fatalf("verify errors should name malformed SPEC: %v", report.Errors)
	}
}

func TestFetchDurableSpecSourcesBlocksMalformedActiveSpec(t *testing.T) {
	malformed := mustBody(t, "SPEC", "SPEC-001", "# SPEC-001\n\nhand-written", "confirmed")
	client := fakeGitHubBackend{
		getIssue: func(context.Context, string, int) (github.Issue, error) {
			return github.Issue{HTMLURL: "https://github.com/o/r/issues/1"}, nil
		},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 1, HTMLURL: "https://github.com/o/r/issues/1#issuecomment-1", Body: malformed}}, nil
		},
	}
	_, _, err := fetchDurableSpecSources(context.Background(), client, "o/r", 1)
	if err == nil {
		t.Fatal("expected archive source collection to block malformed active SPEC")
	}
	if !strings.Contains(err.Error(), "malformed active SPEC") {
		t.Fatalf("archive error should explain malformed SPEC blocking: %v", err)
	}
}

func TestFetchDurableSpecSourcesAcceptsCanonicalSpec(t *testing.T) {
	good := mustBody(t, "SPEC", "SPEC-001", canonicalSpec, "confirmed")
	client := fakeGitHubBackend{
		getIssue: func(context.Context, string, int) (github.Issue, error) {
			return github.Issue{HTMLURL: "https://github.com/o/r/issues/1"}, nil
		},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 1, HTMLURL: "https://github.com/o/r/issues/1#issuecomment-1", Body: good}}, nil
		},
	}
	_, specs, err := fetchDurableSpecSources(context.Background(), client, "o/r", 1)
	if err != nil {
		t.Fatalf("canonical SPEC should be accepted: %v", err)
	}
	if len(specs) != 1 || specs[0].ID != "SPEC-001" {
		t.Fatalf("unexpected specs: %+v", specs)
	}
}
