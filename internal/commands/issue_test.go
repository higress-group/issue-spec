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
	if strings.Contains(body, "- Proposal Issue:") || strings.Contains(body, "- Design Issue:") {
		t.Fatalf("proposal body gained a predecessor link:\n%s", body)
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

func TestPreserveIssueBodyMetadataForUpdateBodyFile(t *testing.T) {
	existing := "<!-- issue-spec:issue=design change=change-name version=1 -->\n# Design\n\n- Proposal Issue: 21\n\nTBD"
	updated, err := preserveIssueBodyMetadata(existing, "# Design\n\nReal design.\n")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(updated, "<!-- issue-spec:issue=design change=change-name version=1 -->\n") {
		t.Fatalf("updated body missing preserved marker:\n%s", updated)
	}
	if strings.Contains(updated, "TBD") {
		t.Fatalf("updated body retained stale placeholder:\n%s", updated)
	}
	if strings.Count(updated, "- Proposal Issue: 21") != 1 {
		t.Fatalf("updated body missing preserved predecessor:\n%s", updated)
	}
}

func TestPreserveIssueBodyMetadataRejectsReplacementClassMismatch(t *testing.T) {
	existing := "<!-- issue-spec:issue=design change=change-name version=1 -->\n# Design\n"
	replacement := "<!-- issue-spec:issue=proposal change=change-name version=1 -->\n# Design\n"
	if _, err := preserveIssueBodyMetadata(existing, replacement); err == nil {
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

func TestIssueCreateSimpleCreatesOrdinaryUnlabelledIssue(t *testing.T) {
	bodyPath := filepath.Join(t.TempDir(), "request.md")
	if err := os.WriteFile(bodyPath, []byte("Please support a smaller setup flow.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.createIssue = func(_ context.Context, repo, title, body string, labels []string) (github.Issue, error) {
			if repo != "o/r" || title != "Smaller setup" || body != "Please support a smaller setup flow.\n" {
				t.Fatalf("create repo=%q title=%q body=%q", repo, title, body)
			}
			if len(labels) != 0 {
				t.Fatalf("simple issue labels=%v", labels)
			}
			return github.Issue{Number: 12, HTMLURL: "https://github.com/o/r/issues/12", Title: title}, nil
		}
	})
	code := app.runIssue(t.Context(), []string{"create", "simple", "--repo", "o/r", "--title", "Smaller setup", "--body-file", bodyPath, "--json"})
	if code != 0 || !strings.Contains(out.String(), `"type": "simple"`) || !strings.Contains(out.String(), `"number": 12`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestIssueCreateSimpleRejectsTypedIssueMarkerBeforeAuthentication(t *testing.T) {
	bodyPath := filepath.Join(t.TempDir(), "typed.md")
	if err := os.WriteFile(bodyPath, []byte("<!-- issue-spec:issue=proposal change=x version=1 -->\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	selected := false
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		selected = true
		return auth.GitHubBackendSelection{}, nil
	}
	code := app.runIssue(t.Context(), []string{"create", "simple", "--repo", "o/r", "--title", "not ordinary", "--body-file", bodyPath})
	if code != 2 || selected || !strings.Contains(errOut.String(), "must not contain") {
		t.Fatalf("exit=%d selected=%t stdout=%q stderr=%q", code, selected, out.String(), errOut.String())
	}
}

func TestIssueListReturnsStableJSONAndExcludesPullRequests(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssues = func(_ context.Context, repo string, opts github.ListIssueOptions) ([]github.Issue, error) {
			if repo != "o/r" || opts.State != "closed" {
				t.Fatalf("list args repo=%q opts=%+v", repo, opts)
			}
			pullRequest := struct{}{}
			return []github.Issue{
				{Number: 1, Title: "ordinary", State: "CLOSED", HTMLURL: "https://github.com/o/r/issues/1", Body: "complete body"},
				{Number: 2, Title: "pull request", State: "closed", HTMLURL: "https://github.com/o/r/pull/2", Body: "pr body", PullRequest: &pullRequest},
			}, nil
		}
	})

	code := app.runIssue(t.Context(), []string{"list", "--repo", "o/r", "--state", "closed", "--json"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	var got issueListResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Repo != "o/r" || got.State != "closed" || len(got.Issues) != 1 {
		t.Fatalf("result=%+v", got)
	}
	if issue := got.Issues[0]; issue.Number != 1 || issue.Title != "ordinary" || issue.State != "closed" ||
		issue.URL != "https://github.com/o/r/issues/1" || issue.Body != "complete body" {
		t.Fatalf("issue=%+v", issue)
	}
}

func TestIssueListDefaultsOpenAndReturnsEmptyArray(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssues = func(_ context.Context, _ string, opts github.ListIssueOptions) ([]github.Issue, error) {
			if opts.State != "open" {
				t.Fatalf("state=%q", opts.State)
			}
			return nil, nil
		}
	})
	if code := app.runIssue(t.Context(), []string{"list", "--repo", "o/r", "--json"}); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"issues": []`) {
		t.Fatalf("empty issues must encode as []: %s", out.String())
	}
}

func TestIssueListRejectsInvalidUsageBeforeBackendSelection(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "json required", args: []string{"list", "--repo", "o/r"}, want: "--json is required"},
		{name: "invalid state", args: []string{"list", "--repo", "o/r", "--state", "merged", "--json"}, want: "--state must be open, closed, or all"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			selected := false
			app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
				selected = true
				return auth.GitHubBackendSelection{}, nil
			}
			if code := app.runIssue(t.Context(), tt.args); code != 2 || selected || !strings.Contains(errOut.String(), tt.want) {
				t.Fatalf("exit=%d selected=%t stdout=%q stderr=%q", code, selected, out.String(), errOut.String())
			}
		})
	}
}

func TestIssueCloseAndReopenAreIdempotent(t *testing.T) {
	for _, tt := range []struct {
		name        string
		command     string
		current     string
		wantTarget  string
		wantChanged bool
		wantPatches int
	}{
		{name: "close open", command: "close", current: "open", wantTarget: "closed", wantChanged: true, wantPatches: 1},
		{name: "close closed", command: "close", current: "closed", wantTarget: "closed", wantChanged: false},
		{name: "reopen closed", command: "reopen", current: "closed", wantTarget: "open", wantChanged: true, wantPatches: 1},
		{name: "reopen open", command: "reopen", current: "open", wantTarget: "open", wantChanged: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			getCalls, patchCalls := 0, 0
			app := newApp(strings.NewReader(""), &out, &errOut)
			app.selectGitHubBackend = ghSelection
			app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
				f.getIssue = func(_ context.Context, repo string, number int) (github.Issue, error) {
					getCalls++
					return github.Issue{Number: number, State: tt.current, HTMLURL: "https://github.com/o/r/issues/9"}, nil
				}
				f.updateIssue = func(_ context.Context, _ string, number int, opts github.UpdateIssueOptions) (github.Issue, error) {
					patchCalls++
					if opts.State == nil || *opts.State != tt.wantTarget || opts.Title != nil || opts.Body != nil {
						t.Fatalf("update options=%+v", opts)
					}
					return github.Issue{Number: number, State: *opts.State, HTMLURL: "https://github.com/o/r/issues/9"}, nil
				}
			})
			if code := app.runIssue(t.Context(), []string{tt.command, "--repo", "o/r", "--issue", "9", "--json"}); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, errOut.String())
			}
			var got issueStateResult
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if getCalls != 1 || patchCalls != tt.wantPatches || got.State != tt.wantTarget || got.Changed != tt.wantChanged {
				t.Fatalf("get=%d patch=%d result=%+v", getCalls, patchCalls, got)
			}
		})
	}
}

func TestPreserveIssueBodyMetadataProtectsExactDirectLineage(t *testing.T) {
	marker := func(kind string) string { return "<!-- issue-spec:issue=" + kind + " change=x version=1 -->" }
	tests := []struct {
		name        string
		existing    string
		replacement string
		wantMarker  string
		wantLink    string
		wantErr     bool
	}{
		{name: "proposal restores marker", existing: marker("proposal") + "\n# Old", replacement: "# New\n", wantMarker: marker("proposal")},
		{name: "design restores direct predecessor", existing: marker("design") + "\n# Old\n\n- Proposal Issue: 21\n", replacement: "# New\n\nDetails.\n", wantMarker: marker("design"), wantLink: "- Proposal Issue: 21"},
		{name: "implement deduplicates exact metadata", existing: marker("implement") + "\n# Old\n\n- Design Issue: https://github.com/o/r/issues/31\n", replacement: marker("implement") + "\n" + marker("implement") + "\n# New\n- design issue: https://github.com/o/r/issues/31\n- Design Issue: https://github.com/o/r/issues/31\n", wantMarker: marker("implement"), wantLink: "- Design Issue: https://github.com/o/r/issues/31"},
		{name: "marker change conflict", existing: marker("proposal") + "\n# Old", replacement: "<!-- issue-spec:issue=proposal change=other version=1 -->\n# New", wantErr: true},
		{name: "marker class conflict", existing: marker("proposal") + "\n# Old", replacement: "<!-- issue-spec:issue=design change=x version=1 -->\n# New", wantErr: true},
		{name: "marker version conflict", existing: marker("proposal") + "\n# Old", replacement: "<!-- issue-spec:issue=proposal change=x version=2 -->\n# New", wantErr: true},
		{name: "reference representation conflict", existing: marker("design") + "\n- Proposal Issue: 21\n", replacement: "# New\n- Proposal Issue: https://github.com/o/r/issues/21\n", wantErr: true},
		{name: "multiple stored markers", existing: marker("proposal") + "\n" + marker("proposal"), replacement: "# New", wantErr: true},
		{name: "missing stored predecessor", existing: marker("design") + "\n# Old\n", replacement: "# New", wantErr: true},
		{name: "multiple stored predecessors", existing: marker("design") + "\n- Proposal Issue: 21\n- Proposal Issue: 21\n", replacement: "# New", wantErr: true},
		{name: "ordinary issue remains unprotected", existing: "# Old\n- Proposal Issue: 21\n", replacement: "\n# New\n", wantMarker: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := preserveIssueBodyMetadata(tt.existing, tt.replacement)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v body=%q", err, got)
			}
			if tt.wantErr {
				return
			}
			if tt.wantMarker != "" && strings.Count(got, tt.wantMarker) != 1 {
				t.Fatalf("marker count in body:\n%s", got)
			}
			if tt.wantMarker == "" && hasIssueBodyMarker(got) {
				t.Fatalf("ordinary issue gained a marker:\n%s", got)
			}
			if tt.wantLink != "" && strings.Count(got, tt.wantLink) != 1 {
				t.Fatalf("predecessor count in body:\n%s", got)
			}
			if tt.wantMarker != "" {
				repeated, repeatErr := preserveIssueBodyMetadata(got, got)
				if repeatErr != nil || repeated != got {
					t.Fatalf("repeated update changed metadata: err=%v\nfirst=%s\nsecond=%s", repeatErr, got, repeated)
				}
			}
		})
	}
}

func TestIssueUpdateMetadataConflictFailsBeforeMutation(t *testing.T) {
	bodyPath := filepath.Join(t.TempDir(), "design.md")
	if err := os.WriteFile(bodyPath, []byte("# Design\n\n- Proposal Issue: 22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	patches := 0
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.getIssue = func(context.Context, string, int) (github.Issue, error) {
			return github.Issue{Number: 9, Body: "<!-- issue-spec:issue=design change=x version=1 -->\n# Design\n\n- Proposal Issue: 21\n"}, nil
		}
		f.updateIssue = func(context.Context, string, int, github.UpdateIssueOptions) (github.Issue, error) {
			patches++
			return github.Issue{}, nil
		}
	})
	code := app.runIssue(t.Context(), []string{"update", "--repo", "o/r", "--issue", "9", "--body-file", bodyPath})
	if code != 2 || patches != 0 || !strings.Contains(errOut.String(), "conflicts with stored reference") {
		t.Fatalf("exit=%d patches=%d stdout=%q stderr=%q", code, patches, out.String(), errOut.String())
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
			if strings.Contains(body, templates.IssueSpecProjectURL) {
				t.Fatalf("body-file path should not inject issue-spec footer:\n%s", body)
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

func TestIssueCreateDefaultBodyIncludesIssueSpecFooter(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.createIssue = func(_ context.Context, _ string, _ string, body string, _ []string) (github.Issue, error) {
			if !strings.Contains(body, templates.IssueBodyManagedByQuote) {
				t.Fatalf("default body missing issue-spec footer:\n%s", body)
			}
			return github.Issue{Number: 22, HTMLURL: "https://github.com/o/r/issues/22"}, nil
		}
	})

	code := app.runIssueCreate(context.Background(), "proposal", []string{"--repo", "o/r", "--change", "issue-spec-footer", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
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
			if strings.Count(body, "- Proposal Issue: 21") != 1 {
				t.Fatalf("custom design body predecessor link = %q:\n%s", "- Proposal Issue: 21", body)
			}
			return github.Issue{Number: 103, HTMLURL: "https://github.com/o/r/issues/103", Title: title}, nil
		}
	})

	code := app.runIssueCreate(context.Background(), "design", []string{"--repo", "o/r", "--change", "issue-title-style", "--proposal", "21", "--body-file", bodyPath, "--title", "Custom design title", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
}

func TestIssueCreateCustomBodiesNormalizeAuthoritativePredecessorLinks(t *testing.T) {
	t.Run("design", func(t *testing.T) {
		bodyPath := filepath.Join(t.TempDir(), "design.md")
		body := "# Design: custom body\n\n- proposal Issue: 999\n\nDetails.\n\n- Proposal Issue: https://issues.invalid/old\n"
		if err := os.WriteFile(bodyPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.selectGitHubBackend = ghSelection
		app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
			f.listIssueComments = func(_ context.Context, _ string, issueNumber int) ([]github.Comment, error) {
				if issueNumber != 21 {
					t.Fatalf("proposal issue = %d", issueNumber)
				}
				return []github.Comment{{ID: 1, Body: mustTypedBody(t, "SPEC", "SPEC-001", "confirmed", "gate",
					"## Requirement: gate\n\nThe design MUST pass.\n\n### Scenario: pass\n\n- **WHEN** created\n- **THEN** pass\n")}}, nil
			}
			f.createIssue = func(_ context.Context, _ string, _ string, createdBody string, _ []string) (github.Issue, error) {
				if strings.Count(createdBody, "- Proposal Issue: 21") != 1 || strings.Contains(createdBody, "999") ||
					strings.Contains(createdBody, "issues.invalid") {
					t.Fatalf("design predecessor was not normalized:\n%s", createdBody)
				}
				if !strings.HasPrefix(createdBody, "<!-- issue-spec:issue=design change=predecessor-links version=1 -->\n") ||
					strings.Contains(createdBody, templates.IssueSpecProjectURL) {
					t.Fatalf("design marker/footer behavior regressed:\n%s", createdBody)
				}
				return github.Issue{Number: 22, HTMLURL: "https://github.com/o/r/issues/22"}, nil
			}
		})
		if code := app.runIssueCreate(t.Context(), "design", []string{"--repo", "o/r", "--change", "predecessor-links",
			"--proposal", "21", "--body-file", bodyPath, "--json"}); code != 0 {
			t.Fatalf("design create exit=%d stderr=%q", code, errOut.String())
		}
	})

	t.Run("implement", func(t *testing.T) {
		bodyPath := filepath.Join(t.TempDir(), "implement.md")
		body := "# Implement: custom body\n\n- Design Issue: 77\n\nExecution plan.\n\n- design issue: https://issues.invalid/old\n"
		if err := os.WriteFile(bodyPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		taskBody, err := model.EnsureTypedBody("TASK", "TASK-001", "## Task: gate\n\n### Implementation Checklist\n\n- [ ] pass\n\n### Execution Planning\n\n- Coupling class: low\n\n### Covers\n\n- SPEC-001\n",
			model.BodyOptions{Status: "confirmed", Scope: "gate", Links: map[string][]string{
				"Related Comments": {"https://github.com/o/r/issues/21#issuecomment-1"},
			}})
		if err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.selectGitHubBackend = ghSelection
		app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
			f.listIssueComments = func(_ context.Context, _ string, issueNumber int) ([]github.Comment, error) {
				if issueNumber != 31 {
					t.Fatalf("design issue = %d", issueNumber)
				}
				return []github.Comment{{ID: 2, Body: taskBody}}, nil
			}
			f.createIssue = func(_ context.Context, _ string, _ string, createdBody string, _ []string) (github.Issue, error) {
				if strings.Count(createdBody, "- Design Issue: 31") != 1 || strings.Contains(createdBody, "77") ||
					strings.Contains(createdBody, "issues.invalid") {
					t.Fatalf("implement predecessor was not normalized:\n%s", createdBody)
				}
				if !strings.HasPrefix(createdBody, "<!-- issue-spec:issue=implement change=predecessor-links version=1 -->\n") ||
					strings.Contains(createdBody, templates.IssueSpecProjectURL) {
					t.Fatalf("implement marker/footer behavior regressed:\n%s", createdBody)
				}
				return github.Issue{Number: 32, HTMLURL: "https://github.com/o/r/issues/32"}, nil
			}
		})
		if code := app.runIssueCreate(t.Context(), "implement", []string{"--repo", "o/r", "--change", "predecessor-links",
			"--design", "31", "--body-file", bodyPath, "--json"}); code != 0 {
			t.Fatalf("implement create exit=%d stderr=%q", code, errOut.String())
		}
	})
}

func TestIssuePredecessorLinkPreservesProposalAndDefaultTemplates(t *testing.T) {
	proposal := "<!-- issue-spec:issue=proposal change=x version=1 -->\n# Proposal: x\n\nBody.\n"
	if got, err := ensureIssuePredecessorLink("proposal", "", proposal); err != nil || got != proposal {
		t.Fatalf("proposal changed got=%q err=%v", got, err)
	}
	_, design, _ := templates.DesignIssue("x", "21")
	if got, err := ensureIssuePredecessorLink("design", "21", design); err != nil || got != design {
		t.Fatalf("default design changed err=%v\n%s", err, got)
	}
	_, implement, _ := templates.ImplementIssue("x", "31")
	if got, err := ensureIssuePredecessorLink("implement", "31", implement); err != nil || got != implement {
		t.Fatalf("default implement changed err=%v\n%s", err, got)
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
