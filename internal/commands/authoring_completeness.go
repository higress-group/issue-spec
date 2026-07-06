package commands

import (
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/templates"
)

// authoringRequiredSections lists the self-contained-authoring sections that a
// cross-agent reader needs populated, keyed by issue kind. These are advisory
// only: a placeholder here never blocks status or verify.
var authoringRequiredSections = map[string][]string{
	"proposal": {
		"Background",
		"Goals",
		"Scope",
		"Related Specs Analysis",
		"Existing Assumptions Impact",
	},
	"design": {
		"Current Implementation Locations",
		"Impact Scope",
		"Candidate Plans",
		"Decisions",
	},
}

// authoringCompletenessDiagnostics returns advisory diagnostics for required
// sections of a proposal/design body that are still empty, carry the
// issue-spec:fill sentinel, or are left as a bare TBD placeholder. It is pure
// over its inputs and never produces blocking gates or errors.
func authoringCompletenessDiagnostics(kind, url, body string) []metadataDiagnostic {
	sections, ok := authoringRequiredSections[kind]
	if !ok {
		return nil
	}
	var diags []metadataDiagnostic
	for _, section := range sections {
		content := rawSectionContent(body, "## "+section)
		if !isPlaceholderContent(content) {
			continue
		}
		diags = append(diags, metadataDiagnostic{
			Level:    "info",
			Code:     "authoring_incomplete",
			Artifact: fmt.Sprintf("%s section %q", kind, section),
			URL:      url,
			Message:  "self-contained authoring: section is empty or still a placeholder; write content a reader with no shared session context can use",
		})
	}
	return diags
}

// rawSectionContent returns the trimmed text of the named `##` section from the
// raw issue body, up to the next `##`/`###` heading. Unlike sectionContent it
// does not run model.LogicalBody, so the issue-spec:fill sentinel (an
// issue-spec HTML marker that LogicalBody would strip) survives for detection.
func rawSectionContent(body, heading string) string {
	lines := strings.Split(body, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return ""
	}
	var out []string
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
			break
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// isPlaceholderContent reports whether a section body is effectively unwritten:
// empty/N/A, still carrying the issue-spec:fill sentinel, or a bare TBD.
func isPlaceholderContent(content string) bool {
	if isEmptyOrNA(content) {
		return true
	}
	if strings.Contains(content, templates.PlaceholderSentinel) {
		return true
	}
	stripped := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "-"))
	return strings.EqualFold(stripped, "TBD")
}
