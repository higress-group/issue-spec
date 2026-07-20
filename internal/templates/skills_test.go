package templates

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssueSpecSkillAndCommandTemplates(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	if got, want := len(skills), 6; got != want {
		t.Fatalf("skills = %d, want %d", got, want)
	}
	if !strings.Contains(skills[0].Content, `generatedBy: "issue-spec"`) {
		t.Fatalf("skill missing generatedBy:\n%s", skills[0].Content)
	}
	commands := IssueSpecCommandContents("owner/repo")
	if got, want := len(commands), 4; got != want {
		t.Fatalf("commands = %d, want %d", got, want)
	}
	if commands[0].ID != "propose" || !strings.Contains(commands[0].Body, "owner/repo") {
		t.Fatalf("unexpected first command: %+v", commands[0])
	}
	for _, skill := range skills {
		if skill.Name == "issue-spec-archive" || strings.Contains(skill.Content, "Issue Spec Archive") {
			t.Fatalf("normal generated skills still expose Archive: %s", skill.Name)
		}
	}
	for _, command := range commands {
		if command.ID == "archive" || strings.Contains(command.Body, "Issue Spec Archive") {
			t.Fatalf("normal generated commands still expose Archive: %s", command.ID)
		}
	}
	propose := skillContent(t, skills, "issue-spec-propose")
	for _, want := range []string{
		"rules.language",
		"rules.language_instructions",
		"pass an explicit `--title` for Proposal, Design, and Implement",
		"retains an English stage prefix",
	} {
		if !strings.Contains(propose, want) {
			t.Fatalf("issue-spec-propose missing title guidance %q", want)
		}
	}
}

func TestCoordinatorGuidanceKeepsActionsStopsAndRecovery(t *testing.T) {
	workflow := skillContent(t, IssueSpecSkills("owner/repo"), "issue-spec-workflow")
	wants := []string{
		"status --repo owner/repo", "--summary --json", "structured detail action", "full --json",
		"comment get", "active/status/history filters", "narrow direct-PR fast path",
		"one independently verifiable Design invariant", "bounded context and working set", "stable interface",
		"stop the Implement transition as blocked", "acceptance consequences", "request human direction",
		"workspace prepare -> real non-Coordinator", "never implements/tests/commits", "sealed assignment",
		"design_context.source_url", "without comments, timeline, history, or gates", "stops on any conflict",
		"Exact revision, ownership, DCO, required tests", "independent review", "review/fix convergence",
		"same plan digest and checkpoint", "Cleanup is explicit", "destructive", "retain uncertain or unintegrated work",
		"verify --summary --json", "authoritative full final verify", "pr link-issues is the final PR-body write",
		"durable-spec preview/apply", "issue-spec/durable-spec", "issue close-change",
	}
	for _, want := range wants {
		if !strings.Contains(workflow, want) {
			t.Fatalf("coordinator guidance missing %q:\n%s", want, workflow)
		}
	}
	for _, stale := range []string{
		"Codex CODEX_THREAD_ID remains the artifact writer session source of truth",
		"CODEX_THREAD_ID may override",
		"split into parallel worker PROCESS nodes only when file/module write ownership is provably disjoint",
		"one repair PROCESS per finding",
	} {
		if strings.Contains(workflow, stale) {
			t.Fatalf("coordinator guidance contains stale rule %q", stale)
		}
	}
}

func TestRoleGuidanceIsBoundedAndRuntimeNeutral(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	cases := []struct {
		name      string
		required  []string
		forbidden []string
	}{
		{
			name: "issue-spec-apply",
			required: []string{"sealed implementation assignment", "design_context.read_mode=complete-issue-body",
				"issue-spec read issue --repo owner/repo", "Stop and report any conflict", "assigned worktree and owned paths",
				"assigned generators", "focused verification", "exactly one DCO commit", "bounded handoff/result receipt"},
			forbidden: []string{"workflow workspace prepare", "workflow reconcile", "pr link-issues", "code-change attach", "archive durable-spec", "SPEC <-> TASK"},
		},
		{
			name: "issue-spec-review",
			required: []string{"sealed review assignment", "exact subject revision", "immutable snapshot/diff",
				"design_context.read_mode=complete-issue-body", "read the complete Design", "Stop on conflict",
				"actionable findings", "explicit no-finding verdict", "bounded review receipt/sync result",
				"issue-spec link --repo owner/repo --from REVIEW-<n> --from-issue <implement-issue> --to PROCESS-<n> --to-issue <implement-issue>",
				"issue-spec link --repo owner/repo --from REVIEW-<n> --from-issue <implement-issue> --to SPEC-<n> --to-issue <proposal-issue>"},
			forbidden: []string{"workflow workspace prepare", "workflow reconcile", "pr link-issues", "code-change attach", "archive durable-spec", "SPEC <-> TASK"},
		},
		{
			name: "issue-spec-verify",
			required: []string{"sealed verification assignment", "exact immutable subject revision", "required focused tests/checks",
				"provider-owned check identity", "bounded VERIFY receipt", "Do not collect or pass runtime-specific session IDs"},
			forbidden: []string{"workflow workspace prepare", "workflow reconcile", "pr link-issues", "code-change attach", "archive durable-spec", "SPEC <-> TASK"},
		},
	}
	for _, tc := range cases {
		content := skillContent(t, skills, tc.name)
		for _, want := range tc.required {
			if !strings.Contains(content, want) {
				t.Fatalf("%s missing bounded role contract %q:\n%s", tc.name, want, content)
			}
		}
		for _, forbidden := range tc.forbidden {
			if strings.Contains(content, forbidden) {
				t.Fatalf("%s leaks coordinator policy %q:\n%s", tc.name, forbidden, content)
			}
		}
		for _, stale := range []string{"CODEX_THREAD_ID", "--agent-session", "session source of truth", "agent-session value is required"} {
			if strings.Contains(content, stale) {
				t.Fatalf("%s depends on runtime-specific session metadata %q", tc.name, stale)
			}
		}
	}
}

func TestGeneratedGuidanceDeterministicSizeBudgets(t *testing.T) {
	type budget struct {
		maxBytes    int
		maxHeadings int
		maxItems    int
	}
	budgets := map[string]budget{
		"issue-spec-workflow": {maxBytes: 10000, maxHeadings: 8, maxItems: 40},
		"issue-spec-propose":  {maxBytes: 5000, maxHeadings: 4, maxItems: 16},
		"issue-spec-apply":    {maxBytes: 5000, maxHeadings: 5, maxItems: 12},
		"issue-spec-review":   {maxBytes: 5000, maxHeadings: 5, maxItems: 12},
		"issue-spec-verify":   {maxBytes: 4000, maxHeadings: 5, maxItems: 10},
	}
	for _, skill := range IssueSpecSkills("owner/repo") {
		limit, ok := budgets[skill.Name]
		if !ok {
			continue
		}
		if got := len([]byte(skill.Content)); got > limit.maxBytes {
			t.Errorf("%s UTF-8 bytes = %d, budget %d", skill.Name, got, limit.maxBytes)
		}
		if got := countInstructionLines(skill.Content, "#"); got > limit.maxHeadings {
			t.Errorf("%s headings = %d, budget %d", skill.Name, got, limit.maxHeadings)
		}
		if got := countListItems(skill.Content); got > limit.maxItems {
			t.Errorf("%s list items = %d, budget %d", skill.Name, got, limit.maxItems)
		}
	}
}

func TestCheckedInCodexClaudeGuidanceMatchesTemplates(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, skill := range IssueSpecSkills("higress-group/issue-spec") {
		var variants [][]byte
		for _, base := range []string{filepath.Join(".agents", "skills"), filepath.Join(".claude", "skills")} {
			path := filepath.Join(root, base, skill.Name, "SKILL.md")
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			variants = append(variants, got)
		}
		if !bytes.Equal(variants[0], variants[1]) {
			t.Fatalf("Codex/Claude skill variants differ: %s", skill.Name)
		}
	}
	for _, command := range IssueSpecCommandContents("higress-group/issue-spec") {
		path := filepath.Join(root, ".claude", "commands", "issue-spec", command.ID+".md")
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		_ = got // PROCESS-008 refreshes managed generated assets from these templates.
	}
}

func TestReferenceOwnershipExamplesUseRecursiveDirectoryDeclarations(t *testing.T) {
	for _, relative := range []string{"reference.md", "reference.zh-CN.md"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "docs", relative))
		if err != nil {
			t.Fatal(err)
		}
		content := string(body)
		if !strings.Contains(content, `"owned_areas": ["internal/templates/**", "docs/reference.md"]`) ||
			!strings.Contains(content, `"write_ownership": ["internal/templates/**", "docs/reference.md"]`) {
			t.Fatalf("%s missing recursive ownership examples", relative)
		}
	}
}

func TestIssueSpecSkillsIncludeBoundedGitHubSupport(t *testing.T) {
	github := skillContent(t, IssueSpecSkills("owner/repo"), "issue-spec-github")
	for _, want := range []string{"Requires GitHub CLI (gh).", "gh auth login", "gh pr checks", "issue-spec comment create", "Never use GitHub CLI"} {
		if !strings.Contains(github, want) {
			t.Fatalf("github skill missing %q", want)
		}
	}
}

func countInstructionLines(content, prefix string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

func countListItems(content string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || (len(trimmed) > 2 && trimmed[0] >= '0' && trimmed[0] <= '9' && strings.Contains(trimmed[:3], ".")) {
			count++
		}
	}
	return count
}

func skillContent(t *testing.T, skills []RenderedSkill, name string) string {
	t.Helper()
	for _, skill := range skills {
		if skill.Name == name {
			return skill.Content
		}
	}
	t.Fatalf("skill %q not found", name)
	return ""
}
