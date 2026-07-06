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
		"create issue-spec labels (default: false)",
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
