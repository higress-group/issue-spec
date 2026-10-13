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
				"No automatic reviewer, PR/MR, push or comment is implied",
			} {
				if !strings.Contains(content, want) {
					t.Fatalf("provider workflow missing %q:\n%s", want, content)
				}
			}
		})
	}
}

func TestProviderWorkflowRoutesReviewToSharedGuidance(t *testing.T) {
	content := IssueSpecProviderSkill("acme/widgets", minimalProviderPlan()).Content
	for _, want := range []string{"active Source Binding", "Implement and validate one exact reviewable head", "implementation-review.md", "configured project review guidance", "require separate authorization"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q", want)
		}
	}
	for _, forbidden := range []string{"Dispatch the independent", "Repeat automatically", "### Implementation Rationale"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("duplicate policy %q", forbidden)
		}
	}
}

func TestProviderWorkflowKeepsPlanningAndHistoryOutOfHandoffAuthority(t *testing.T) {
	content := IssueSpecProviderSkill("acme/widgets", minimalProviderPlan()).Content
	for _, want := range []string{"Optional PROCESS links remain planning metadata", "explicit audit-only history", "never become handoff input", "No automatic reviewer", "Provider executables, arguments, environment, and credentials are operator-owned"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing boundary %q", want)
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
