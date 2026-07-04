package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

func (a *app) runIssue(ctx context.Context, args []string) int {
	if len(args) < 1 {
		a.errorf("usage: issue-spec issue create proposal|design|implement --repo owner/repo --change name [--body-file file.md]\n")
		a.errorf("       issue-spec issue update --repo owner/repo --issue N [--title title] [--body-file file.md] [--summary \"what changed\"]\n")
		return 2
	}
	switch args[0] {
	case "create":
		if len(args) < 2 {
			a.errorf("issue class is required: proposal, design, or implement\n")
			return 2
		}
		return a.runIssueCreate(ctx, args[1], args[2:])
	case "update":
		return a.runIssueUpdate(ctx, args[1:])
	default:
		a.errorf("unknown issue command %q\n", args[0])
		return 2
	}
}

func (a *app) runIssueCreate(ctx context.Context, kind string, args []string) int {
	fs := newFlagSet("issue create "+kind, a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	change := fs.String("change", "", "change name")
	proposal := fs.String("proposal", "", "proposal issue number or URL")
	design := fs.String("design", "", "design issue number or URL")
	bodyFile := fs.String("body-file", "", "markdown issue body file, or - for stdin")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if *change == "" {
		a.errorf("--change is required\n")
		return 2
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for issue create on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}

	var title, body string
	var labels []string
	switch kind {
	case "proposal":
		title, body, labels = templates.ProposalIssue(*change)
	case "design":
		if *proposal == "" {
			a.errorf("--proposal is required for design issues\n")
			return 2
		}
		proposalIssue, err := parseIssueFlag(*proposal, "proposal")
		if err != nil {
			a.errorf("%v\n", err)
			return 2
		}
		artifacts, err := collectArtifacts(ctx, client, repo, proposalIssue)
		if err != nil {
			a.errorf("read proposal issue comments: %v\n", err)
			return 1
		}
		if countType(artifacts, "SPEC") == 0 {
			a.errorf("design gate blocked: proposal issue %d has no SPEC comments\n", proposalIssue)
			return 1
		}
		if hasBlockedQuestion(artifacts) {
			a.errorf("design gate blocked: proposal issue %d has open blocking QUESTION comments\n", proposalIssue)
			return 1
		}
		title, body, labels = templates.DesignIssue(*change, *proposal)
	case "implement":
		if *design == "" {
			a.errorf("--design is required for implement issues\n")
			return 2
		}
		designIssue, err := parseIssueFlag(*design, "design")
		if err != nil {
			a.errorf("%v\n", err)
			return 2
		}
		artifacts, err := collectArtifacts(ctx, client, repo, designIssue)
		if err != nil {
			a.errorf("read design issue comments: %v\n", err)
			return 1
		}
		if countType(artifacts, "TASK") == 0 {
			a.errorf("implement gate blocked: design issue %d has no TASK comments\n", designIssue)
			return 1
		}
		if hasBlockedQuestion(artifacts) {
			a.errorf("implement gate blocked: design issue %d has open blocking QUESTION comments\n", designIssue)
			return 1
		}
		if *proposal != "" {
			proposalIssue, err := parseIssueFlag(*proposal, "proposal")
			if err != nil {
				a.errorf("%v\n", err)
				return 2
			}
			fullArtifacts, err := collectArtifacts(ctx, client, repo, proposalIssue, designIssue)
			if err != nil {
				a.errorf("read proposal/design issue comments: %v\n", err)
				return 1
			}
			report := model.VerifyTraceability(fullArtifacts)
			if !report.OK {
				a.errorf("implement gate blocked: proposal/design traceability errors:\n")
				for _, msg := range report.Errors {
					a.errorf("- %s\n", msg)
				}
				return 1
			}
		} else {
			for _, artifact := range artifacts {
				if artifact.Comment.Type == "TASK" && len(model.RelatedCommentURLs(artifact.Comment)) == 0 {
					a.errorf("implement gate blocked: %s has no Related Comments links; pass --proposal for full SPEC backlink verification\n", artifact.Comment.ID)
					return 1
				}
			}
		}
		title, body, labels = templates.ImplementIssue(*change, *design)
	default:
		a.errorf("unknown issue class %q\n", kind)
		return 2
	}
	if *bodyFile != "" {
		rawBody, ok := a.readBodyFile(*bodyFile)
		if !ok {
			return 2
		}
		if strings.TrimSpace(rawBody) == "" {
			a.errorf("--body-file must not be empty\n")
			return 2
		}
		body, err = ensureIssueBodyMarker(kind, *change, rawBody)
		if err != nil {
			a.errorf("prepare issue body: %v\n", err)
			return 2
		}
	}

	issue, err := client.CreateIssue(ctx, repo, title, body, labels)
	if err != nil {
		a.errorf("create %s issue: %v\n", kind, err)
		return 1
	}
	result := map[string]any{"ok": true, "type": kind, "number": issue.Number, "url": issue.HTMLURL, "title": issue.Title}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "created %s issue #%d: %s\n", kind, issue.Number, issue.HTMLURL)
	return 0
}

func (a *app) runIssueUpdate(ctx context.Context, args []string) int {
	fs := newFlagSet("issue update", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	issueFlag := fs.String("issue", "", "issue number or URL")
	titleFlag := fs.String("title", "", "replacement issue title")
	bodyFile := fs.String("body-file", "", "replacement markdown issue body file, or - for stdin")
	summaryFlag := fs.String("summary", "", "human-readable update summary comment")
	summaryFile := fs.String("summary-file", "", "human-readable update summary file, or - for stdin")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	issueNumber, err := parseIssueFlag(*issueFlag, "issue")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	title := strings.TrimSpace(*titleFlag)
	var titlePtr *string
	if title != "" {
		titlePtr = &title
	}
	if *bodyFile == "-" && *summaryFile == "-" {
		a.errorf("--body-file - and --summary-file - cannot both read from stdin\n")
		return 2
	}
	if strings.TrimSpace(*summaryFlag) != "" && *summaryFile != "" {
		a.errorf("--summary and --summary-file cannot both be provided\n")
		return 2
	}

	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for issue update on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}

	var bodyPtr *string
	if *bodyFile != "" {
		rawBody, ok := a.readBodyFile(*bodyFile)
		if !ok {
			return 2
		}
		if strings.TrimSpace(rawBody) == "" {
			a.errorf("--body-file must not be empty\n")
			return 2
		}
		existing, err := client.GetIssue(ctx, repo, issueNumber)
		if err != nil {
			a.errorf("read issue #%d: %v\n", issueNumber, err)
			return 1
		}
		body, err := preserveIssueBodyMarker(existing.Body, rawBody)
		if err != nil {
			a.errorf("prepare issue body: %v\n", err)
			return 2
		}
		bodyPtr = &body
	}
	if titlePtr == nil && bodyPtr == nil {
		a.errorf("--title or --body-file is required\n")
		return 2
	}
	summary := strings.TrimSpace(*summaryFlag)
	if *summaryFile != "" {
		rawSummary, ok := a.readFlagFile(*summaryFile, "summary-file")
		if !ok {
			return 2
		}
		summary = strings.TrimSpace(rawSummary)
		if summary == "" {
			a.errorf("--summary-file must not be empty\n")
			return 2
		}
	}

	issue, err := client.UpdateIssue(ctx, repo, issueNumber, github.UpdateIssueOptions{Title: titlePtr, Body: bodyPtr})
	if err != nil {
		a.errorf("update issue #%d: %v\n", issueNumber, err)
		return 1
	}

	result := map[string]any{"ok": true, "issue": issue.Number, "url": issue.HTMLURL, "title": issue.Title}
	if summary != "" {
		comment, err := client.CreateComment(ctx, repo, issueNumber, renderIssueUpdateSummary(issue.Number, issue.HTMLURL, summary))
		if err != nil {
			a.errorf("create issue update summary comment: %v\n", err)
			return 1
		}
		result["summary_comment_id"] = comment.ID
		result["summary_url"] = comment.HTMLURL
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "updated issue #%d: %s\n", issue.Number, issue.HTMLURL)
	if summaryURL, ok := result["summary_url"].(string); ok {
		fmt.Fprintf(a.out, "created update summary: %s\n", summaryURL)
	}
	return 0
}

var issueBodyMarkerLineRe = regexp.MustCompile(`^<!--\s*issue-spec:issue=([a-z]+)(?:\s+[^>]*)?-->$`)

func ensureIssueBodyMarker(kind, change, body string) (string, error) {
	body = strings.TrimLeft(body, "\n")
	if marker, markerKind := extractIssueBodyMarker(body); marker != "" {
		if markerKind != kind {
			return "", fmt.Errorf("body marker issue class is %s, command requested %s", markerKind, kind)
		}
		return body, nil
	}
	return fmt.Sprintf("<!-- issue-spec:issue=%s change=%s version=1 -->\n%s", kind, change, body), nil
}

func preserveIssueBodyMarker(existing, replacement string) (string, error) {
	replacement = strings.TrimLeft(replacement, "\n")
	if _, replacementKind := extractIssueBodyMarker(replacement); replacementKind != "" {
		if _, existingKind := extractIssueBodyMarker(existing); existingKind != "" && replacementKind != existingKind {
			return "", fmt.Errorf("replacement marker issue class is %s, existing issue class is %s", replacementKind, existingKind)
		}
		return replacement, nil
	}
	if marker, _ := extractIssueBodyMarker(existing); marker != "" {
		return marker + "\n" + replacement, nil
	}
	return replacement, nil
}

func hasIssueBodyMarker(body string) bool {
	marker, _ := extractIssueBodyMarker(body)
	return marker != ""
}

func extractIssueBodyMarker(body string) (string, string) {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if match := issueBodyMarkerLineRe.FindStringSubmatch(trimmed); match != nil {
			return trimmed, match[1]
		}
	}
	return "", ""
}

func renderIssueUpdateSummary(issueNumber int, issueURL, summary string) string {
	target := strings.TrimSpace(issueURL)
	if target == "" {
		target = "N/A"
	}
	return fmt.Sprintf(`<!-- issue-spec:issue-update-summary version=1 -->
### Issue Body Update Summary

- Issue: #%d
- Target: %s

%s
`, issueNumber, target, strings.TrimSpace(summary))
}
