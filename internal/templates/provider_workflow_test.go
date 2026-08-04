package templates

import (
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/workflow"
)

func minimalProviderPlan() workflow.ProviderPlan {
	return workflow.ProviderPlan{
		ProviderKey:      "code.example",
		DisplayName:      "Example Code",
		CodeChangeLabel:  "change",
		ChangeComment:    true,
		EvidenceSnapshot: true,
	}
}

func TestProviderWorkflowDeclaresOperationScopedHumanHandoff(t *testing.T) {
	provider := minimalProviderPlan()
	for name, content := range map[string]string{
		"notice": ProviderWorkflowNotice(provider),
		"skill":  IssueSpecProviderSkill("owner/repo", provider).Content,
	} {
		t.Run(name, func(t *testing.T) {
			for _, want := range []string{
				"change-create=false", "change-comment=true", "audit-snapshot=true",
				"Missing automatic merge support never makes this checkout planning-only",
				"provider and human reviewer own CI", "Issue-spec does not compute or execute a merge decision",
				"exact head, change link, tests, risks, and rationale", "stops before approval or merge",
			} {
				if !strings.Contains(content, want) {
					t.Fatalf("provider workflow missing %q:\n%s", want, content)
				}
			}
		})
	}
}

func TestProviderWorkflowOrdersReviewContextBeforeHumanStop(t *testing.T) {
	content := IssueSpecProviderSkill("acme/widgets", minimalProviderPlan()).Content
	for _, want := range []string{
		"active Source Binding", "Implement and validate one exact reviewable head",
		"### Implementation Rationale", "rendered body retained",
		"writer rationale anchors", "pushed exact head", "valuable unchanged text",
		"independent read-only reviewer", "exact base and pushed head", "Route every P0/P1 unchanged to its original writer",
		"same reviewer recheck", "Repeat automatically until zero P0/P1 remain", "final-head P2 unchanged",
		"ordinary change-level `change.comment` preserving `path:symbol/line`", "Never pause for P2", "rendered body while continuing",
		"non-blocking inline discussion", "summary/index", "path:symbol/line", "Publish no filler",
		"Report the exact head", "Stop. The human reviews current provider-native CI",
		"decides whether to merge in the provider UI",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider workflow missing %q:\n%s", want, content)
		}
	}
	assertTextOrder(t, content, "Implement and validate one exact reviewable head", "Select or manually create", "Dispatch the independent read-only reviewer", "Publish each final-head P2", "Validate writer rationale anchors", "Report the exact head", "Stop. The human")
}

func TestProviderWorkflowKeepsPlanningAndHistoryOutOfHandoffAuthority(t *testing.T) {
	content := IssueSpecProviderSkill("acme/widgets", minimalProviderPlan()).Content
	for _, want := range []string{
		"Optional PROCESS links remain planning metadata",
		"Historical REVIEW, VERIFY, evidence, finalization, Archive, and merge-authority data are explicit audit-only history",
		"never become handoff input",
		"ordinary top-level provider discussion", "no typed carrier, marker, rationale ID, PROCESS/SPEC binding, evidence field, gate, or merge input",
		"issue-spec never decides merge",
		"Writers need no provider access", "never guess final diff positions", "no filler, quota, or coverage comments",
		"would become an unresolved merge blocker", "secret, raw payload, or credential",
		"continued applicability", "sensitive-data absence", "Invalid, stale, or sensitive drafts",
		"independent of every code writer", "without write access or provider credentials",
		"Every P0/P1 returns unchanged to the original writer", "same reviewer rechecks the new head",
		"Only still-applicable P2 findings from the final reviewed head", "provider-neutral ordinary `change.comment` operation",
		"P2 never enters the repair loop or pauses completion", "workflow continues",
		"no typed REVIEW/VERIFY, finding evidence, receipt, readiness gate, or reviewer merge authority",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider workflow missing boundary %q:\n%s", want, content)
		}
	}
	for _, forbidden := range []string{"issue-spec code-change rationale", "issue-spec:code-change-rationale", "Rationale ID:", "merge-check", "code-change merge", "authority token"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("provider guidance restores legacy rationale mechanism %q:\n%s", forbidden, content)
		}
	}
}

func TestProviderWorkflowDescribesCreateOnlyAsCapability(t *testing.T) {
	provider := minimalProviderPlan()
	withoutCreate := IssueSpecProviderSkill("acme/widgets", provider).Content
	if !strings.Contains(withoutCreate, "Select or manually create a provider change") {
		t.Fatalf("workflow must select an existing change when create is unavailable:\n%s", withoutCreate)
	}
	provider.ChangeCreate = true
	withCreate := IssueSpecProviderSkill("acme/widgets", provider).Content
	if !strings.Contains(withCreate, "approved operator tool") || !strings.Contains(withCreate, "capability contract") {
		t.Fatalf("workflow must keep provider creation operator-owned:\n%s", withCreate)
	}
}
