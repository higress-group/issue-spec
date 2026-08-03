package commands

import (
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/templates"
)

func TestAuthoringCompletenessDiagnosticsFlagsPlaceholders(t *testing.T) {
	cases := []struct {
		body     string
		wantFlag bool
	}{
		{body: "## Background\n\n## Goals\n\ndone\n", wantFlag: true},
		{body: "## Background\n\n" + templates.PlaceholderSentinel + " write it\n", wantFlag: true},
		{body: "## Background\n\nReal context.\n", wantFlag: false},
	}
	for _, test := range cases {
		flagged := false
		for _, diagnostic := range authoringCompletenessDiagnostics("proposal", "https://example/1", test.body) {
			if strings.Contains(diagnostic.Artifact, "Background") {
				flagged = true
			}
		}
		if flagged != test.wantFlag {
			t.Fatalf("body %q flagged=%v want=%v", test.body, flagged, test.wantFlag)
		}
	}
}

func TestAuthoringCompletenessDiagnosticsUnknownKind(t *testing.T) {
	if diagnostics := authoringCompletenessDiagnostics("implement", "https://example/1", "## Background\n\n"); diagnostics != nil {
		t.Fatalf("unknown kind should yield no diagnostics: %+v", diagnostics)
	}
}
