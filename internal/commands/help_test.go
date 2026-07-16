package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestLeafCommandHelpShowsOptionsAndDefaults(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)

	code := app.runInit(context.Background(), []string{"-h"})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"Usage:",
		"issue-spec init [options]",
		"--repo string",
		"repository owner/name (default: \"\")",
		"--delivery string",
		"workflow artifact delivery: both, skills, or commands (default: both)",
		"--create-labels",
		"ensure issue-spec labels (default: true)",
		"--skip-labels",
		"skip ensuring issue-spec labels (default: false)",
		"--language string",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("init help missing %q:\n%s", want, text)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("init help wrote stderr: %q", errOut.String())
	}
}

func TestIssueListAndStateHelpShowsOptions(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "list", args: []string{"list", "-h"}, want: []string{"issue-spec issue list [options]", "--state string", "issue state: open, closed, or all (default: open)", "--json"}},
		{name: "close", args: []string{"close", "-h"}, want: []string{"issue-spec issue close [options]", "--issue string", "--json"}},
		{name: "reopen", args: []string{"reopen", "-h"}, want: []string{"issue-spec issue reopen [options]", "--issue string", "--json"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			if code := app.runIssue(t.Context(), tt.args); code != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("help missing %q:\n%s", want, out.String())
				}
			}
			if errOut.Len() != 0 {
				t.Fatalf("help wrote stderr: %q", errOut.String())
			}
		})
	}
}

func TestCommentListHelpShowsIncludeBodyRequirement(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)

	if code := app.runComment(t.Context(), []string{"list", "-h"}); code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, want := range []string{
		"issue-spec comment list [options]",
		"--include-body",
		"include original backend Markdown in JSON output (requires --json)",
		"--json",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, out.String())
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("help wrote stderr: %q", errOut.String())
	}
}
