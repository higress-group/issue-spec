package model

import (
	"strings"
	"testing"
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

func TestValidateCanonicalBodyNonSpecTypesReturnNil(t *testing.T) {
	for _, typ := range []string{"TASK", "PROCESS", "REVIEW", "VERIFY", "QUESTION"} {
		if diags := ValidateCanonicalBody(typ, typ+"-001", "", "anything at all"); diags != nil {
			t.Fatalf("%s should not have canonical diagnostics: %+v", typ, diags)
		}
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
