package durable

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const canonicalSpecBody = `## Requirement: route ownership

The proxy MUST require an owner for every public route.

### Scenario: owner is present

- **WHEN** a public route is configured
- **THEN** verification accepts an explicit owner
`

func TestIntentValidatesUnchangedAndEveryOperationKind(t *testing.T) {
	if _, err := NormalizeIntent(Intent{Version: 1, Kind: IntentUnchanged}, ValidationOptions{}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		operation Operation
	}{
		{name: "added-current-spec", operation: Operation{ID: "SPEC-003-OP-01", Kind: OperationAdded,
			Capability: "route-policy", Path: "issue-spec/specs/route-policy/spec.md", NewRequirement: "route ownership",
			Projection: &Projection{Source: ProjectionCurrentSpec}}},
		{name: "modified-inline", operation: Operation{ID: "SPEC-003-OP-02", Kind: OperationModified,
			Capability: "route-policy", Path: "issue-spec/specs/route-policy/spec.md", CurrentRequirement: "route ownership",
			Projection: &Projection{Source: ProjectionInline,
				Requirement: &RequirementInput{Title: "route ownership", Text: "The proxy MUST reject unowned public routes."},
				Scenarios:   []ScenarioInput{{Title: "missing owner", When: "a route has no owner", Then: "verification rejects it"}}}}},
		{name: "removed", operation: Operation{ID: "SPEC-003-OP-03", Kind: OperationRemoved,
			Capability: "route-policy", Path: "issue-spec/specs/route-policy/spec.md", CurrentRequirement: "obsolete route policy"}},
		{name: "renamed", operation: Operation{ID: "SPEC-003-OP-04", Kind: OperationRenamed,
			Capability: "route-policy", Path: "issue-spec/specs/route-policy/spec.md",
			CurrentRequirement: "old route ownership", NewRequirement: "new route ownership"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent := Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{test.operation}}
			normalized, err := NormalizeIntent(intent, ValidationOptions{SpecID: "SPEC-003", SpecRequirement: "route ownership"})
			if err != nil {
				t.Fatal(err)
			}
			if len(normalized.Operations) != 1 || normalized.Operations[0].Kind != test.operation.Kind {
				t.Fatalf("normalized intent = %+v", normalized)
			}
			payload, err := CanonicalJSON(normalized, ValidationOptions{SpecID: "SPEC-003", SpecRequirement: "route ownership"})
			if err != nil {
				t.Fatal(err)
			}
			body := canonicalSpecBody + "\n## Durable Intent\n\n```json\n" + string(payload) + "\n```\n"
			parsed, found, err := ParseSpecIntent(body, ValidationOptions{SpecID: "SPEC-003"})
			if err != nil || !found || len(parsed.Operations) != 1 || parsed.Operations[0].Kind != test.operation.Kind {
				t.Fatalf("parsed intent=%+v found=%t err=%v", parsed, found, err)
			}
		})
	}
}

func TestIntentNormalizesMultipleOperationsByExactID(t *testing.T) {
	intent := Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{
		{ID: "SPEC-003-OP-02", Kind: OperationRemoved, Capability: "routes", Path: "issue-spec/specs/routes/spec.md", CurrentRequirement: "legacy routes"},
		{ID: "SPEC-003-OP-01", Kind: OperationAdded, Capability: "routes", Path: "issue-spec/specs/routes/spec.md", NewRequirement: "owned routes",
			Projection: &Projection{Source: ProjectionInline, Requirement: &RequirementInput{Title: "owned routes", Text: "Routes MUST have owners."},
				Scenarios: []ScenarioInput{{Title: "owner", When: "a route is added", Then: "it names an owner"}}}},
	}}
	normalized, err := NormalizeIntent(intent, ValidationOptions{SpecID: "SPEC-003"})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Operations[0].ID != "SPEC-003-OP-01" || normalized.Operations[1].ID != "SPEC-003-OP-02" {
		t.Fatalf("operation order = %+v", normalized.Operations)
	}
}

func TestParseSpecIntentIsStrictAndUsesCurrentSpecProjection(t *testing.T) {
	body := canonicalSpecBody + "\n## Durable Intent\n\n```json\n" +
		`{"version":1,"intent":"OPERATIONS","operations":[{"id":"SPEC-003-OP-01","kind":"MODIFIED","capability":"route-policy","path":"issue-spec/specs/route-policy/spec.md","current_requirement":"route ownership","projection":{"source":"current-spec"}}]}` +
		"\n```\n"
	intent, found, err := ParseSpecIntent(body, ValidationOptions{SpecID: "SPEC-003"})
	if err != nil || !found || len(intent.Operations) != 1 {
		t.Fatalf("intent=%+v found=%t err=%v", intent, found, err)
	}
	canonical, err := CanonicalJSON(intent, ValidationOptions{SpecID: "SPEC-003", SpecRequirement: "route ownership"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), `"source": "current-spec"`) {
		t.Fatalf("canonical JSON = %s", canonical)
	}

	if _, found, err := ParseSpecIntent(canonicalSpecBody, ValidationOptions{SpecID: "SPEC-003"}); err != nil || found {
		t.Fatalf("missing intent found=%t err=%v", found, err)
	}
	for name, unknown := range map[string]string{
		"intent":     strings.Replace(body, `"version":1`, `"version":1,"plugin":"execute"`, 1),
		"operation":  strings.Replace(body, `"id":"SPEC-003-OP-01"`, `"id":"SPEC-003-OP-01","digest":"forged"`, 1),
		"projection": strings.Replace(body, `"source":"current-spec"`, `"source":"current-spec","command":"execute"`, 1),
	} {
		if _, _, err := ParseSpecIntent(unknown, ValidationOptions{SpecID: "SPEC-003"}); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("unknown %s field error = %v", name, err)
		}
	}
	unsupported := strings.Replace(body, `"version":1`, `"version":2`, 1)
	if _, _, err := ParseSpecIntent(unsupported, ValidationOptions{SpecID: "SPEC-003"}); err == nil || !strings.Contains(err.Error(), "unsupported value 2") {
		t.Fatalf("unsupported version error = %v", err)
	}
	multiple := body + "\n## Durable Intent\n\n```json\n{\"version\":1,\"intent\":\"UNCHANGED\"}\n```\n"
	if _, _, err := ParseSpecIntent(multiple, ValidationOptions{SpecID: "SPEC-003"}); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("multiple intent error = %v", err)
	}
}

func TestIntentRejectsInvalidVersionsFieldsAndOperationShapes(t *testing.T) {
	base := Operation{ID: "SPEC-003-OP-01", Kind: OperationModified, Capability: "routes",
		Path: "issue-spec/specs/routes/spec.md", CurrentRequirement: "route ownership", Projection: &Projection{Source: ProjectionCurrentSpec}}
	tests := []struct {
		name   string
		intent Intent
		want   string
	}{
		{"version", Intent{Version: 2, Kind: IntentUnchanged}, "unsupported value"},
		{"intent", Intent{Version: 1, Kind: "MAYBE"}, "unsupported value"},
		{"empty operations", Intent{Version: 1, Kind: IntentOperations}, "at least one"},
		{"unchanged operations", Intent{Version: 1, Kind: IntentUnchanged, Operations: []Operation{base}}, "must not contain"},
		{"bad id", Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{func() Operation { value := base; value.ID = "OP-1"; return value }()}}, "must match"},
		{"wrong spec id", Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{func() Operation { value := base; value.ID = "SPEC-004-OP-01"; return value }()}}, "must belong"},
		{"modified title change", Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{func() Operation { value := base; value.NewRequirement = "new"; return value }()}}, "forbids new_requirement"},
		{"removed projection", Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{func() Operation { value := base; value.Kind = OperationRemoved; return value }()}}, "forbids new_requirement and projection"},
		{"rename same title", Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{func() Operation {
			value := base
			value.Kind = OperationRenamed
			value.NewRequirement = value.CurrentRequirement
			value.Projection = nil
			return value
		}()}}, "must be distinct"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.intent.Validate(ValidationOptions{SpecID: "SPEC-003", SpecRequirement: "route ownership"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestIntentRejectsDuplicateTargetsAndConflictingRenameEndpoints(t *testing.T) {
	duplicate := Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{
		{ID: "SPEC-003-OP-01", Kind: OperationRemoved, Capability: "routes", Path: "issue-spec/specs/routes/spec.md", CurrentRequirement: "old"},
		{ID: "SPEC-003-OP-02", Kind: OperationRemoved, Capability: "routes", Path: "issue-spec/specs/routes/spec.md", CurrentRequirement: "old"},
	}}
	if err := duplicate.Validate(ValidationOptions{SpecID: "SPEC-003"}); err == nil || !strings.Contains(err.Error(), "duplicate target") {
		t.Fatalf("duplicate target error = %v", err)
	}

	rename := Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{
		{ID: "SPEC-003-OP-01", Kind: OperationRenamed, Capability: "routes", Path: "issue-spec/specs/routes/spec.md", CurrentRequirement: "old-a", NewRequirement: "shared"},
		{ID: "SPEC-003-OP-02", Kind: OperationRenamed, Capability: "routes", Path: "issue-spec/specs/routes/spec.md", CurrentRequirement: "old-b", NewRequirement: "shared"},
	}}
	if err := rename.Validate(ValidationOptions{SpecID: "SPEC-003"}); err == nil || !strings.Contains(err.Error(), "rename endpoint") {
		t.Fatalf("rename collision error = %v", err)
	}
}

func TestIntentTargetPathAllowsCanonicalAndOnlyExistingLegacy(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "openspec", "specs", "routes", "spec.md")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("# Routes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation := Operation{ID: "SPEC-003-OP-01", Kind: OperationRemoved, Capability: "routes",
		Path: "openspec/specs/routes/spec.md", CurrentRequirement: "old routes"}
	intent := Intent{Version: 1, Kind: IntentOperations, Operations: []Operation{operation}}
	if err := intent.Validate(ValidationOptions{RepositoryRoot: root, SpecID: "SPEC-003"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}
	if err := intent.Validate(ValidationOptions{RepositoryRoot: root, SpecID: "SPEC-003"}); err == nil || !strings.Contains(err.Error(), "does not already exist") {
		t.Fatalf("missing legacy path error = %v", err)
	}
	intent.Operations[0].Path = "../openspec/specs/routes/spec.md"
	if err := intent.Validate(ValidationOptions{RepositoryRoot: root, SpecID: "SPEC-003"}); err == nil || !strings.Contains(err.Error(), "clean repository-relative") {
		t.Fatalf("path traversal error = %v", err)
	}
	intent.Operations[0].Path = "issue-spec/specs/other/spec.md"
	if err := intent.Validate(ValidationOptions{RepositoryRoot: root, SpecID: "SPEC-003"}); err == nil || !strings.Contains(err.Error(), "must be") {
		t.Fatalf("capability/path mismatch error = %v", err)
	}
}
