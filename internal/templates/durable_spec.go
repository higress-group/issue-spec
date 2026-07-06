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
	order, byKey := parseExistingRequirements(opts.ExistingSpecBody)
	for _, spec := range opts.SpecificationList {
		content, err := durableRequirementContent(spec)
		if err != nil {
			return "", err
		}
		key := requirementKey(content, len(order))
		if _, exists := byKey[key]; !exists {
			order = append(order, key)
		}
		byKey[key] = content
	}
	requirements := make([]string, 0, len(order))
	for _, key := range order {
		requirements = append(requirements, byKey[key])
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

var (
	requirementsHeadingRe = regexp.MustCompile(`(?m)^## Requirements[ \t]*$`)
	requirementTitleRe    = regexp.MustCompile(`(?m)^###\s+Requirement:\s*(.*?)\s*$`)
)

// parseExistingRequirements extracts the requirement blocks already present in a
// durable spec body. It returns the block keys in document order plus a map from
// key to the full block text (including the "### Requirement:" heading and any
// "Source SPEC comment:" trailer). Blocks are keyed by requirement title so a
// re-archive can replace a prior requirement in place; empty-title blocks get a
// unique key so two malformed requirements never collapse into one.
func parseExistingRequirements(body string) ([]string, map[string]string) {
	order := []string{}
	byKey := map[string]string{}
	section := requirementsSection(body)
	if section == "" {
		return order, byKey
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
		key := requirementKey(block, len(order))
		if _, exists := byKey[key]; !exists {
			order = append(order, key)
		}
		byKey[key] = block
	}
	return order, byKey
}

// requirementsSection returns everything under the "## Requirements" heading to
// the end of the body. The heading is matched as a whole line (not a substring),
// and the section deliberately runs to end-of-file rather than stopping at the
// next "## " so that a level-2-looking line inside a requirement body cannot
// truncate the section and silently drop later requirements. Requirement blocks
// are delimited by "### Requirement:" headings, so any stray "## " text stays
// inside the block it belongs to.
func requirementsSection(body string) string {
	loc := requirementsHeadingRe.FindStringIndex(body)
	if loc == nil {
		return ""
	}
	return strings.TrimSpace(body[loc[1]:])
}

// requirementKey returns the dedup key for a requirement block: its title when
// present, or a unique sentinel derived from uniqueSeed when the title is empty.
// Empty-title requirements are malformed but pass canonical validation, so they
// must be preserved individually instead of colliding on the empty string.
func requirementKey(block string, uniqueSeed int) string {
	if title := requirementTitle(block); title != "" {
		return title
	}
	return fmt.Sprintf("\x00empty-%d", uniqueSeed)
}

// requirementTitle extracts the text following "### Requirement:" from a block,
// used as the dedup key when merging existing and new requirements.
func requirementTitle(block string) string {
	if m := requirementTitleRe.FindStringSubmatch(block); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
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
	for _, match := range proposalIssueLineRe.FindAllString(proposalIssuesBlock(existingBody), -1) {
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

// proposalIssuesBlock returns just the "Proposal Issues:" bullet list, bounded
// by the next "## " heading. Scoping the scan here prevents issue-URL bullets
// that legitimately appear inside requirement bodies from being harvested as
// proposal issues on re-archive.
func proposalIssuesBlock(body string) string {
	idx := strings.Index(body, "Proposal Issues:")
	if idx < 0 {
		return ""
	}
	rest := body[idx:]
	if next := strings.Index(rest, "\n## "); next >= 0 {
		rest = rest[:next]
	}
	return rest
}
