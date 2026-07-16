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
			for _, want := range []string{"before verification gates", "Runner dispatch synchronization remains an explicit `external_code.evidence.sync_before` project-policy opt-in"} {
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
