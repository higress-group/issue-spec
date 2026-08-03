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
	"github.com/higress-group/issue-spec/internal/workflow"
)

func (a *app) runIssue(ctx context.Context, args []string) int {
	if len(args) < 1 {
		a.errorf("usage: issue-spec issue create simple --repo owner/repo --title title --body-file file.md\n")
		a.errorf("       issue-spec issue create proposal|design|implement --repo owner/repo --change name [--body-file file.md] [--title title]\n")
		a.errorf("       issue-spec issue update --repo owner/repo --issue N [--title title] [--body-file file.md] [--summary \"what changed\"]\n")
		a.errorf("       issue-spec issue list --repo owner/repo [--state open|closed|all] --json\n")
		a.errorf("       issue-spec issue close|reopen --repo owner/repo --issue N [--json]\n")
		return 2
	}
	switch args[0] {
	case "create":
		if len(args) < 2 {
			a.errorf("issue class is required: simple, proposal, design, or implement\n")
			return 2
		}
		return a.runIssueCreate(ctx, args[1], args[2:])
	case "update":
		return a.runIssueUpdate(ctx, args[1:])
	case "list":
		return a.runIssueList(ctx, args[1:])
	case "close":
		return a.runIssueState(ctx, "closed", args[1:])
	case "reopen":
		return a.runIssueState(ctx, "open", args[1:])
	default:
		a.errorf("unknown issue command %q\n", args[0])
		return 2
	}
}

type issueListItem struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Body   string `json:"body"`
}

type issueListResult struct {
	OK     bool            `json:"ok"`
	Repo   string          `json:"repo"`
	State  string          `json:"state"`
	Issues []issueListItem `json:"issues"`
}

func (a *app) runIssueList(ctx context.Context, args []string) int {
	fs := newFlagSet("issue list", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	stateFlag := fs.String("state", "open", "issue state: open, closed, or all")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if !*jsonOut {
		a.errorf("--json is required for issue list\n")
		return 2
	}
	state, ok := normalizeIssueListState(*stateFlag)
	if !ok {
		a.errorf("--state must be open, closed, or all\n")
		return 2
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for issue list on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	listed, err := client.ListIssues(ctx, repo, github.ListIssueOptions{State: state})
	if err != nil {
		a.errorf("list issues: %v\n", err)
		return 1
	}
	issues := make([]issueListItem, 0, len(listed))
	for _, issue := range listed {
		if issue.PullRequest != nil {
			continue
		}
		issues = append(issues, issueListItem{
			Number: issue.Number,
			Title:  issue.Title,
			State:  strings.ToLower(strings.TrimSpace(issue.State)),
			URL:    issue.HTMLURL,
			Body:   issue.Body,
		})
	}
	return a.outputJSON(issueListResult{OK: true, Repo: repo, State: state, Issues: issues})
}

func normalizeIssueListState(value string) (string, bool) {
	state := strings.ToLower(strings.TrimSpace(value))
	if state == "" {
		state = "open"
	}
	switch state {
	case "open", "closed", "all":
		return state, true
	default:
		return "", false
	}
}

type issueStateResult struct {
	OK      bool   `json:"ok"`
	Issue   int    `json:"issue"`
	URL     string `json:"url"`
	State   string `json:"state"`
	Changed bool   `json:"changed"`
}

func (a *app) runIssueState(ctx context.Context, target string, args []string) int {
	command := "issue " + map[string]string{"closed": "close", "open": "reopen"}[target]
	fs := newFlagSet(command, a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	issueFlag := fs.String("issue", "", "issue number or URL")
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
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for %s on %s: %v\n", command, auth.NormalizeHost(*host), err)
		return 1
	}
	issue, err := client.GetIssue(ctx, repo, issueNumber)
	if err != nil {
		a.errorf("read issue #%d: %v\n", issueNumber, err)
		return 1
	}
	changed := strings.ToLower(strings.TrimSpace(issue.State)) != target
	if changed {
		issue, err = client.UpdateIssue(ctx, repo, issueNumber, github.UpdateIssueOptions{State: &target})
		if err != nil {
			a.errorf("set issue #%d state to %s: %v\n", issueNumber, target, err)
			return 1
		}
	}
	if issue.Number == 0 {
		issue.Number = issueNumber
	}
	result := issueStateResult{OK: true, Issue: issue.Number, URL: issue.HTMLURL, State: target, Changed: changed}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "issue #%d state=%s changed=%t: %s\n", result.Issue, result.State, result.Changed, result.URL)
	return 0
}

func (a *app) runIssueCreate(ctx context.Context, kind string, args []string) int {
	if kind == "simple" {
		return a.runIssueCreateSimple(ctx, args)
	}
	fs := newFlagSet("issue create "+kind, a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	change := fs.String("change", "", "change name")
	proposal := fs.String("proposal", "", "proposal issue number or URL")
	design := fs.String("design", "", "design issue number or URL")
	bodyFile := fs.String("body-file", "", "markdown issue body file, or - for stdin")
	titleFlag := fs.String("title", "", "custom issue title")
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
	workflowPlan, workflowErr := workflow.Resolve(".")
	if workflowErr != nil {
		a.errorf("workflow validation failed: %v\n", workflowErr)
		for _, diagnostic := range workflowPlan.Diagnostics {
			if diagnostic.Severity == "error" {
				a.errorf("- %s: %s\n", diagnostic.Code, diagnostic.Message)
			}
		}
		return 1
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for issue create on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}

	var title, body string
	var labels []string
	authoringOptions := templates.WorkflowAuthoringOptions{HTMLReviewEnabled: workflowPlan.HTMLReviewEnabled()}
	switch kind {
	case "proposal":
		title, body, labels = templates.ProposalIssueWithOptions(*change, authoringOptions)
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
		if err := validatePhasePredecessor(ctx, client, repo, proposalIssue, "proposal", *change); err != nil {
			a.errorf("invalid proposal predecessor: %v\n", err)
			return 1
		}
		title, body, labels = templates.DesignIssueWithOptions(*change, *proposal, authoringOptions)
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
		if err := validatePhasePredecessor(ctx, client, repo, designIssue, "design", *change); err != nil {
			a.errorf("invalid design predecessor: %v\n", err)
			return 1
		}
		if *proposal != "" {
			proposalIssue, err := parseIssueFlag(*proposal, "proposal")
			if err != nil {
				a.errorf("%v\n", err)
				return 2
			}
			if err := validatePhasePredecessor(ctx, client, repo, proposalIssue, "proposal", *change); err != nil {
				a.errorf("invalid proposal predecessor: %v\n", err)
				return 1
			}
		}
		title, body, labels = templates.ImplementIssueWithOptions(*change, *design, authoringOptions)
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
	} else {
		rendered, used, err := renderIssueBodyFromWorkflow(workflowPlan, *repoFlag, kind, *change, *proposal, *design, body)
		if err != nil {
			a.errorf("render workflow issue template: %v\n", err)
			return 1
		}
		if used {
			body = rendered
		} else {
			body = templates.AppendIssueSpecIssueFooter(body)
		}
	}
	predecessor := ""
	if kind == "design" {
		predecessor = *proposal
	} else if kind == "implement" {
		predecessor = *design
	}
	body, err = ensureIssuePredecessorLink(kind, predecessor, body)
	if err != nil {
		a.errorf("prepare issue predecessor link: %v\n", err)
		return 2
	}
	title = templates.IssueTitle(kind, *change, body, *titleFlag)

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

var exactPhaseIssueMarkerLineRe = regexp.MustCompile(`^<!--\s*issue-spec:issue=([a-z]+)\s+change=([^\s>]+)\s+version=([0-9]+)\s*-->$`)

// validatePhasePredecessor protects explicit lineage without turning optional
// planning comments into an authoring gate. SPEC, QUESTION, TASK, and their
// relationships remain useful planning state, but phase issue creation neither
// requires nor interprets them.
func validatePhasePredecessor(ctx context.Context, client github.Operations, repo string, issueNumber int, wantKind, wantChange string) error {
	issue, err := client.GetIssue(ctx, repo, issueNumber)
	if err != nil {
		return fmt.Errorf("read %s issue %d: %w", wantKind, issueNumber, err)
	}
	var markers [][]string
	for _, line := range strings.Split(issue.Body, "\n") {
		if match := exactPhaseIssueMarkerLineRe.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			markers = append(markers, match)
		}
	}
	if len(markers) != 1 {
		return fmt.Errorf("issue %d must contain exactly one supported issue-spec marker, found %d", issueNumber, len(markers))
	}
	marker := markers[0]
	if marker[1] != wantKind {
		return fmt.Errorf("issue %d is %s, want %s", issueNumber, marker[1], wantKind)
	}
	if marker[2] != wantChange {
		return fmt.Errorf("issue %d belongs to change %q, want %q", issueNumber, marker[2], wantChange)
	}
	if marker[3] != "1" {
		return fmt.Errorf("issue %d uses unsupported marker version %s", issueNumber, marker[3])
	}
	return nil
}

func collectAnswerResolution(ctx context.Context, client github.Operations, repo string, issueNumbers ...int) (model.AnswerResolution, error) {
	var observations []model.AnswerObservation
	for _, issueNumber := range issueNumbers {
		if issueNumber <= 0 {
			continue
		}
		comments, err := client.ListIssueComments(ctx, repo, issueNumber)
		if err != nil {
			return model.AnswerResolution{}, err
		}
		observations = append(observations, answerObservationsFromComments(comments)...)
	}
	return model.ResolveEffectiveAnswers(observations), nil
}

func hasUnsatisfiedQuestion(artifacts []model.Artifact, answers model.AnswerResolution) bool {
	for _, artifact := range artifacts {
		if artifact.Comment.Type == "QUESTION" && !model.QuestionIsSatisfied(artifact.Comment, answers) {
			return true
		}
	}
	return false
}

func (a *app) runIssueCreateSimple(ctx context.Context, args []string) int {
	fs := newFlagSet("issue create simple", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	titleFlag := fs.String("title", "", "ordinary issue title")
	bodyFile := fs.String("body-file", "", "ordinary markdown issue body file, or - for stdin")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	title := strings.TrimSpace(*titleFlag)
	if title == "" {
		a.errorf("--title is required\n")
		return 2
	}
	if *bodyFile == "" {
		a.errorf("--body-file is required\n")
		return 2
	}
	body, ok := a.readBodyFile(*bodyFile)
	if !ok {
		return 2
	}
	if strings.TrimSpace(body) == "" {
		a.errorf("--body-file must not be empty\n")
		return 2
	}
	if hasIssueBodyMarker(body) {
		a.errorf("simple issue body must not contain an issue-spec typed issue marker; use proposal, design, or implement instead\n")
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for issue create on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	issue, err := client.CreateIssue(ctx, repo, title, body, nil)
	if err != nil {
		a.errorf("create simple issue: %v\n", err)
		return 1
	}
	result := map[string]any{"ok": true, "type": "simple", "number": issue.Number, "url": issue.HTMLURL, "title": issue.Title}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "created simple issue #%d: %s\n", issue.Number, issue.HTMLURL)
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
		body, err := preserveIssueBodyMetadata(existing.Body, rawBody)
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
var proposalIssueLinkLineRe = regexp.MustCompile(`(?i)^\s*-\s*Proposal\s+Issue\s*:\s*(.*?)\s*$`)
var designIssueLinkLineRe = regexp.MustCompile(`(?i)^\s*-\s*Design\s+Issue\s*:\s*(.*?)\s*$`)

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

// ensureIssuePredecessorLink makes the command-line predecessor flag the one
// authority for the raw issue body consumed by change projection. Custom body
// and project workflow templates remain free-form, but cannot omit, duplicate,
// or override the reserved predecessor bullet. Proposal bodies have no
// predecessor and are returned byte-for-byte unchanged.
func ensureIssuePredecessorLink(kind, predecessor, body string) (string, error) {
	var label string
	var linePattern *regexp.Regexp
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "proposal":
		return body, nil
	case "design":
		label, linePattern = "Proposal Issue", proposalIssueLinkLineRe
	case "implement":
		label, linePattern = "Design Issue", designIssueLinkLineRe
	default:
		return "", fmt.Errorf("unknown issue class %q", kind)
	}
	predecessor = strings.TrimSpace(predecessor)
	if predecessor == "" {
		return "", fmt.Errorf("%s predecessor is required", strings.ToLower(label))
	}
	authoritative := "- " + label + ": " + predecessor
	lines := strings.Split(body, "\n")
	result := make([]string, 0, len(lines)+2)
	replaced := false
	for _, line := range lines {
		if linePattern.MatchString(line) {
			if !replaced {
				result = append(result, authoritative)
				replaced = true
			}
			continue
		}
		result = append(result, line)
	}
	if replaced {
		return strings.Join(result, "\n"), nil
	}
	return strings.Join(insertIssueMetadataLine(result, predecessorLinkInsertIndex(result), authoritative), "\n"), nil
}

func insertIssueMetadataLine(lines []string, insertAt int, line string) []string {
	block := make([]string, 0, 3)
	if insertAt > 0 && strings.TrimSpace(lines[insertAt-1]) != "" {
		block = append(block, "")
	}
	block = append(block, line)
	if insertAt < len(lines) && strings.TrimSpace(lines[insertAt]) != "" {
		block = append(block, "")
	}
	withLink := make([]string, 0, len(lines)+len(block))
	withLink = append(withLink, lines[:insertAt]...)
	withLink = append(withLink, block...)
	withLink = append(withLink, lines[insertAt:]...)
	return withLink
}

func predecessorLinkInsertIndex(lines []string) int {
	anchor := -1
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			anchor = index
			break
		}
		if anchor < 0 && issueBodyMarkerLineRe.MatchString(trimmed) {
			anchor = index
		}
	}
	if anchor < 0 {
		return 0
	}
	insertAt := anchor + 1
	for insertAt < len(lines) && strings.TrimSpace(lines[insertAt]) == "" {
		insertAt++
	}
	return insertAt
}

func preserveIssueBodyMetadata(existing, replacement string) (string, error) {
	replacement = strings.TrimLeft(replacement, "\n")
	existingMarkers := extractIssueBodyMarkers(existing)
	if len(existingMarkers) == 0 {
		return replacement, nil
	}
	if len(existingMarkers) != 1 {
		return "", fmt.Errorf("stored issue body has %d issue-spec markers; exactly one is required", len(existingMarkers))
	}
	marker := existingMarkers[0]
	if marker.kind != "proposal" && marker.kind != "design" && marker.kind != "implement" {
		return "", fmt.Errorf("stored issue body has unsupported issue class %s", marker.kind)
	}
	lines := strings.Split(replacement, "\n")
	withoutDuplicateMarkers := make([]string, 0, len(lines)+1)
	markerWritten := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		match := issueBodyMarkerLineRe.FindStringSubmatch(trimmed)
		if match == nil {
			withoutDuplicateMarkers = append(withoutDuplicateMarkers, line)
			continue
		}
		if trimmed != marker.line {
			return "", fmt.Errorf("replacement issue marker conflicts with stored marker")
		}
		if !markerWritten {
			withoutDuplicateMarkers = append(withoutDuplicateMarkers, marker.line)
			markerWritten = true
		}
	}
	if !markerWritten {
		withoutDuplicateMarkers = append([]string{marker.line}, withoutDuplicateMarkers...)
	}
	prepared := strings.Join(withoutDuplicateMarkers, "\n")
	if marker.kind == "proposal" {
		return prepared, nil
	}
	pattern := proposalIssueLinkLineRe
	label := "Proposal Issue"
	if marker.kind == "implement" {
		pattern = designIssueLinkLineRe
		label = "Design Issue"
	}
	storedLinks := extractIssuePredecessorLines(existing, pattern)
	if len(storedLinks) != 1 || storedLinks[0].reference == "" {
		return "", fmt.Errorf("stored %s body must contain exactly one non-empty %s link", marker.kind, label)
	}
	stored := storedLinks[0]
	lines = strings.Split(prepared, "\n")
	result := make([]string, 0, len(lines)+1)
	linkWritten := false
	for _, line := range lines {
		match := pattern.FindStringSubmatch(line)
		if match == nil {
			result = append(result, line)
			continue
		}
		reference := strings.TrimSpace(match[1])
		if reference != stored.reference {
			return "", fmt.Errorf("replacement %s link %q conflicts with stored reference %q", label, reference, stored.reference)
		}
		if !linkWritten {
			result = append(result, stored.line)
			linkWritten = true
		}
	}
	if linkWritten {
		return strings.Join(result, "\n"), nil
	}
	return strings.Join(insertIssueMetadataLine(result, predecessorLinkInsertIndex(result), stored.line), "\n"), nil
}

func hasIssueBodyMarker(body string) bool {
	marker, _ := extractIssueBodyMarker(body)
	return marker != ""
}

func extractIssueBodyMarker(body string) (string, string) {
	markers := extractIssueBodyMarkers(body)
	if len(markers) > 0 {
		return markers[0].line, markers[0].kind
	}
	return "", ""
}

type issueBodyMarker struct {
	line string
	kind string
}

func extractIssueBodyMarkers(body string) []issueBodyMarker {
	var markers []issueBodyMarker
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if match := issueBodyMarkerLineRe.FindStringSubmatch(trimmed); match != nil {
			markers = append(markers, issueBodyMarker{line: trimmed, kind: match[1]})
		}
	}
	return markers
}

type issuePredecessorLine struct {
	line      string
	reference string
}

func extractIssuePredecessorLines(body string, pattern *regexp.Regexp) []issuePredecessorLine {
	var links []issuePredecessorLine
	for _, line := range strings.Split(body, "\n") {
		if match := pattern.FindStringSubmatch(line); match != nil {
			links = append(links, issuePredecessorLine{line: strings.TrimSpace(line), reference: strings.TrimSpace(match[1])})
		}
	}
	return links
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
