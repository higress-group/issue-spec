package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/feedback"
	"github.com/higress-group/issue-spec/internal/github"
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
			if guideBody := readTestFile(t, filepath.Join(filepath.Dir(entry), guide)); !strings.Contains(guideBody, " feedback record ") {
				t.Fatal("the shared guide did not lead to feedback record")
			}
			workflow := readTestFile(t, ".agents/skills/issue-spec-workflow/SKILL.md")
			if !strings.Contains(workflow, "(references/workflow-feedback.md)") {
				t.Fatal("workflow and standalone feedback must use the same guide")
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

func TestGitHubInitRejectsInvalidWorkflowBeforeWrites(t *testing.T) {
	const bindingPath = ".issue-spec/config.json"
	const binding = `{"version":1,"repo":"acme/previous","profile":"github","hostname":"github.com"}`
	const feedbackPath = ".agents/skills/issue-spec-feedback/SKILL.md"
	for _, tc := range []struct {
		name string
		path string
		body string
		args []string
		err  string
	}{
		{name: "malformed workflow without detected tools", path: "issue-spec/config.yaml", body: "rules: [", err: "yaml:"},
		{name: "project-owned skill collision", path: feedbackPath, body: "Project-owned feedback.", args: []string{"--language", "zh"}, err: "locally changed or not managed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeWorkflowTestFile(t, bindingPath, binding)
			writeWorkflowTestFile(t, tc.path, tc.body)
			var out, errOut bytes.Buffer
			app := newInitTestApp(t, &out, &errOut)
			labelWrites := 0
			app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
				return fakeGitHubBackend{
					info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
					user: github.User{Login: "octocat"},
					createLabel: func(_ context.Context, _ string, name, _, _ string) (github.LabelResult, error) {
						labelWrites++
						return github.LabelResult{Name: name, Created: true}, nil
					},
				}, nil
			}

			args := append([]string{"--repo", "acme/widgets"}, tc.args...)
			if code := app.runInit(t.Context(), args); code == 0 || !strings.Contains(errOut.String(), tc.err) {
				t.Fatalf("init exit = %d, stderr=%q", code, errOut.String())
			}
			if got := readTestFile(t, bindingPath); got != binding || labelWrites != 0 {
				t.Fatalf("failed init changed binding to %q and attempted %d label writes", got, labelWrites)
			}
			if got := readTestFile(t, tc.path); got != tc.body {
				t.Fatalf("failed init changed project file to %q", got)
			}
			for _, path := range []string{"issue-spec/config.yaml", feedbackPath, workflowManifestPath} {
				if path == tc.path {
					continue
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("failed init created %s: %v", path, err)
				}
			}
		})
	}
}

func TestGeneratedFeedbackPreviewUsesBoundProfile(t *testing.T) {
	const repo = "acme/widgets"
	occurrence := readTestFile(t, "../../testdata/workflow-feedback/release-0.6.3.json")
	for _, tc := range []struct {
		kind    auth.ProfileKind
		profile string
	}{
		{kind: auth.ProfileKindGitHub, profile: "github"},
		{kind: auth.ProfileKindHosted, profile: "team"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			clearCommandAuthEnv(t)
			t.Setenv(auth.GitHubBackendEnv, "rest")
			t.Setenv(auth.ProfileEnv, "")
			t.Setenv("ISSUE_SPEC_TOKEN", "test-reporter")
			t.Chdir(t.TempDir())
			if err := os.Mkdir(".git", 0o755); err != nil {
				t.Fatal(err)
			}

			target, _ := newFeedbackTestServer(t)
			other, otherRequests := newFeedbackTestServer(t)
			profile := auth.Profile{Name: tc.profile, Kind: tc.kind, Hostname: "github.com",
				APIURL: target.URL, NativeAPIURL: target.URL + "/api/v1", WebURL: target.URL, ServerInstanceID: "intended"}
			if err := auth.SaveProfile(profile, false); err != nil {
				t.Fatal(err)
			}
			if err := auth.SaveProfile(auth.Profile{Name: "other", Kind: auth.ProfileKindHosted,
				APIURL: other.URL, NativeAPIURL: other.URL + "/api/v1", WebURL: other.URL, ServerInstanceID: "other"}, true); err != nil {
				t.Fatal(err)
			}
			if tc.kind == auth.ProfileKindGitHub {
				// An explicitly active profile also takes precedence over the GitHub checkout marker.
				t.Setenv(auth.ProfileEnv, "other")
			}
			writeWorkflowTestFile(t, ".issue-spec/config.json", fmt.Sprintf(`{"repo":%q,"profile":%q,"hostname":"github.com"}`, repo, profile.Name))
			writeWorkflowTestFile(t, "occurrence.json", occurrence)
			if _, err := writeWorkflowArtifacts(".", repo, "", workflowDeliverySkills); err != nil {
				t.Fatal(err)
			}
			guide := readTestFile(t, ".agents/skills/issue-spec-workflow/references/workflow-feedback.md")
			var args []string
			for line := range strings.Lines(guide) {
				if strings.HasPrefix(line, "issue-spec ") && strings.Contains(line, " feedback record ") {
					resolved := strings.NewReplacer(`"$feedback_profile"`, profile.Name, `"$feedback_repo"`, repo)
					args = strings.Fields(resolved.Replace(line))[1:]
					break
				}
			}
			if len(args) == 0 {
				t.Fatal("the shared guide has no feedback preview example")
			}
			var out, errOut bytes.Buffer
			if code := Execute(args, strings.NewReader(""), &out, &errOut); code != 0 {
				t.Fatalf("feedback preview exit = %d, stderr=%q", code, errOut.String())
			}
			var result feedback.Result
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.URL != target.URL+"/acme/widgets/issues/42" || otherRequests.Load() != 0 ||
				!result.DryRun || result.EventStatus != "would_record" || result.ConfirmedCount != 1 {
				t.Fatalf("feedback preview = %+v, requests to global profile = %d", result, otherRequests.Load())
			}
		})
	}
}

func newFeedbackTestServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("feedback preview attempted %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/user":
			writeTestJSON(w, github.User{ID: 7, Login: "reporter"})
		case "/repos/acme/widgets/issues/42":
			writeTestJSON(w, github.Issue{Number: 42, State: "open", HTMLURL: "http://" + r.Host + "/acme/widgets/issues/42"})
		case "/repos/acme/widgets/issues/42/comments":
			writeTestJSON(w, []github.Comment{})
		case "/repos/acme/widgets/labels/issue-spec/feedback-policy":
			http.NotFound(w, r)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests
}
