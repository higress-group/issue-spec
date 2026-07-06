package templates

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	IssueSpecProjectURL      = "https://github.com/higress-group/issue-spec"
	AgentReplyPoweredByQuote = "> This agent reply is powered by [issue-spec](" + IssueSpecProjectURL + "), an issue-native workflow for specs, tasks, reviews, and resumable agent sessions."
	IssueBodyManagedByQuote  = "> This issue is managed by [issue-spec](" + IssueSpecProjectURL + "), an issue-native workflow for specs, tasks, reviews, and resumable agent sessions."
)

// PlaceholderSentinel marks a template section whose author-facing prompt has
// not yet been replaced with self-contained content. Authoring-completeness
// diagnostics key on this token; an author clears a section by removing it and
// writing environment-independent content in its place.
const PlaceholderSentinel = "<!-- issue-spec:fill -->"

func fillSection(title, prompt string) string {
	return fmt.Sprintf("## %s\n\n%s %s\n\n", title, PlaceholderSentinel, prompt)
}

func plainSection(title, content string) string {
	return fmt.Sprintf("## %s\n\n%s\n\n", title, content)
}

func ProposalIssue(change string) (string, string, []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- issue-spec:issue=proposal change=%s version=1 -->\n", change)
	fmt.Fprintf(&b, "# Proposal: %s\n\n", change)
	b.WriteString(plainSection("Metadata", fmt.Sprintf("- Change Name: %s\n- External Issue ID: N/A", change)))
	b.WriteString(fillSection("Background", "Write the environment-independent background a reader with no shared session context needs: the problem being solved, the domain facts and constraints you discovered, and why this change matters now. Do not rely on local paths or session state."))
	b.WriteString(fillSection("Goals", "List the concrete, verifiable outcomes this change must achieve."))
	b.WriteString(fillSection("Scope", "State what is in scope, precisely enough that another agent can tell whether a given change belongs here."))
	b.WriteString(fillSection("Non-Goals", "List what is explicitly out of scope to prevent scope creep."))
	b.WriteString(plainSection("Key Constraints", "- Active change artifacts live in this GitHub proposal/design/implement issue set."))
	b.WriteString(fillSection("Related Specs Analysis", "Summarize the existing SPECs or durable specs this interacts with and how, so a reader need not rediscover them."))
	b.WriteString(fillSection("Existing Assumptions Impact", "Record the assumptions in force and how this change affects or depends on them."))
	b.WriteString(plainSection("Open Questions", "- No blocking QUESTION is currently recorded."))
	b.WriteString(fillSection("Capabilities", "List the capability areas this change introduces or modifies."))
	b.WriteString(fillSection("Impact", "Describe the expected impact on users, agents, and the workflow."))
	body := strings.TrimRight(b.String(), "\n") + "\n"
	title := IssueTitle("proposal", change, body, "")
	return title, body, []string{"issue-spec/proposal"}
}

func DesignIssue(change, proposalRef string) (string, string, []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- issue-spec:issue=design change=%s version=1 -->\n", change)
	fmt.Fprintf(&b, "# Design: %s\n\n", change)
	b.WriteString(plainSection("Question Convergence Check", fmt.Sprintf("- Proposal Issue: %s\n- Blocking QUESTION status: confirmed or explicitly accepted as assumptions.", valueOr(proposalRef, "N/A"))))
	b.WriteString(fillSection("Current Implementation Locations", "Name the concrete files/symbols that implement the affected behavior today, so a reader with no shared context can find them without rediscovery."))
	b.WriteString(fillSection("Involved Modules", "List the modules this change touches and their role."))
	b.WriteString(fillSection("Impact Scope", "Describe what behavior changes and which SPECs each part realizes."))
	b.WriteString(fillSection("Unaffected Modules", "Call out closely related modules that are deliberately left unchanged."))
	b.WriteString(fillSection("Search Entry Points / Key Files", "List the file:symbol entry points a fresh reader should start from."))
	b.WriteString(fillSection("Risk Hotspots", "Identify the risky areas and how the design contains them."))
	b.WriteString(fillSection("Candidate Plans", "Present the candidate plans considered, with enough detail to compare them."))
	b.WriteString(fillSection("Decisions", "State the decisions made and the environment-independent rationale behind each."))
	b.WriteString(fillSection("Rejected Alternatives", "Record the alternatives rejected and why, so they are not re-litigated."))
	b.WriteString(fillSection("Test Strategy and Acceptance Criteria", "Describe how each SPEC is verified and what acceptance looks like."))
	b.WriteString(fillSection("Rollout / Rollback Notes", "Note rollout and rollback considerations."))
	b.WriteString(plainSection("Confirmation Checklist", "- [ ] SPEC comments are linked and testable.\n- [ ] Blocking QUESTION comments are resolved or accepted as assumptions."))
	body := strings.TrimRight(b.String(), "\n") + "\n"
	title := IssueTitle("design", change, body, "")
	return title, body, []string{"issue-spec/design"}
}

func ImplementIssue(change, designRef string) (string, string, []string) {
	body := fmt.Sprintf(`<!-- issue-spec:issue=implement change=%s version=1 -->
# Implement DAG: %s

## PR Mode Decision

TBD

## DAG Nodes and Dependencies

TBD

## Worktree / Branch Plan

TBD

## PR-owner and Review-agent Assignment

TBD

## Conflict Risk and Serialization Plan

TBD

## Global Review / Verify Status

- Design Issue: %s
- Status: draft
`, change, change, valueOr(designRef, "N/A"))
	title := IssueTitle("implement", change, body, "")
	return title, body, []string{"issue-spec/implement"}
}

func AppendIssueSpecIssueFooter(body string) string {
	body = strings.TrimRight(body, "\n")
	if strings.Contains(body, IssueSpecProjectURL) {
		return body + "\n"
	}
	return body + "\n\n" + IssueBodyManagedByQuote + "\n"
}

func IssueTitle(kind, change, body, explicitTitle string) string {
	if title := strings.TrimSpace(explicitTitle); title != "" {
		return title
	}
	prefix := issueTitlePrefix(kind)
	subject := issueTitleSubject(body)
	if subject == "" {
		subject = strings.TrimSpace(change)
	}
	if subject == "" {
		subject = "N/A"
	}
	return prefix + ": " + subject
}

func issueTitlePrefix(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "proposal":
		return "Proposal"
	case "design":
		return "Design"
	case "implement":
		return "Implement"
	default:
		return "Issue"
	}
}

var markdownH1Re = regexp.MustCompile(`^#\s+(.+?)\s*$`)

func issueTitleSubject(body string) string {
	for _, line := range strings.Split(body, "\n") {
		match := markdownH1Re.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		return stripIssueTitlePrefix(match[1])
	}
	return ""
}

func stripIssueTitlePrefix(subject string) string {
	subject = strings.TrimSpace(subject)
	for _, prefix := range []string{
		"Proposal:",
		"Design:",
		"Implement:",
		"Implementation:",
		"Implement DAG:",
	} {
		if strings.HasPrefix(strings.ToLower(subject), strings.ToLower(prefix)) {
			return strings.TrimSpace(subject[len(prefix):])
		}
	}
	return subject
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
