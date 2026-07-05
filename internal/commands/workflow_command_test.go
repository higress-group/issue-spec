package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

func TestWorkflowValidateReportsProjectSchema(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"), "schema: custom\n")
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  proposal:
    type: proposal
    template: proposal.md
`)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "templates", "proposal.md"), "# Proposal {{.Change}}\n")

	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runWorkflow(context.Background(), []string{"validate", "--repo", "o/r", "--json"})
	if code != 0 {
		t.Fatalf("workflow validate failed code=%d stderr=%q", code, errOut.String())
	}
	var got workflowCommandResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Workflow.Source.SchemaName != "custom" || got.Workflow.Source.Kind != "issue-spec-project" {
		t.Fatalf("unexpected workflow validate result: %+v", got)
	}
}

func TestIssueCreateUsesProjectWorkflowTemplate(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"), "schema: custom\n")
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  proposal:
    type: proposal
    template: proposal.md
`)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "templates", "proposal.md"), "# Custom Proposal {{.Change}}\n\nRepo: {{.Repo}}\n")

	var createdBody string
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			createIssue: func(_ context.Context, _ string, title, body string, _ []string) (github.Issue, error) {
				createdBody = body
				return github.Issue{Number: 12, HTMLURL: "https://github.com/o/r/issues/12", Title: title}, nil
			},
		}, nil
	}
	code := app.runIssueCreate(context.Background(), "proposal", []string{"--repo", "o/r", "--change", "custom-workflow", "--json"})
	if code != 0 {
		t.Fatalf("issue create failed code=%d", code)
	}
	if !strings.Contains(createdBody, "Custom Proposal custom-workflow") {
		t.Fatalf("project template was not used:\n%s", createdBody)
	}
	if !strings.HasPrefix(createdBody, "<!-- issue-spec:issue=proposal change=custom-workflow version=1 -->") {
		t.Fatalf("project template body missing issue marker:\n%s", createdBody)
	}
	if strings.Contains(createdBody, templates.IssueSpecProjectURL) {
		t.Fatalf("project workflow template should control its own footer:\n%s", createdBody)
	}
}

func TestIssueCreateProjectWorkflowDefaultBodyIsFooterFree(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"), "schema: custom\n")
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  proposal:
    type: proposal
    template: proposal.md
`)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "templates", "proposal.md"), "{{.DefaultBody}}\n\n## Project Footer\n\nProject controlled.\n")

	var createdBody string
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			createIssue: func(_ context.Context, _ string, title, body string, _ []string) (github.Issue, error) {
				createdBody = body
				return github.Issue{Number: 13, HTMLURL: "https://github.com/o/r/issues/13", Title: title}, nil
			},
		}, nil
	}
	code := app.runIssueCreate(context.Background(), "proposal", []string{"--repo", "o/r", "--change", "custom-default-body", "--json"})
	if code != 0 {
		t.Fatalf("issue create failed code=%d", code)
	}
	if !strings.Contains(createdBody, "Project controlled.") || !strings.Contains(createdBody, "# Proposal: custom-default-body") {
		t.Fatalf("project template default body was not used:\n%s", createdBody)
	}
	if strings.Contains(createdBody, templates.IssueSpecProjectURL) {
		t.Fatalf("project template DefaultBody should not inherit issue-spec footer:\n%s", createdBody)
	}
}

func TestCommentGenerateUsesProjectTypedTemplate(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"), "schema: custom\n")
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  specs:
    type: specs
    template: spec.md
`)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "templates", "spec.md"), "{{.DefaultLogicalBody}}\n\n### Project Rule\n\n- {{.Input.requirement.title}}\n")
	input := writeTempInput(t, `{
  "requirement": {"title": "custom template", "text": "The CLI MUST render project custom templates."},
  "scenarios": [{"title":"render","when":"comment generate runs","then":"project template text is included"}]
}`)

	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runCommentGenerate(context.Background(), []string{"--type", "SPEC", "--id", "SPEC-001", "--input-file", input})
	if code != 0 {
		t.Fatalf("comment generate failed code=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Project Rule") || !strings.Contains(out.String(), "custom template") {
		t.Fatalf("project typed template was not applied:\n%s", out.String())
	}
}

func TestCommentGenerateRejectsNoncanonicalProjectSpecTemplate(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"), "schema: custom\n")
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  specs:
    type: specs
    template: spec.md
`)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "templates", "spec.md"), "# {{.ID}}\n\n{{.Input.requirement.title}}\n")
	input := writeTempInput(t, `{
  "requirement": {"title": "custom template", "text": "The CLI MUST render project custom templates."},
  "scenarios": [{"title":"render","when":"comment generate runs","then":"project template text is included"}]
}`)

	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runCommentGenerate(context.Background(), []string{"--type", "SPEC", "--id", "SPEC-001", "--input-file", input})
	if code == 0 {
		t.Fatalf("expected noncanonical project template to fail, stdout=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "not canonical") || !strings.Contains(errOut.String(), "missing `## Requirement:` heading") {
		t.Fatalf("error should explain canonical failure:\n%s", errOut.String())
	}
}

func TestWriteWorkflowArtifactsIncludesResolvedWorkflowNotice(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"), `
schema: custom
context:
  team: platform
rules:
  review: require workflow-owner approval
`)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  proposal:
    type: proposal
    template: proposal.md
    instructions: Use the proposal project guidance.
`)
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "templates", "proposal.md"), "# Proposal\n")

	result, err := writeWorkflowArtifacts(root, "owner/repo", "codex", "skills")
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkflowSource != "issue-spec-project" || result.WorkflowSchema != "custom" {
		t.Fatalf("workflow generation result missing source: %+v", result)
	}
	body := readTestFile(t, filepath.Join(root, ".agents", "skills", "issue-spec-propose", "SKILL.md"))
	if !strings.Contains(body, "Workflow Source: `issue-spec-project`") || !strings.Contains(body, "Workflow Schema: `custom`") {
		t.Fatalf("generated skill missing workflow notice:\n%s", body)
	}
	for _, want := range []string{
		"Workflow Context:",
		`"team": "platform"`,
		"Workflow Rules:",
		`"review": "require workflow-owner approval"`,
		"Artifact Instructions:",
		"`proposal` (proposal):",
		"Use the proposal project guidance.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("generated skill missing workflow notice content %q:\n%s", want, body)
		}
	}
}

func TestArchiveSelectsExistingLegacyDurableSpecPath(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeWorkflowTestFile(t, filepath.Join(root, "openspec", "specs", "compat", "spec.md"), "# Existing Legacy\n")
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-001", `## Requirement: Legacy durable path

The archive command MUST update existing openspec durable specs.

### Scenario: Existing legacy spec

- **WHEN** archive runs without --output and openspec/specs/compat/spec.md exists
- **THEN** it updates that legacy path.
`, model.BodyOptions{Status: "confirmed", Scope: "archive"})
	if err != nil {
		t.Fatal(err)
	}

	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			getIssue: func(context.Context, string, int) (github.Issue, error) {
				return github.Issue{Number: 9, HTMLURL: "https://github.com/o/r/issues/9"}, nil
			},
			listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
				return []github.Comment{{ID: 1, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-1", Body: specBody}}, nil
			},
		}, nil
	}
	code := app.runArchive(context.Background(), []string{"durable-spec", "--repo", "o/r", "--proposal", "9", "--capability", "compat"})
	if code != 0 {
		t.Fatalf("archive failed code=%d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "openspec", "specs", "compat", "spec.md")); err != nil {
		t.Fatalf("legacy durable spec not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "issue-spec", "specs", "compat", "spec.md")); !os.IsNotExist(err) {
		t.Fatalf("new issue-spec path should not be written when legacy exists, err=%v", err)
	}
}

func writeWorkflowTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
