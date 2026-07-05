package templates

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type SpecSource struct {
	ID   string
	URL  string
	Body string
}

type DurableSpecOptions struct {
	Capability        string
	Purpose           string
	ProposalIssueURL  string
	ExistingSpecBody  string
	SpecificationList []SpecSource
}

var proposalIssueLineRe = regexp.MustCompile(`(?m)^-\s+https?://\S+/issues/\d+\s*$`)

func DurableSpec(opts DurableSpecOptions) (string, error) {
	capability := strings.TrimSpace(opts.Capability)
	if capability == "" {
		return "", fmt.Errorf("capability is required")
	}
	if len(opts.SpecificationList) == 0 {
		return "", fmt.Errorf("at least one SPEC source is required")
	}
	purpose := strings.TrimSpace(opts.Purpose)
	if purpose == "" {
		purpose = "Define the long-lived behavior contract for this capability."
	}
	proposals := collectProposalIssueURLs(opts.ExistingSpecBody, opts.ProposalIssueURL)
	var requirements []string
	for _, spec := range opts.SpecificationList {
		content, err := durableRequirementContent(spec)
		if err != nil {
			return "", err
		}
		requirements = append(requirements, content)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", capability)
	b.WriteString("## Purpose\n\n")
	b.WriteString(purpose)
	b.WriteString("\n\n")
	b.WriteString("Proposal Issues:\n")
	for _, proposal := range proposals {
		fmt.Fprintf(&b, "- %s\n", proposal)
	}
	b.WriteString("\n## Requirements\n\n")
	b.WriteString(strings.Join(requirements, "\n\n"))
	b.WriteString("\n")
	return b.String(), nil
}

func durableRequirementContent(spec SpecSource) (string, error) {
	body := stripTypedHeader(spec.Body)
	if err := validateSpecDiscipline(spec.ID, body); err != nil {
		return "", err
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "## Requirement:"):
			lines[i] = strings.Replace(line, "## Requirement:", "### Requirement:", 1)
		case strings.HasPrefix(trimmed, "### Scenario:"):
			lines[i] = strings.Replace(line, "### Scenario:", "#### Scenario:", 1)
		case strings.HasPrefix(trimmed, "## ADDED Requirements"),
			strings.HasPrefix(trimmed, "## MODIFIED Requirements"),
			strings.HasPrefix(trimmed, "## REMOVED Requirements"),
			strings.HasPrefix(trimmed, "## RENAMED Requirements"):
			lines[i] = ""
		}
	}
	content := strings.TrimSpace(strings.Join(lines, "\n"))
	if spec.URL != "" {
		content += "\n\nSource SPEC comment: " + spec.URL
	}
	return content, nil
}

func stripTypedHeader(body string) string {
	body = strings.TrimSpace(body)
	idx := strings.Index(body, "\n## Requirement:")
	if idx >= 0 {
		return strings.TrimSpace(body[idx+1:])
	}
	if strings.HasPrefix(body, "## Requirement:") {
		return body
	}
	return body
}

func validateSpecDiscipline(id, body string) error {
	body = strings.TrimSpace(body)
	if !strings.Contains(body, "## Requirement:") {
		return fmt.Errorf("%s is missing Requirement heading", id)
	}
	if !strings.Contains(body, " MUST ") && !strings.Contains(body, " SHALL ") {
		return fmt.Errorf("%s must use MUST or SHALL", id)
	}
	if !strings.Contains(body, "Scenario:") {
		return fmt.Errorf("%s is missing Scenario", id)
	}
	if !strings.Contains(body, "**WHEN**") || !strings.Contains(body, "**THEN**") {
		return fmt.Errorf("%s scenarios must include WHEN and THEN", id)
	}
	return nil
}

func collectProposalIssueURLs(existingBody, current string) []string {
	seen := map[string]bool{}
	var out []string
	for _, match := range proposalIssueLineRe.FindAllString(existingBody, -1) {
		url := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(match), "-"))
		if url != "" && !seen[url] {
			seen[url] = true
			out = append(out, url)
		}
	}
	current = strings.TrimSpace(current)
	if current != "" && !seen[current] {
		seen[current] = true
		out = append(out, current)
	}
	sort.Strings(out)
	return out
}
