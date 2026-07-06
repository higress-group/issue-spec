package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

func TestAuthoringCompletenessDiagnosticsFlagsPlaceholders(t *testing.T) {
	sentinelBackground := "## Background\n\n" + templates.PlaceholderSentinel + " write it\n\n"
	cases := []struct {
		name     string
		kind     string
		body     string
		wantFlag bool
	}{
		{
			name:     "empty section",
			kind:     "proposal",
			body:     "## Background\n\n## Goals\n\ndone\n",
			wantFlag: true,
		},
		{
			name:     "sentinel section",
			kind:     "proposal",
			body:     sentinelBackground,
			wantFlag: true,
		},
		{
			name:     "bare TBD section",
			kind:     "proposal",
			body:     "## Background\n\n- TBD\n",
			wantFlag: true,
		},
		{
			name:     "populated section",
			kind:     "proposal",
			body:     "## Background\n\nReal environment-independent context that a fresh agent can use.\n",
			wantFlag: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := authoringCompletenessDiagnostics(tc.kind, "https://example/1", tc.body)
			flagged := false
			for _, d := range diags {
				if d.Level != "info" || d.Code != "authoring_incomplete" {
					t.Fatalf("unexpected diagnostic shape: %+v", d)
				}
				if strings.Contains(d.Artifact, "Background") {
					flagged = true
				}
			}
			if flagged != tc.wantFlag {
				t.Fatalf("Background flagged=%v want %v (diags=%+v)", flagged, tc.wantFlag, diags)
			}
		})
	}
}

func TestAuthoringCompletenessDiagnosticsUnknownKind(t *testing.T) {
	if diags := authoringCompletenessDiagnostics("implement", "https://example/1", "## Background\n\n"); diags != nil {
		t.Fatalf("unknown kind should yield no diagnostics: %+v", diags)
	}
}

func TestRunStatusReportsAuthoringDiagnosticsWithoutBlocking(t *testing.T) {
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y", model.BodyOptions{Status: "confirmed", Scope: "s", AgentSessionID: "sess", AgentSessionSource: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_, proposalBody, _ := templates.ProposalIssue("demo-change")

	var out bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.getIssue = func(_ context.Context, _ string, issue int) (github.Issue, error) {
			return github.Issue{Number: issue, HTMLURL: "https://github.com/o/r/issues/1", Body: proposalBody}, nil
		}
		f.listIssueComments = func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
			return []github.Comment{{ID: 1, HTMLURL: "https://github.com/o/r/issues/1#issuecomment-1", Body: specBody}}, nil
		}
	})
	code := app.runStatus(context.Background(), []string{"--repo", "o/r", "--proposal", "1", "--json"})
	if code != 0 {
		t.Fatalf("status exit=%d out=%q", code, out.String())
	}
	var summary struct {
		OK          bool `json:"ok"`
		Diagnostics []struct {
			Level string `json:"level"`
			Code  string `json:"code"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("decode status json: %v\n%s", err, out.String())
	}
	if !summary.OK {
		t.Fatalf("authoring diagnostics must not block status: %s", out.String())
	}
	authoring := 0
	for _, d := range summary.Diagnostics {
		if d.Code == "authoring_incomplete" {
			if d.Level != "info" {
				t.Fatalf("authoring diagnostic must be advisory info, got %q", d.Level)
			}
			authoring++
		}
	}
	if authoring == 0 {
		t.Fatalf("expected authoring_incomplete diagnostics for placeholder proposal: %s", out.String())
	}
}

func TestRunVerifyReportsAuthoringDiagnosticsWithoutBlocking(t *testing.T) {
	const (
		specURL    = "https://github.com/o/r/issues/1#issuecomment-1"
		taskURL    = "https://github.com/o/r/issues/2#issuecomment-2"
		processURL = "https://github.com/o/r/issues/3#issuecomment-3"
		verifyURL  = "https://github.com/o/r/issues/3#issuecomment-4"
	)
	spec := typedCommentWithLinks(t, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y", 1, specURL, taskURL)
	task := typedCommentWithLinks(t, "TASK", "TASK-001", "done", canonicalTaskContent, 2, taskURL, specURL, processURL)
	process := typedCommentWithLinks(t, "PROCESS", "PROCESS-001", "done", canonicalProcessContent, 3, processURL, taskURL)
	verify := typedCommentWithLinks(t, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent, 4, verifyURL)
	_, proposalBody, _ := templates.ProposalIssue("demo-change")

	var out bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.getIssue = func(_ context.Context, _ string, issue int) (github.Issue, error) {
			return github.Issue{Number: issue, HTMLURL: "https://github.com/o/r/issues/1", Body: proposalBody}, nil
		}
		f.listIssueComments = func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
			switch issue {
			case 1:
				return []github.Comment{spec}, nil
			case 2:
				return []github.Comment{task}, nil
			case 3:
				return []github.Comment{process, verify}, nil
			}
			return nil, nil
		}
	})
	code := app.runVerify(context.Background(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--json"})
	if code != 0 {
		t.Fatalf("verify exit=%d out=%q", code, out.String())
	}
	var report struct {
		OK          bool     `json:"ok"`
		Errors      []string `json:"errors"`
		Diagnostics []struct {
			Level string `json:"level"`
			Code  string `json:"code"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode verify json: %v\n%s", err, out.String())
	}
	if !report.OK {
		t.Fatalf("authoring diagnostics must not block verify: errors=%v\n%s", report.Errors, out.String())
	}
	authoring := 0
	for _, d := range report.Diagnostics {
		if d.Code == "authoring_incomplete" {
			if d.Level != "info" {
				t.Fatalf("authoring diagnostic must be advisory info, got %q", d.Level)
			}
			authoring++
		}
	}
	if authoring == 0 {
		t.Fatalf("expected authoring_incomplete diagnostics for placeholder proposal: %s", out.String())
	}
}

func typedCommentWithLinks(t *testing.T, typ, id, status, content string, commentID int64, htmlURL string, related ...string) github.Comment {
	t.Helper()
	body, err := model.EnsureTypedBody(typ, id, content, model.BodyOptions{Status: status})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range related {
		body, _, err = model.AddRelatedCommentLink(body, u)
		if err != nil {
			t.Fatal(err)
		}
	}
	return github.Comment{ID: commentID, HTMLURL: htmlURL, Body: body}
}
