package templates

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
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

	// Umbrella accumulation: preserve requirements already archived into this
	// capability, then merge in the current proposal's SPECs. A new requirement
	// that shares a title with an existing one replaces it in place (newest
	// wins); genuinely new requirements append after the preserved ones.
	order, byTitle := parseExistingRequirements(opts.ExistingSpecBody)
	for _, spec := range opts.SpecificationList {
		content, err := durableRequirementContent(spec)
		if err != nil {
			return "", err
		}
		title := requirementTitle(content)
		if _, exists := byTitle[title]; !exists {
			order = append(order, title)
		}
		byTitle[title] = content
	}
	requirements := make([]string, 0, len(order))
	for _, title := range order {
		requirements = append(requirements, byTitle[title])
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

var requirementHeadingRe = regexp.MustCompile(`(?m)^###\s+Requirement:`)

// parseExistingRequirements extracts the requirement blocks already present in a
// durable spec body. It returns the block titles in document order plus a map
// from title to the full block text (including the "### Requirement:" heading
// and any "Source SPEC comment:" trailer). Blocks are keyed by requirement
// title so a re-archive can replace a prior requirement in place.
func parseExistingRequirements(body string) ([]string, map[string]string) {
	order := []string{}
	byTitle := map[string]string{}
	section := requirementsSection(body)
	if section == "" {
		return order, byTitle
	}
	locs := requirementHeadingRe.FindAllStringIndex(section, -1)
	for i, loc := range locs {
		end := len(section)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := strings.TrimSpace(section[loc[0]:end])
		if block == "" {
			continue
		}
		title := requirementTitle(block)
		if _, exists := byTitle[title]; !exists {
			order = append(order, title)
		}
		byTitle[title] = block
	}
	return order, byTitle
}

// requirementsSection returns the text under the "## Requirements" heading up to
// the next level-2 heading (or end of body).
func requirementsSection(body string) string {
	idx := strings.Index(body, "## Requirements")
	if idx < 0 {
		return ""
	}
	rest := body[idx+len("## Requirements"):]
	if next := strings.Index(rest, "\n## "); next >= 0 {
		rest = rest[:next]
	}
	return strings.TrimSpace(rest)
}

// requirementTitle extracts the text following "### Requirement:" from a block,
// used as the dedup key when merging existing and new requirements.
func requirementTitle(block string) string {
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### Requirement:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "### Requirement:"))
		}
	}
	return strings.TrimSpace(block)
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

// validateSpecDiscipline reuses the single shared canonical SPEC validator in
// the model layer so durable archive rendering enforces the same rules as
// comment upsert, list, status, and verify.
func validateSpecDiscipline(id, body string) error {
	if errs := model.SpecBodyErrors(body); len(errs) > 0 {
		return fmt.Errorf("%s %s", id, strings.Join(errs, "; "))
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
