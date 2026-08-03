package templates

import (
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/workflow"
)

func minimalProviderPlan() workflow.ProviderPlan {
	return workflow.ProviderPlan{
		ProviderKey:                  "code.example",
		DisplayName:                  "Example Code",
		CodeChangeLabel:              "change",
		SemanticGeneration:           codereview.MergeAuthorityGeneration,
		ProviderBuildIdentity:        "example-bridge@sha256:0123456789abcdef",
		ReviewDecision:               true,
		AuthoritativeCheckConclusion: true,
		MergeConditional:             true,
	}
}

func TestProviderWorkflowDeclaresOneImmutableAuthoritySet(t *testing.T) {
	provider := minimalProviderPlan()
	for name, content := range map[string]string{
		"notice": ProviderWorkflowNotice(provider),
		"skill":  IssueSpecProviderSkill("owner/repo", provider).Content,
	} {
		t.Run(name, func(t *testing.T) {
			for _, want := range []string{
				"minimal-merge-authority/v1", "example-bridge@sha256:0123456789abcdef",
				"review-decision=true", "authoritative-check-conclusion=true", "merge-conditional=true",
				"stable authenticated reviewers", "opaque provider-native keys", "provider-selected current conclusion",
				"provider-issued complete authority token", "Ordinary GitHub REST read-then-write",
			} {
				if !strings.Contains(content, want) {
					t.Fatalf("provider workflow missing %q:\n%s", want, content)
				}
			}
		})
	}
}

func TestProviderWorkflowOrdersReadOnlyDecisionBeforeConditionalMerge(t *testing.T) {
	content := IssueSpecProviderSkill("acme/widgets", minimalProviderPlan()).Content
	for _, want := range []string{
		"active Source Binding", "Run release preflight", "provider-native policy-complete review",
		"### Implementation Rationale", "before requesting human review", "direct single writer or managed PROCESS",
		"retain the rendered body", "do not claim review handoff complete",
		"writer anchors", "map them to changed lines", "valid text",
		"non-blocking inline discussion", "summary/index", "path:symbol/line", "Publish no filler",
		"issue-spec merge-check --repo acme/widgets", "read-only current decision",
		"issue-spec code-change merge", "caller-observed `--expected-head`",
		"Freshly observe merged state", "idempotent selected-Issue reconciliation",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider workflow missing %q:\n%s", want, content)
		}
	}
	assertTextOrder(t, content, "Run release preflight", "Once the exact change is reviewable", "provider-native policy-complete review", "issue-spec merge-check", "issue-spec code-change merge", "Freshly observe merged state")
}

func TestProviderWorkflowKeepsPlanningAndHistoryOutOfAuthority(t *testing.T) {
	content := IssueSpecProviderSkill("acme/widgets", minimalProviderPlan()).Content
	for _, want := range []string{
		"Optional PROCESS links remain planning metadata and never enter merge-check",
		"Do not synchronize them into typed workflow comments",
		"Historical REVIEW, VERIFY, PROCESS evidence, rationale, receipts, coverage, finalization, and Archive data are audit-only",
		"never become merge input",
		"ordinary top-level provider discussion", "no typed carrier, marker, rationale ID, PROCESS/SPEC binding, evidence field, gate, or merge input",
		"merge-check and merge authority remain unchanged",
		"Writers need no provider access", "never guess final diff positions", "no filler, quota, or coverage comments",
		"would become an unresolved merge blocker", "secret, raw payload, or credential",
		"continued applicability", "sensitive-data absence", "Invalid, stale, or sensitive drafts",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider workflow missing boundary %q:\n%s", want, content)
		}
	}
	for _, forbidden := range []string{"issue-spec code-change rationale", "issue-spec:code-change-rationale", "Rationale ID:"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("provider guidance restores legacy rationale mechanism %q:\n%s", forbidden, content)
		}
	}
}

func TestProviderWorkflowDescribesCreateOnlyAsCapability(t *testing.T) {
	provider := minimalProviderPlan()
	withoutCreate := IssueSpecProviderSkill("acme/widgets", provider).Content
	if !strings.Contains(withoutCreate, "Select a pre-existing provider change") {
		t.Fatalf("workflow must select an existing change when create is unavailable:\n%s", withoutCreate)
	}
	provider.ChangeCreate = true
	withCreate := IssueSpecProviderSkill("acme/widgets", provider).Content
	if !strings.Contains(withCreate, "approved operator tool") || !strings.Contains(withCreate, "capability contract") {
		t.Fatalf("workflow must keep provider creation operator-owned:\n%s", withCreate)
	}
}
