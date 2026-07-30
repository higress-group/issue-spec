package templates

import (
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/workflow"
)

func TestProviderWorkflowKeepsRunnerEvidenceSynchronizationOptIn(t *testing.T) {
	provider := workflow.ProviderPlan{
		ProviderKey:      "code.example",
		DisplayName:      "Example Code",
		CodeChangeLabel:  "change",
		EvidenceSnapshot: true,
	}

	for name, content := range map[string]string{
		"notice": ProviderWorkflowNotice(provider),
		"skill":  IssueSpecProviderSkill("owner/repo", provider).Content,
	} {
		t.Run(name, func(t *testing.T) {
			for _, want := range []string{"before verification gates", "reads HEAD before and after fact collection",
				"returns `revision_mismatch` without a snapshot", "Runner dispatch synchronization remains an explicit `external_code.evidence.sync_before` project-policy opt-in"} {
				if !strings.Contains(content, want) {
					t.Fatalf("provider workflow missing %q:\n%s", want, content)
				}
			}
			if strings.Contains(content, "before verify and runner gates") {
				t.Fatalf("provider workflow still presents runner synchronization as a default:\n%s", content)
			}
		})
	}
}

func TestProviderWorkflowAttachesExistingChangeWithoutGitHubAssumptions(t *testing.T) {
	provider := workflow.ProviderPlan{ProviderKey: "code.example", DisplayName: "Example Code", CodeChangeLabel: "change"}
	content := IssueSpecProviderSkill("acme/widgets", provider).Content
	for _, want := range []string{
		"Source Binding",
		"code-change attach --repo acme/widgets",
		"Attach does not create the change or ingest review/CI evidence",
		"Refresh only the same active change",
		"code-change link-process --repo acme/widgets",
		"requires exactly one active `code_change`",
		"explicitly delete only the unwanted active reference",
		"code-change rationale --repo acme/widgets",
		"strict versioned Issue carrier is authoritative",
		"explicit gate-eligible issue-only fallback",
		"Published external IDs/URLs are navigation metadata, not trusted evidence",
		"Legacy version-1 carriers remain bounded gate-compatible and are never silently republished",
		"fresh exact-current REVIEW completion",
		"finding-backed consumed native-ledger evidence retained only for legacy compatibility",
		"evidence-writer identity is never treated as the code author",
		"review sync --repo acme/widgets --implement <issue> --revision <revision>",
		"even with zero findings",
		"review PROCESS, every covered change-bearing PROCESS, and every covered active SPEC",
		"never fabricate findings or hand-author its stamp",
		"Prose IDs, automatic inference, and generic approval frameworks are not carriers",
		"Archive reads implementation REVIEW completion only for implementation code_change merge policy",
		"never applies it to archive_change or mutates REVIEW",
		"do not substitute GitHub PR endpoints",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider workflow missing %q:\n%s", want, content)
		}
	}
}

func TestProviderWorkflowDocumentsRecoverableRationalePublication(t *testing.T) {
	capable := workflow.ProviderPlan{ProviderKey: "code.example", DisplayName: "Example Code",
		CodeChangeLabel: "change", ChangeComment: true}
	content := IssueSpecProviderSkill("acme/widgets", capable).Content
	for _, want := range []string{
		"stable external projection",
		"exact replay recovers lost provider or Issue acknowledgements",
		"pending never passes final gates",
		"original external comment for exact `rationale_id` replay",
		"reject conflicting reuse",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("capable provider workflow missing %q:\n%s", want, content)
		}
	}
}

func TestProviderWorkflowUsesIssueScopedTypedIDGuidance(t *testing.T) {
	provider := workflow.ProviderPlan{ProviderKey: "code.example", DisplayName: "Example Code", CodeChangeLabel: "change"}
	content := IssueSpecProviderSkill("acme/widgets", provider).Content
	for _, want := range []string{
		"<TYPE>-<issue><three-digit sequence>",
		"--process <process-id>",
		"--id <review-id>",
		"--spec <spec-id>",
		"preserve legacy IDs",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider workflow missing typed ID guidance %q:\n%s", want, content)
		}
	}
	for _, legacyExample := range []string{"PROCESS-001", "REVIEW-001", "SPEC-001"} {
		if strings.Contains(content, legacyExample) {
			t.Fatalf("provider workflow still contains legacy new-ID example %q:\n%s", legacyExample, content)
		}
	}
}

func TestProviderWorkflowKeepsProjectVerificationDeclarative(t *testing.T) {
	provider := workflow.ProviderPlan{ProviderKey: "code.example", DisplayName: "Example Code", CodeChangeLabel: "change", EvidenceSnapshot: true}
	content := IssueSpecProviderSkill("acme/widgets", provider).Content
	for _, want := range []string{
		"sealed verifier assignment carries workflow context, `rules.verify`, VERIFY instructions, affected scenarios, and exact required test/check selectors",
		"Core never executes rule prose",
		"exact sealed selector identities at the exact subject revision",
		"provider-check outcome and authority come only from the provider observation",
		"Natural-language VERIFY conclusions remain role-owned evidence and never become provider-owned authority",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("provider workflow missing declarative verification boundary %q:\n%s", want, content)
		}
	}
}
