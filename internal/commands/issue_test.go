package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestEnsureIssueBodyMarkerForCreateBodyFile(t *testing.T) {
	body, err := ensureIssueBodyMarker("proposal", "change-name", "# Proposal\n\nReal content.\n")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(body, "<!-- issue-spec:issue=proposal change=change-name version=1 -->\n") {
		t.Fatalf("body missing proposal marker:\n%s", body)
	}
	if !strings.Contains(body, "Real content.") {
		t.Fatalf("body lost content:\n%s", body)
	}
}

func TestEnsureIssueBodyMarkerRejectsWrongIssueClass(t *testing.T) {
	_, err := ensureIssueBodyMarker("proposal", "change-name", "<!-- issue-spec:issue=design change=change-name version=1 -->\n# Proposal\n")
	if err == nil {
		t.Fatal("expected wrong issue marker class to fail")
	}
}

func TestEnsureIssueBodyMarkerIgnoresProseMention(t *testing.T) {
	body, err := ensureIssueBodyMarker("proposal", "change-name", "# Proposal\n\nMention `issue-spec:issue=` in prose.\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "<!-- issue-spec:issue=proposal change=change-name version=1 -->\n") {
		t.Fatalf("body missing prepended marker:\n%s", body)
	}
}

func TestPreserveIssueBodyMarkerForUpdateBodyFile(t *testing.T) {
	existing := "<!-- issue-spec:issue=design change=change-name version=1 -->\n# Design\n\nTBD"
	updated, err := preserveIssueBodyMarker(existing, "# Design\n\nReal design.\n")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(updated, "<!-- issue-spec:issue=design change=change-name version=1 -->\n") {
		t.Fatalf("updated body missing preserved marker:\n%s", updated)
	}
	if strings.Contains(updated, "TBD") {
		t.Fatalf("updated body retained stale placeholder:\n%s", updated)
	}
}

func TestPreserveIssueBodyMarkerRejectsReplacementClassMismatch(t *testing.T) {
	existing := "<!-- issue-spec:issue=design change=change-name version=1 -->\n# Design\n"
	replacement := "<!-- issue-spec:issue=proposal change=change-name version=1 -->\n# Design\n"
	if _, err := preserveIssueBodyMarker(existing, replacement); err == nil {
		t.Fatal("expected replacement marker mismatch to fail")
	}
}

func TestIssueUpdateSummaryIsNotTypedComment(t *testing.T) {
	body := renderIssueUpdateSummary(5, "https://github.com/o/r/issues/5", "Replaced placeholder body with a concrete proposal.")

	if model.IsLikelyTyped(body) {
		t.Fatalf("issue update summary should not be parsed as a typed comment:\n%s", body)
	}
	if !strings.Contains(body, "Replaced placeholder body") {
		t.Fatalf("summary body missing content:\n%s", body)
	}
}

func TestIssueUpdateRejectsSummaryFlagConflict(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Execute([]string{
		"issue", "update",
		"--repo", "o/r",
		"--issue", "1",
		"--title", "new title",
		"--summary", "inline",
		"--summary-file", "summary.md",
	}, strings.NewReader(""), &out, &errOut)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "--summary and --summary-file cannot both be provided") {
		t.Fatalf("missing conflict error: %s", errOut.String())
	}
}

func TestIssueCreateBodyFileDerivesStandardizedTitle(t *testing.T) {
	bodyPath := filepath.Join(t.TempDir(), "proposal.md")
	if err := os.WriteFile(bodyPath, []byte("# Proposal: standardize issue-spec issue titles\n\nConcrete proposal body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.createIssue = func(_ context.Context, repo, title, body string, labels []string) (github.Issue, error) {
			if repo != "o/r" {
				t.Fatalf("repo = %q", repo)
			}
			if title != "Proposal: standardize issue-spec issue titles" {
				t.Fatalf("title = %q", title)
			}
			if !strings.HasPrefix(body, "<!-- issue-spec:issue=proposal change=issue-title-style version=1 -->\n") {
				t.Fatalf("body missing marker:\n%s", body)
			}
			if !strings.Contains(body, "Concrete proposal body.") {
				t.Fatalf("body missing content:\n%s", body)
			}
			if len(labels) != 1 || labels[0] != "issue-spec/proposal" {
				t.Fatalf("labels = %#v", labels)
			}
			return github.Issue{Number: 21, HTMLURL: "https://github.com/o/r/issues/21", Title: title}, nil
		}
	})

	code := app.runIssueCreate(context.Background(), "proposal", []string{"--repo", "o/r", "--change", "issue-title-style", "--body-file", bodyPath, "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	var got struct {
		OK    bool   `json:"ok"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Title != "Proposal: standardize issue-spec issue titles" {
		t.Fatalf("unexpected output: %+v", got)
	}
}

func TestIssueCreateTitleOverrideWins(t *testing.T) {
	bodyPath := filepath.Join(t.TempDir(), "design.md")
	if err := os.WriteFile(bodyPath, []byte("# Design: ignored generated title\n\nDesign body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(_ context.Context, repo string, issueNumber int) ([]github.Comment, error) {
			if repo != "o/r" || issueNumber != 21 {
				t.Fatalf("unexpected proposal comments args repo=%q issue=%d", repo, issueNumber)
			}
			specBody := mustTypedBody(t, "SPEC", "SPEC-001", "confirmed", "title", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y\n")
			return []github.Comment{{ID: 1, HTMLURL: "https://github.com/o/r/issues/21#issuecomment-1", URL: "https://api.github.com/repos/o/r/issues/comments/1", Body: specBody}}, nil
		}
		f.createIssue = func(_ context.Context, _ string, title, body string, _ []string) (github.Issue, error) {
			if title != "Custom design title" {
				t.Fatalf("title = %q", title)
			}
			if !strings.Contains(body, "# Design: ignored generated title") {
				t.Fatalf("body was not preserved:\n%s", body)
			}
			return github.Issue{Number: 103, HTMLURL: "https://github.com/o/r/issues/103", Title: title}, nil
		}
	})

	code := app.runIssueCreate(context.Background(), "design", []string{"--repo", "o/r", "--change", "issue-title-style", "--proposal", "21", "--body-file", bodyPath, "--title", "Custom design title", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
}

func mustTypedBody(t *testing.T, typ, id, status, scope, logical string) string {
	t.Helper()
	body, err := model.EnsureTypedBody(typ, id, logical, model.BodyOptions{Status: status, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
