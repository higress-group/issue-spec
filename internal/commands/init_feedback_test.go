package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitInstallsStandaloneFeedbackSkillByDefault(t *testing.T) {
	for _, tc := range []struct {
		name   string
		skill  string
		claude bool
	}{
		{name: "fresh repository"},
		{name: "existing Codex skill", skill: ".agents/skills/local-check/SKILL.md"},
		{name: "existing Claude skill", skill: ".claude/skills/local-check/SKILL.md", claude: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			const instructions = "# Project instructions\n\nUse the project check skill.\n"
			const projectSkill = "---\nname: local-check\ndescription: Run project checks.\n---\n\nRun the project checks.\n"
			writeWorkflowTestFile(t, "AGENTS.md", instructions)
			if tc.skill != "" {
				writeWorkflowTestFile(t, tc.skill, projectSkill)
			}

			var out, errOut bytes.Buffer
			app := newInitTestApp(t, &out, &errOut)
			if code := app.runInit(t.Context(), []string{"--repo", "acme/widgets"}); code != 0 {
				t.Fatalf("init exit = %d, stderr=%q", code, errOut.String())
			}

			const entry = ".agents/skills/issue-spec-feedback/SKILL.md"
			const guide = "../issue-spec-workflow/references/workflow-feedback.md"
			feedback := readTestFile(t, entry)
			if !strings.Contains(feedback, "name: issue-spec-feedback\n") || !strings.Contains(feedback, "("+guide+")") {
				t.Fatalf("feedback skill is missing its discoverable name or shared guide:\n%s", feedback)
			}
			if guideBody := readTestFile(t, filepath.Join(filepath.Dir(entry), guide)); !strings.Contains(guideBody, "issue-spec feedback record") {
				t.Fatal("the shared guide did not lead to feedback record")
			}

			claudePath := filepath.Join(".claude", "skills", "issue-spec-feedback")
			if tc.claude {
				if link, err := os.Readlink(claudePath); err != nil || link != "../../.agents/skills/issue-spec-feedback" {
					t.Fatalf("Claude feedback link = %q, err=%v", link, err)
				}
				if got := readTestFile(t, filepath.Join(claudePath, "SKILL.md")); got != feedback {
					t.Fatal("Claude resolved a different feedback skill")
				}
				readTestFile(t, filepath.Join(claudePath, guide))
			} else if _, err := os.Lstat(claudePath); !os.IsNotExist(err) {
				t.Fatalf("unexpected Claude feedback exposure: %v", err)
			}
			if got := readTestFile(t, "AGENTS.md"); got != instructions {
				t.Fatal("init modified project-owned instructions")
			}
			if tc.skill != "" && readTestFile(t, tc.skill) != projectSkill {
				t.Fatal("init modified a project-owned skill")
			}
			if _, err := os.Stat("issue-spec/config.yaml"); !os.IsNotExist(err) {
				t.Fatalf("default skill discovery required workflow configuration: %v", err)
			}
		})
	}
}
