package templates

import (
	"strings"
	"testing"
)

func TestIssueSpecSkillAndCommandTemplates(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	if got, want := len(skills), 7; got != want {
		t.Fatalf("skills = %d, want %d", got, want)
	}
	if !strings.Contains(skills[0].Content, `generatedBy: "issue-spec"`) {
		t.Fatalf("skill missing generatedBy:\n%s", skills[0].Content)
	}

	commands := IssueSpecCommandContents("owner/repo")
	if got, want := len(commands), 5; got != want {
		t.Fatalf("commands = %d, want %d", got, want)
	}
	if commands[0].ID != "propose" {
		t.Fatalf("first command ID = %q, want propose", commands[0].ID)
	}
	if !strings.Contains(commands[0].Body, "issue-spec issue create proposal --repo owner/repo") {
		t.Fatalf("command body missing repo-specific issue-spec usage:\n%s", commands[0].Body)
	}
}

func TestIssueSpecSkillsIncludeGitHubCLISupportSkill(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	if hasSkill(skills, "github") {
		t.Fatal("generic github skill should not be generated")
	}
	github := skillContent(t, skills, "issue-spec-github")
	for _, want := range []string{
		"name: issue-spec-github",
		"compatibility: Requires GitHub CLI (gh).",
		"Use GitHub CLI for GitHub issues",
		"gh auth login",
		"gh pr checks",
		"gh api",
		"issue-spec owns the proposal, design, implement",
	} {
		if !strings.Contains(github, want) {
			t.Fatalf("github skill missing %q:\n%s", want, github)
		}
	}
}

func TestIssueSpecSkillTemplatesDocumentGitHubBackendGuidance(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillContent(t, skills, "issue-spec-workflow")
	for _, want := range []string{
		"native GitHub CLI support",
		"gh auth status --active",
		"ISSUE_SPEC_GITHUB_BACKEND=rest",
		"ISSUE_SPEC_GITHUB_BACKEND=gh",
		`ISSUE_SPEC_TOKEN="$(gh auth token)"`,
		"ISSUE_SPEC_API_URL applies to the rest backend",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing %q:\n%s", want, workflow)
		}
	}

	apply := skillContent(t, skills, "issue-spec-apply")
	for _, want := range []string{
		"expected GitHub backend",
		"native gh backend",
		`ISSUE_SPEC_TOKEN="$(gh auth token)"`,
		"forced-rest compatibility path",
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply skill missing %q:\n%s", want, apply)
		}
	}
}

func TestIssueSpecSkillTemplatesDocumentSessionSourceSeparation(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillContent(t, skills, "issue-spec-workflow")
	for _, want := range []string{
		"Agent as the logical role",
		"Agent Session ID and Agent Session Source as artifact writer provenance",
		"--agent-session",
		"CODEX_THREAD_ID may override",
		"runner.public_session_id is the public /resume handle",
		"/resume <public-session-id> <answer or next instruction>",
		"Do not present Agent Session ID, CODEX_THREAD_ID, acpx record ids, or provider session ids as /resume handles",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing %q:\n%s", want, workflow)
		}
	}

	apply := skillContent(t, skills, "issue-spec-apply")
	for _, want := range []string{
		"Keep Agent as the logical role",
		"--agent-session",
		"Codex CODEX_THREAD_ID remains the artifact writer session source of truth",
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply skill missing %q:\n%s", want, apply)
		}
	}
}

func TestIssueSpecSkillTemplatesDocumentDurableArchiveGuidance(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillContent(t, skills, "issue-spec-workflow")
	archive := skillContent(t, skills, "issue-spec-archive")
	for _, want := range []string{
		"abstract long-lived --capability directory",
		"inspect existing related durable specs",
		"regroup the generated draft by stable capability modules",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing %q:\n%s", want, workflow)
		}
	}
	for _, want := range []string{
		"stable long-lived capability or domain directory",
		"not the original change/proposal name",
		"workflow-identity-and-sessions instead of agent-session-source-of-truth",
		"Inspect existing durable specs before creating or finalizing the archive PR",
		"issue-spec/specs/<capability>/spec.md",
		"issue-spec/specs/*/spec.md",
		"update, merge, or reorganize existing durable requirements",
		"Reconcile it with any existing related durable specs",
		"regroup related source SPEC content into durable capability modules",
		"Source SPEC links for traceability",
	} {
		if !strings.Contains(archive, want) {
			t.Fatalf("archive skill missing %q:\n%s", want, archive)
		}
	}
}

func TestIssueSpecSkillsDirectAgentsToGenerators(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")

	propose := skillContent(t, skills, "issue-spec-propose")
	for _, want := range []string{
		"issue-spec comment generate --type SPEC",
		"--allow-noncanonical",
		"issue-spec comment generate --type TASK",
	} {
		if !strings.Contains(propose, want) {
			t.Fatalf("propose skill missing generator guidance %q:\n%s", want, propose)
		}
	}

	workflow := skillContent(t, skills, "issue-spec-workflow")
	if !strings.Contains(workflow, "issue-spec comment generate") {
		t.Fatalf("workflow skill missing generator guidance:\n%s", workflow)
	}

	// The generic REVIEW guidance must preserve review sync ownership.
	review := skillContent(t, skills, "issue-spec-review")
	if !strings.Contains(review, "Review Sync Summary") || !strings.Contains(review, "issue-spec comment generate --type REVIEW") {
		t.Fatalf("review skill missing generate/review-sync guidance:\n%s", review)
	}

	verify := skillContent(t, skills, "issue-spec-verify")
	if !strings.Contains(verify, "issue-spec comment generate --type VERIFY") {
		t.Fatalf("verify skill missing VERIFY generator guidance:\n%s", verify)
	}
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

func hasSkill(skills []RenderedSkill, name string) bool {
	for _, skill := range skills {
		if skill.Name == name {
			return true
		}
	}
	return false
}
