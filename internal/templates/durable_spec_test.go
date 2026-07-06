package templates

import (
	"strings"
	"testing"
)

func TestDurableSpecRendersFinalFormat(t *testing.T) {
	out, err := DurableSpec(DurableSpecOptions{
		Capability:       "issue-spec-cli",
		ProposalIssueURL: "https://github.com/o/r/issues/1",
		SpecificationList: []SpecSource{{
			ID:  "SPEC-001",
			URL: "https://github.com/o/r/issues/1#issuecomment-1",
			Body: `<!-- issue-spec:type=SPEC id=SPEC-001 version=1 -->
Agent: Coordinator
Type: SPEC
ID: SPEC-001
Status: confirmed
Scope: cli
Links:
- Proposal Issue: https://github.com/o/r/issues/1
- Design Issue: N/A
- Implement Issue: N/A
- Related Comments: N/A
- PR: N/A

## Requirement: Question lifecycle

The CLI MUST manage questions.

### Scenario: Create question

- **WHEN** Coordinator creates a question
- **THEN** the CLI records it.
`,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# issue-spec-cli",
		"## Purpose",
		"Define the long-lived behavior contract for this capability.",
		"Proposal Issues:\n- https://github.com/o/r/issues/1",
		"## Requirements",
		"### Requirement: Question lifecycle",
		"#### Scenario: Create question",
		"Source SPEC comment: https://github.com/o/r/issues/1#issuecomment-1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ADDED Requirements") {
		t.Fatalf("durable spec kept delta-only heading:\n%s", out)
	}
	for _, notWant := range []string{
		"issue-spec capability",
		"Organize requirements",
		"one-to-one proposal SPEC comments",
	} {
		if strings.Contains(out, notWant) {
			t.Fatalf("durable spec included archive-editing guidance %q:\n%s", notWant, out)
		}
	}
}

func specBody(id, requirement string) string {
	return `<!-- issue-spec:type=SPEC id=` + id + ` version=1 -->
Agent: Coordinator
Type: SPEC
ID: ` + id + `
Status: confirmed
Scope: cli

## Requirement: ` + requirement + `

The CLI MUST ` + strings.ToLower(requirement) + `.

### Scenario: ` + requirement + ` happens

- **WHEN** the trigger for ` + requirement + ` occurs
- **THEN** the CLI ` + strings.ToLower(requirement) + `.
`
}

func TestDurableSpecAccumulatesExistingRequirements(t *testing.T) {
	first, err := DurableSpec(DurableSpecOptions{
		Capability:       "cross-agent-handoff",
		ProposalIssueURL: "https://github.com/o/r/issues/1",
		SpecificationList: []SpecSource{{
			ID:   "SPEC-001",
			URL:  "https://github.com/o/r/issues/1#issuecomment-1",
			Body: specBody("SPEC-001", "Read side handoff"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	second, err := DurableSpec(DurableSpecOptions{
		Capability:       "cross-agent-handoff",
		ProposalIssueURL: "https://github.com/o/r/issues/2",
		ExistingSpecBody: first,
		SpecificationList: []SpecSource{{
			ID:   "SPEC-002",
			URL:  "https://github.com/o/r/issues/2#issuecomment-2",
			Body: specBody("SPEC-002", "Write side handoff"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"### Requirement: Read side handoff",
		"### Requirement: Write side handoff",
		"Source SPEC comment: https://github.com/o/r/issues/1#issuecomment-1",
		"Source SPEC comment: https://github.com/o/r/issues/2#issuecomment-2",
		"- https://github.com/o/r/issues/1",
		"- https://github.com/o/r/issues/2",
	} {
		if !strings.Contains(second, want) {
			t.Fatalf("re-archive dropped %q:\n%s", want, second)
		}
	}
	// Preserved requirement must come before the newly appended one.
	if strings.Index(second, "Read side handoff") > strings.Index(second, "Write side handoff") {
		t.Fatalf("expected preserved requirement before new one:\n%s", second)
	}
}

func TestDurableSpecReplacesRequirementByTitleNewestWins(t *testing.T) {
	first, err := DurableSpec(DurableSpecOptions{
		Capability:       "cross-agent-handoff",
		ProposalIssueURL: "https://github.com/o/r/issues/1",
		SpecificationList: []SpecSource{{
			ID:   "SPEC-001",
			URL:  "https://github.com/o/r/issues/1#issuecomment-1",
			Body: specBody("SPEC-001", "Session resume"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	updated := `<!-- issue-spec:type=SPEC id=SPEC-009 version=1 -->
Agent: Coordinator
Type: SPEC
ID: SPEC-009
Status: confirmed
Scope: cli

## Requirement: Session resume

The CLI MUST resume sessions with a revised contract.

### Scenario: Resume after crash

- **WHEN** an agent resumes after a crash
- **THEN** the CLI restores the prior session state.
`
	second, err := DurableSpec(DurableSpecOptions{
		Capability:       "cross-agent-handoff",
		ProposalIssueURL: "https://github.com/o/r/issues/2",
		ExistingSpecBody: first,
		SpecificationList: []SpecSource{{
			ID:   "SPEC-009",
			URL:  "https://github.com/o/r/issues/2#issuecomment-9",
			Body: updated,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(second, "### Requirement: Session resume") != 1 {
		t.Fatalf("expected single merged requirement, got:\n%s", second)
	}
	if !strings.Contains(second, "revised contract") {
		t.Fatalf("newest requirement text missing:\n%s", second)
	}
	if strings.Contains(second, "issuecomment-1") {
		t.Fatalf("stale source link survived replacement:\n%s", second)
	}
	if !strings.Contains(second, "issuecomment-9") {
		t.Fatalf("new source link missing:\n%s", second)
	}
}

func TestDurableSpecPreservesRequirementsWhenBodyContainsHeadingLine(t *testing.T) {
	alpha := `<!-- issue-spec:type=SPEC id=SPEC-001 version=1 -->
Agent: Coordinator
Type: SPEC
ID: SPEC-001
Status: confirmed
Scope: cli

## Requirement: Alpha

The CLI MUST do alpha.

## Notes

An internal note whose line starts like a level-2 heading.

### Scenario: alpha happens

- **WHEN** the trigger occurs
- **THEN** the CLI does alpha.
`
	first, err := DurableSpec(DurableSpecOptions{
		Capability:       "cross-agent-handoff",
		ProposalIssueURL: "https://github.com/o/r/issues/1",
		SpecificationList: []SpecSource{
			{ID: "SPEC-001", URL: "https://github.com/o/r/issues/1#issuecomment-1", Body: alpha},
			{ID: "SPEC-002", URL: "https://github.com/o/r/issues/1#issuecomment-2", Body: specBody("SPEC-002", "Beta")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := DurableSpec(DurableSpecOptions{
		Capability:       "cross-agent-handoff",
		ProposalIssueURL: "https://github.com/o/r/issues/2",
		ExistingSpecBody: first,
		SpecificationList: []SpecSource{{
			ID:   "SPEC-003",
			URL:  "https://github.com/o/r/issues/2#issuecomment-3",
			Body: specBody("SPEC-003", "Gamma"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"### Requirement: Alpha",
		"### Requirement: Beta",
		"### Requirement: Gamma",
	} {
		if !strings.Contains(second, want) {
			t.Fatalf("re-archive dropped %q when a body contained a heading-like line:\n%s", want, second)
		}
	}
}

func TestDurableSpecPreservesEmptyTitleRequirements(t *testing.T) {
	emptyTitle := func(id, then string) string {
		return `<!-- issue-spec:type=SPEC id=` + id + ` version=1 -->
Agent: Coordinator
Type: SPEC
ID: ` + id + `
Status: confirmed
Scope: cli

## Requirement:

The CLI MUST ` + then + `.

### Scenario: ` + then + `

- **WHEN** the trigger for ` + then + ` occurs
- **THEN** the CLI ` + then + `.
`
	}
	out, err := DurableSpec(DurableSpecOptions{
		Capability:       "cross-agent-handoff",
		ProposalIssueURL: "https://github.com/o/r/issues/1",
		SpecificationList: []SpecSource{
			{ID: "SPEC-001", URL: "https://github.com/o/r/issues/1#issuecomment-1", Body: emptyTitle("SPEC-001", "handle first")},
			{ID: "SPEC-002", URL: "https://github.com/o/r/issues/1#issuecomment-2", Body: emptyTitle("SPEC-002", "handle second")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, "### Requirement:"); got != 2 {
		t.Fatalf("expected 2 empty-title requirements preserved, got %d:\n%s", got, out)
	}
}

func TestDurableSpecDoesNotHarvestIssueURLsFromRequirementBodies(t *testing.T) {
	withBodyURL := `<!-- issue-spec:type=SPEC id=SPEC-001 version=1 -->
Agent: Coordinator
Type: SPEC
ID: SPEC-001
Status: confirmed
Scope: cli

## Requirement: References other work

The CLI MUST reference related work:

- https://github.com/o/r/issues/999

### Scenario: reference recorded

- **WHEN** the reference is recorded
- **THEN** the CLI keeps it.
`
	first, err := DurableSpec(DurableSpecOptions{
		Capability:       "cross-agent-handoff",
		ProposalIssueURL: "https://github.com/o/r/issues/1",
		SpecificationList: []SpecSource{{
			ID:   "SPEC-001",
			URL:  "https://github.com/o/r/issues/1#issuecomment-1",
			Body: withBodyURL,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := DurableSpec(DurableSpecOptions{
		Capability:       "cross-agent-handoff",
		ProposalIssueURL: "https://github.com/o/r/issues/2",
		ExistingSpecBody: first,
		SpecificationList: []SpecSource{{
			ID:   "SPEC-002",
			URL:  "https://github.com/o/r/issues/2#issuecomment-2",
			Body: specBody("SPEC-002", "Second"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// issues/999 lives only in a requirement body; it must not be promoted into
	// the Proposal Issues list (which would make it appear a second time).
	if got := strings.Count(second, "issues/999"); got != 1 {
		t.Fatalf("body issue URL leaked into Proposal Issues (count=%d):\n%s", got, second)
	}
}

func TestDurableSpecRejectsUntestableSpec(t *testing.T) {
	_, err := DurableSpec(DurableSpecOptions{
		Capability:       "issue-spec-cli",
		ProposalIssueURL: "https://github.com/o/r/issues/1",
		SpecificationList: []SpecSource{{
			ID:   "SPEC-001",
			Body: "## Requirement: Bad\n\nThis is vague.",
		}},
	})
	if err == nil {
		t.Fatal("expected untestable spec to fail")
	}
}
