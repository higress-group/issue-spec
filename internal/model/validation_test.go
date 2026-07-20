package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/durable"
)

const canonicalSpecLogical = `## Requirement: canonical SPEC comments

The CLI MUST render canonical SPEC Markdown from structured fields.

### Scenario: structured fields render a canonical SPEC body

- **WHEN** a caller provides requirement and scenario fields
- **THEN** the CLI renders a body accepted by comment upsert`

func TestLogicalBodyStripsMarkerAndHeader(t *testing.T) {
	wrapped, err := EnsureTypedBody("SPEC", "SPEC-001", canonicalSpecLogical, BodyOptions{Status: "confirmed", Scope: "gen"})
	if err != nil {
		t.Fatal(err)
	}
	logical := LogicalBody(wrapped)
	if !strings.HasPrefix(logical, "## Requirement: canonical SPEC comments") {
		t.Fatalf("logical body should start at requirement heading, got:\n%s", logical)
	}
	if strings.Contains(logical, "Agent:") || strings.Contains(logical, "issue-spec:type=SPEC") || strings.Contains(logical, "Links:") {
		t.Fatalf("logical body still contains marker/header:\n%s", logical)
	}
	// Raw generated body (no wrapping) reduces to the same logical body.
	if got := LogicalBody(canonicalSpecLogical); got != logical {
		t.Fatalf("raw and wrapped bodies produced different logical bodies:\nraw=%q\nwrapped=%q", got, logical)
	}
}

func TestSpecBodyErrorsAcceptsCanonical(t *testing.T) {
	if errs := SpecBodyErrors(canonicalSpecLogical); len(errs) != 0 {
		t.Fatalf("canonical body reported errors: %v", errs)
	}
}

func TestValidateCanonicalBodyReportsEachMissingElement(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		element string
	}{
		{
			name:    "missing requirement heading",
			body:    "# SPEC-001\n\nThe CLI MUST work.\n\n### Scenario: s\n\n- **WHEN** x\n- **THEN** y",
			element: "requirement-heading",
		},
		{
			name:    "missing normative language",
			body:    "## Requirement: r\n\nThe CLI should work.\n\n### Scenario: s\n\n- **WHEN** x\n- **THEN** y",
			element: "normative-language",
		},
		{
			name:    "missing scenario heading",
			body:    "## Requirement: r\n\nThe CLI MUST work.\n\n- **WHEN** x\n- **THEN** y",
			element: "scenario-heading",
		},
		{
			name:    "missing when bullet",
			body:    "## Requirement: r\n\nThe CLI MUST work.\n\n### Scenario: s\n\n- **THEN** y",
			element: "when-bullet",
		},
		{
			name:    "missing then bullet",
			body:    "## Requirement: r\n\nThe CLI MUST work.\n\n### Scenario: s\n\n- **WHEN** x",
			element: "then-bullet",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := ValidateCanonicalBody("SPEC", "SPEC-001", "https://example/1", tc.body)
			if len(diags) == 0 {
				t.Fatalf("expected diagnostics for %s", tc.name)
			}
			found := false
			for _, d := range diags {
				if d.Element == tc.element {
					found = true
				}
				if d.Severity != "error" || d.Type != "SPEC" || d.ID != "SPEC-001" || d.URL != "https://example/1" {
					t.Fatalf("diagnostic missing context: %+v", d)
				}
			}
			if !found {
				t.Fatalf("expected element %q in diagnostics %+v", tc.element, diags)
			}
		})
	}
}

func TestValidateCanonicalBodyUncheckedTypesReturnNil(t *testing.T) {
	for _, typ := range []string{"REVIEW", "VERIFY", "QUESTION"} {
		if diags := ValidateCanonicalBody(typ, typ+"-001", "", "anything at all"); diags != nil {
			t.Fatalf("%s should not have canonical diagnostics: %+v", typ, diags)
		}
	}
}

func TestValidateCanonicalBodyTaskProcessDiscipline(t *testing.T) {
	if diags := ValidateCanonicalBody("TASK", "TASK-001", "", "anything at all"); len(diags) == 0 {
		t.Fatal("noncanonical TASK body should be rejected")
	}
	if diags := ValidateCanonicalBody("PROCESS", "PROCESS-001", "", "anything at all"); len(diags) == 0 {
		t.Fatal("noncanonical PROCESS body should be rejected")
	}
	goodTask := "## Task: t\n\n### Implementation Checklist\n\n- [ ] a\n\n### Execution Planning\n\n- Coupling class: low\n\n### Covers\n\n- SPEC-001"
	if diags := ValidateCanonicalBody("TASK", "TASK-001", "", goodTask); len(diags) != 0 {
		t.Fatalf("canonical TASK body should pass: %+v", diags)
	}
	goodProcess := "## Process: p\n\n### Owner\n\n- Worker\n\n### Parent TASK\n\n- TASK-001\n\n### Covers\n\n- TASK-001\n\n### Handoff\n\nN/A"
	if diags := ValidateCanonicalBody("PROCESS", "PROCESS-001", "", goodProcess); len(diags) != 0 {
		t.Fatalf("canonical PROCESS body should pass: %+v", diags)
	}
}

func TestValidateArtifactUsesRemoteBody(t *testing.T) {
	// A malformed SPEC written with a bypass must still be detected from its
	// remote body via ValidateArtifact.
	body, err := EnsureTypedBody("SPEC", "SPEC-009", "# SPEC-009\n\nvague text", BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	art := Artifact{URL: "https://example/9", Comment: ParseTypedComment(body)}
	if diags := ValidateArtifact(art); len(diags) == 0 {
		t.Fatal("expected ValidateArtifact to detect malformed SPEC from remote body")
	}
}

func TestValidateCanonicalBodyParsesStrictDurableIntentWhenPresent(t *testing.T) {
	logical := canonicalSpecLogical + "\n\n## Durable Intent\n\n```json\n" +
		`{"version":1,"intent":"OPERATIONS","operations":[{"id":"SPEC-001-OP-01","kind":"MODIFIED","capability":"canonical-comments","path":"issue-spec/specs/canonical-comments/spec.md","current_requirement":"canonical SPEC comments","projection":{"source":"current-spec"}}]}` +
		"\n````\n"
	if diags := ValidateCanonicalBody("SPEC", "SPEC-001", "", logical); len(diags) != 0 {
		t.Fatalf("core canonical validation interpreted durable policy: %+v", diags)
	}
	if diags := ValidateCanonicalBodyAtRoot("SPEC", "SPEC-001", "", logical, ""); !diagnosticsContainElement(diags, "durable-intent") {
		t.Fatalf("malformed Durable Intent was accepted: %+v", diags)
	}
	logical = strings.Replace(logical, "````", "```", 1)
	intent, found, err := ParseSpecDurableIntent("SPEC-001", logical, "")
	if err != nil || !found || intent.Kind != durable.IntentOperations || len(intent.Operations) != 1 {
		t.Fatalf("intent=%+v found=%t err=%v", intent, found, err)
	}
	if diags := ValidateCanonicalBodyAtRoot("SPEC", "SPEC-001", "", logical, ""); len(diags) != 0 {
		t.Fatalf("valid Durable Intent diagnostics = %+v", diags)
	}
	if diags := ValidateCanonicalBody("SPEC", "SPEC-001", "", canonicalSpecLogical); len(diags) != 0 {
		t.Fatalf("missing optional Durable Intent became universally invalid: %+v", diags)
	}
}

func TestValidateCanonicalBodyAtRootRequiresLegacyTargetToExist(t *testing.T) {
	root := t.TempDir()
	logical := canonicalSpecLogical + "\n\n## Durable Intent\n\n```json\n" +
		`{"version":1,"intent":"OPERATIONS","operations":[{"id":"SPEC-001-OP-01","kind":"REMOVED","capability":"canonical-comments","path":"openspec/specs/canonical-comments/spec.md","current_requirement":"legacy comment"}]}` +
		"\n```\n"
	if diags := ValidateCanonicalBodyAtRoot("SPEC", "SPEC-001", "", logical, root); !diagnosticsContainElement(diags, "durable-intent") {
		t.Fatalf("missing legacy target diagnostics = %+v", diags)
	}
	target := filepath.Join(root, "openspec", "specs", "canonical-comments", "spec.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# Legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if diags := ValidateCanonicalBodyAtRoot("SPEC", "SPEC-001", "", logical, root); len(diags) != 0 {
		t.Fatalf("existing legacy target diagnostics = %+v", diags)
	}
}

func diagnosticsContainElement(diagnostics []CanonicalDiagnostic, element string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Element == element {
			return true
		}
	}
	return false
}
