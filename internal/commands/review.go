package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func (a *app) runReview(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec review finding|reply|sync ...\n")
		return 2
	}
	switch args[0] {
	case "finding":
		return a.runReviewFinding(ctx, args[1:])
	case "reply":
		return a.runReviewReply(ctx, args[1:])
	case "sync":
		return a.runReviewSync(ctx, args[1:])
	default:
		a.errorf("unknown review command %q\n", args[0])
		return 2
	}
}

func (a *app) runReviewFinding(ctx context.Context, args []string) int {
	fs := newFlagSet("review finding", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	prFlag := fs.Int("pr", 0, "pull request number")
	pathFlag := fs.String("path", "", "changed file path")
	lineFlag := fs.Int("line", 0, "RIGHT-side line number in the PR diff")
	id := fs.String("id", "", "FINDING id")
	severity := fs.String("severity", "P2", "finding severity: P0, P1, or P2")
	processID := fs.String("process", "", "PROCESS id assigned to fix this finding")
	specID := fs.String("spec", "", "SPEC id")
	specURL := fs.String("spec-url", "", "SPEC comment URL")
	bodyFile := fs.String("body-file", "", "finding body file, or - for stdin")
	bodyText := fs.String("body", "", "finding body text")
	agent := fs.String("agent", "Review Agent", "logical agent identity")
	agentSession := addAgentSessionFlag(fs)
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	if *prFlag <= 0 {
		a.errorf("--pr must be a positive pull request number\n")
		return 2
	}
	if strings.TrimSpace(*pathFlag) == "" {
		a.errorf("--path is required\n")
		return 2
	}
	if *lineFlag <= 0 {
		a.errorf("--line must be a positive RIGHT-side diff line\n")
		return 2
	}
	body := strings.TrimSpace(*bodyText)
	if *bodyFile != "" {
		content, ok := a.readBodyFile(*bodyFile)
		if !ok {
			return 2
		}
		body = strings.TrimSpace(content)
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for review finding on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	session := resolveWriterSession(*agentSession)
	result, err := createReviewFinding(ctx, client, repo, *prFlag, *pathFlag, *lineFlag, *id, *severity, *processID, *specID, *specURL, *agent, session, body)
	if err != nil {
		a.errorf("create review finding: %v\n", err)
		return 1
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	if result.Created {
		fmt.Fprintf(a.out, "created review finding: %s\n", result.URL)
	} else {
		fmt.Fprintf(a.out, "review finding already exists: %s\n", result.URL)
	}
	return 0
}

func (a *app) runReviewReply(ctx context.Context, args []string) int {
	fs := newFlagSet("review reply", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	prFlag := fs.Int("pr", 0, "pull request number")
	commentID := fs.Int64("comment-id", 0, "parent PR review comment id")
	findingID := fs.String("finding", "", "FINDING id")
	processID := fs.String("process", "", "PROCESS id that fixed this finding")
	status := fs.String("status", "resolved", "reply status: resolved, fixed, done, closed, or superseded")
	bodyFile := fs.String("body-file", "", "reply body file, or - for stdin")
	bodyText := fs.String("body", "", "reply body text")
	agent := fs.String("agent", "Worker Agent", "logical agent identity")
	agentSession := addAgentSessionFlag(fs)
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	if *prFlag <= 0 {
		a.errorf("--pr must be a positive pull request number\n")
		return 2
	}
	if *commentID <= 0 {
		a.errorf("--comment-id must be a positive PR review comment id\n")
		return 2
	}
	body := strings.TrimSpace(*bodyText)
	if *bodyFile != "" {
		content, ok := a.readBodyFile(*bodyFile)
		if !ok {
			return 2
		}
		body = strings.TrimSpace(content)
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for review reply on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	session := resolveWriterSession(*agentSession)
	result, err := replyReviewFinding(ctx, client, repo, *prFlag, *commentID, *findingID, *processID, *status, *agent, session, body)
	if err != nil {
		a.errorf("reply to review finding: %v\n", err)
		return 1
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	if result.Created {
		fmt.Fprintf(a.out, "created review finding reply: %s\n", result.URL)
	} else {
		fmt.Fprintf(a.out, "review finding reply already exists: %s\n", result.URL)
	}
	return 0
}

func (a *app) runReviewSync(ctx context.Context, args []string) int {
	fs := newFlagSet("review sync", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	prFlag := fs.Int("pr", 0, "pull request number")
	implementFlag := fs.String("implement", "", "implement issue number or URL")
	id := fs.String("id", "", "REVIEW id to upsert")
	agent := fs.String("agent", "Coordinator", "logical agent identity")
	agentSession := addAgentSessionFlag(fs)
	scope := fs.String("scope", "pr-review", "review scope")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	if *prFlag <= 0 {
		a.errorf("--pr must be a positive pull request number\n")
		return 2
	}
	implementIssue, err := parseIssueFlag(*implementFlag, "implement")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	if strings.TrimSpace(*id) == "" {
		a.errorf("--id is required\n")
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for review sync on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	pr, err := client.GetPullRequest(ctx, repo, *prFlag)
	if err != nil {
		a.errorf("read PR #%d: %v\n", *prFlag, err)
		return 1
	}
	reviewComments, err := client.ListPullRequestReviewComments(ctx, repo, *prFlag)
	if err != nil {
		a.errorf("read PR #%d review comments: %v\n", *prFlag, err)
		return 1
	}
	issueComments, err := client.ListIssueComments(ctx, repo, *prFlag)
	if err != nil {
		a.errorf("read PR #%d issue comments: %v\n", *prFlag, err)
		return 1
	}
	status, err := client.GetCombinedStatus(ctx, repo, pr.Head.SHA)
	if err != nil {
		a.errorf("read PR #%d status contexts: %v\n", *prFlag, err)
		return 1
	}
	checkRuns, err := client.ListCheckRuns(ctx, repo, pr.Head.SHA)
	if err != nil {
		a.errorf("read PR #%d check runs: %v\n", *prFlag, err)
		return 1
	}
	report := buildReviewSyncReport(pr, reviewComments, issueComments, status, checkRuns)
	session := resolveWriterSession(*agentSession)
	body, err := renderReviewSyncComment(*id, *agent, session, *scope, pr.HTMLURL, report)
	if err != nil {
		a.errorf("render review sync comment: %v\n", err)
		return 1
	}
	action, comment, err := upsertTypedComment(ctx, client, repo, implementIssue, "REVIEW", *id, body)
	if err != nil {
		a.errorf("upsert REVIEW %s: %v\n", *id, err)
		return 1
	}
	result := map[string]any{"ok": report.OK, "action": action, "comment_id": comment.ID, "url": comment.HTMLURL, "review": report}
	if *jsonOut {
		if code := a.outputJSON(result); code != 0 {
			return code
		}
		if !report.OK {
			return 1
		}
		return 0
	}
	fmt.Fprintf(a.out, "%s REVIEW %s: %s\n", action, *id, comment.HTMLURL)
	if !report.OK {
		return 1
	}
	return 0
}

type reviewFindingResult struct {
	OK        bool   `json:"ok"`
	Created   bool   `json:"created"`
	CommentID int64  `json:"comment_id"`
	URL       string `json:"url"`
	PR        int    `json:"pr"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Finding   string `json:"finding"`
	Severity  string `json:"severity"`
	Process   string `json:"process"`
	Spec      string `json:"spec"`
}

type reviewReplyResult struct {
	OK              bool   `json:"ok"`
	Created         bool   `json:"created"`
	CommentID       int64  `json:"comment_id"`
	ParentCommentID int64  `json:"parent_comment_id"`
	URL             string `json:"url"`
	PR              int    `json:"pr"`
	Finding         string `json:"finding"`
	Process         string `json:"process"`
	Status          string `json:"status"`
}

func createReviewFinding(ctx context.Context, client interface {
	GetPullRequest(context.Context, string, int) (github.PullRequest, error)
	ListPullRequestFiles(context.Context, string, int) ([]github.PullRequestFile, error)
	ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error)
	CreatePullRequestReviewComment(context.Context, string, int, string, string, string, int, string) (github.PullRequestReviewComment, error)
}, repo string, prNumber int, path string, line int, findingID, severity, processID, specID, specURL, agent string, session writerSession, findingBody string) (reviewFindingResult, error) {
	path = strings.TrimSpace(path)
	findingID = strings.TrimSpace(findingID)
	processID = strings.TrimSpace(processID)
	specID = strings.TrimSpace(specID)
	files, err := client.ListPullRequestFiles(ctx, repo, prNumber)
	if err != nil {
		return reviewFindingResult{}, err
	}
	if !lineExistsInPullRequestFiles(path, line, files) {
		return reviewFindingResult{}, fmt.Errorf("%s:%d is not a RIGHT-side changed line in PR #%d", path, line, prNumber)
	}
	existing, err := client.ListPullRequestReviewComments(ctx, repo, prNumber)
	if err != nil {
		return reviewFindingResult{}, err
	}
	for _, comment := range existing {
		marker, ok, err := model.FindFindingMarker(comment.Body)
		if err != nil {
			return reviewFindingResult{}, err
		}
		if ok && model.SameFinding(marker, findingID, path, line) {
			return reviewFindingResult{
				OK:        true,
				Created:   false,
				CommentID: comment.ID,
				URL:       comment.HTMLURL,
				PR:        prNumber,
				Path:      path,
				Line:      line,
				Finding:   findingID,
				Severity:  marker.Severity,
				Process:   processID,
				Spec:      specID,
			}, nil
		}
	}
	pr, err := client.GetPullRequest(ctx, repo, prNumber)
	if err != nil {
		return reviewFindingResult{}, err
	}
	body, err := model.RenderFindingBodyWithSession(agent, session.ID, session.Source, findingID, severity, processID, specID, specURL, findingBody, "open", path, line)
	if err != nil {
		return reviewFindingResult{}, err
	}
	comment, err := client.CreatePullRequestReviewComment(ctx, repo, prNumber, body, pr.Head.SHA, path, line, "RIGHT")
	if err != nil {
		return reviewFindingResult{}, err
	}
	return reviewFindingResult{
		OK:        true,
		Created:   true,
		CommentID: comment.ID,
		URL:       comment.HTMLURL,
		PR:        prNumber,
		Path:      path,
		Line:      line,
		Finding:   findingID,
		Severity:  model.NormalizeFindingSeverity(severity),
		Process:   processID,
		Spec:      specID,
	}, nil
}

func replyReviewFinding(ctx context.Context, client interface {
	ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error)
	ReplyPullRequestReviewComment(context.Context, string, int, int64, string) (github.PullRequestReviewComment, error)
}, repo string, prNumber int, parentCommentID int64, findingID, processID, status, agent string, session writerSession, replyBody string) (reviewReplyResult, error) {
	findingID = strings.TrimSpace(findingID)
	processID = strings.TrimSpace(processID)
	status = model.NormalizeFindingStatus(status)
	existing, err := client.ListPullRequestReviewComments(ctx, repo, prNumber)
	if err != nil {
		return reviewReplyResult{}, err
	}
	foundParent := false
	for _, comment := range existing {
		if comment.ID == parentCommentID {
			foundParent = true
			continue
		}
		if comment.InReplyToID != parentCommentID {
			continue
		}
		marker, ok, err := model.FindFindingReplyMarker(comment.Body)
		if err != nil {
			return reviewReplyResult{}, err
		}
		if ok && marker.Finding == findingID && marker.Process == processID && marker.Status == status {
			return reviewReplyResult{
				OK:              true,
				Created:         false,
				CommentID:       comment.ID,
				ParentCommentID: parentCommentID,
				URL:             comment.HTMLURL,
				PR:              prNumber,
				Finding:         findingID,
				Process:         processID,
				Status:          status,
			}, nil
		}
	}
	if !foundParent {
		return reviewReplyResult{}, fmt.Errorf("parent PR review comment %d not found on PR #%d", parentCommentID, prNumber)
	}
	body, err := model.RenderFindingReplyBodyWithSession(agent, session.ID, session.Source, findingID, processID, status, replyBody)
	if err != nil {
		return reviewReplyResult{}, err
	}
	comment, err := client.ReplyPullRequestReviewComment(ctx, repo, prNumber, parentCommentID, body)
	if err != nil {
		return reviewReplyResult{}, err
	}
	return reviewReplyResult{
		OK:              true,
		Created:         true,
		CommentID:       comment.ID,
		ParentCommentID: parentCommentID,
		URL:             comment.HTMLURL,
		PR:              prNumber,
		Finding:         findingID,
		Process:         processID,
		Status:          status,
	}, nil
}

type reviewSyncReport struct {
	OK                 bool                 `json:"ok"`
	PR                 int                  `json:"pr"`
	PRURL              string               `json:"pr_url"`
	RationaleComments  int                  `json:"rationale_comments"`
	ActionableFindings []reviewFinding      `json:"actionable_findings"`
	BlockingFindings   []reviewFinding      `json:"blocking_findings"`
	ResolvedFindings   []reviewFinding      `json:"resolved_findings"`
	IssueComments      int                  `json:"issue_comments"`
	Diagnostics        []metadataDiagnostic `json:"diagnostics,omitempty"`
	FailedChecks       []reviewCheck        `json:"failed_checks"`
	PendingChecks      []reviewCheck        `json:"pending_checks"`
	PassedChecks       []reviewCheck        `json:"passed_checks"`
}

type reviewFinding struct {
	ID                 string `json:"id,omitempty"`
	CommentID          int64  `json:"comment_id"`
	URL                string `json:"url"`
	Path               string `json:"path"`
	Line               int    `json:"line"`
	Severity           string `json:"severity"`
	Status             string `json:"status"`
	Process            string `json:"process,omitempty"`
	Spec               string `json:"spec,omitempty"`
	AgentSessionID     string `json:"agent_session_id,omitempty"`
	AgentSessionSource string `json:"agent_session_source,omitempty"`
	Summary            string `json:"summary"`
}

type reviewCheck struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
}

func buildReviewSyncReport(pr github.PullRequest, reviewComments []github.PullRequestReviewComment, issueComments []github.Comment, status github.CombinedStatus, checkRuns []github.CheckRun) reviewSyncReport {
	report := reviewSyncReport{PR: pr.Number, PRURL: pr.HTMLURL, IssueComments: len(issueComments)}
	resolvedByParent := map[int64]bool{}
	for _, comment := range reviewComments {
		reply, ok, err := model.FindFindingReplyMarker(comment.Body)
		if err != nil || !ok || !model.IsTerminalFindingStatus(reply.Status) {
			continue
		}
		if comment.InReplyToID != 0 {
			resolvedByParent[comment.InReplyToID] = true
		}
	}
	for _, comment := range reviewComments {
		if rationale, ok, err := model.FindRationaleMarker(comment.Body); err == nil && ok {
			report.RationaleComments++
			report.Diagnostics = append(report.Diagnostics, artifactSessionDiagnostics("RATIONALE/"+rationale.Process, comment.HTMLURL, rationale.AgentSessionID, rationale.AgentSessionSource)...)
			continue
		}
		if reply, ok, err := model.FindFindingReplyMarker(comment.Body); err == nil && ok {
			report.Diagnostics = append(report.Diagnostics, artifactSessionDiagnostics("FINDING_REPLY/"+reply.Finding, comment.HTMLURL, reply.AgentSessionID, reply.AgentSessionSource)...)
			continue
		}
		if comment.InReplyToID != 0 {
			continue
		}
		finding, ok, err := model.FindFindingMarker(comment.Body)
		if err == nil && ok {
			item := reviewFinding{
				ID:                 finding.ID,
				CommentID:          comment.ID,
				URL:                comment.HTMLURL,
				Path:               firstNonEmpty(finding.Path, comment.Path),
				Line:               firstPositive(finding.Line, comment.Line),
				Severity:           finding.Severity,
				Status:             finding.Status,
				Process:            finding.Process,
				Spec:               finding.Spec,
				AgentSessionID:     finding.AgentSessionID,
				AgentSessionSource: finding.AgentSessionSource,
				Summary:            firstFindingSummary(comment.Body),
			}
			report.Diagnostics = append(report.Diagnostics, artifactSessionDiagnostics("FINDING/"+finding.ID, comment.HTMLURL, finding.AgentSessionID, finding.AgentSessionSource)...)
			if model.IsTerminalFindingStatus(item.Status) || resolvedByParent[comment.ID] {
				item.Status = "resolved"
				report.ResolvedFindings = append(report.ResolvedFindings, item)
				continue
			}
			report.ActionableFindings = append(report.ActionableFindings, item)
			if blocksReview(item.Severity) {
				report.BlockingFindings = append(report.BlockingFindings, item)
			}
			continue
		}
		item := reviewFinding{
			ID:        fmt.Sprintf("comment-%d", comment.ID),
			CommentID: comment.ID,
			URL:       comment.HTMLURL,
			Path:      comment.Path,
			Line:      comment.Line,
			Severity:  findingSeverity(comment.Body),
			Status:    "open",
			Summary:   firstFindingSummary(comment.Body),
		}
		report.ActionableFindings = append(report.ActionableFindings, item)
		if blocksReview(item.Severity) {
			report.BlockingFindings = append(report.BlockingFindings, item)
		}
	}
	for _, s := range status.Statuses {
		check := reviewCheck{Name: s.Context, State: s.State, URL: s.TargetURL}
		if s.State == "success" {
			report.PassedChecks = append(report.PassedChecks, check)
		} else if s.State == "pending" {
			report.PendingChecks = append(report.PendingChecks, check)
		} else {
			report.FailedChecks = append(report.FailedChecks, check)
		}
	}
	for _, run := range checkRuns {
		check := reviewCheck{Name: run.Name, State: run.Status, Conclusion: run.Conclusion, URL: firstNonEmpty(run.DetailsURL, run.HTMLURL)}
		if run.Status != "completed" {
			report.PendingChecks = append(report.PendingChecks, check)
			continue
		}
		switch run.Conclusion {
		case "success", "neutral", "skipped":
			report.PassedChecks = append(report.PassedChecks, check)
		default:
			report.FailedChecks = append(report.FailedChecks, check)
		}
	}
	sortReviewFindings(report.ActionableFindings)
	sortReviewFindings(report.BlockingFindings)
	sortReviewFindings(report.ResolvedFindings)
	report.OK = len(report.BlockingFindings) == 0 && len(report.FailedChecks) == 0 && len(report.PendingChecks) == 0
	return report
}

func renderReviewSyncComment(id, agent string, session writerSession, scope, prURL string, report reviewSyncReport) (string, error) {
	status := "done"
	if !report.OK {
		status = "blocked"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", model.RenderMarker("REVIEW", id, 1))
	b.WriteString(model.RenderHeader("REVIEW", id, model.BodyOptions{
		Agent:              agent,
		AgentSessionID:     session.ID,
		AgentSessionSource: session.Source,
		Status:             status,
		Scope:              scope,
		Links:              map[string][]string{"Implement Issue": {"N/A"}, "PR": {prURL}},
	}))
	b.WriteString("\n\n## Review Sync Summary\n\n")
	fmt.Fprintf(&b, "- PR: %s\n", prURL)
	fmt.Fprintf(&b, "- Rationale comments: %d\n", report.RationaleComments)
	fmt.Fprintf(&b, "- PR issue comments: %d\n", report.IssueComments)
	fmt.Fprintf(&b, "- Actionable findings: %d\n", len(report.ActionableFindings))
	fmt.Fprintf(&b, "- Blocking findings: %d\n", len(report.BlockingFindings))
	fmt.Fprintf(&b, "- Resolved findings: %d\n", len(report.ResolvedFindings))
	fmt.Fprintf(&b, "- Failed checks: %d\n", len(report.FailedChecks))
	fmt.Fprintf(&b, "- Pending checks: %d\n", len(report.PendingChecks))
	b.WriteString("\n## Actionable Findings\n\n")
	if len(report.ActionableFindings) == 0 {
		b.WriteString("- None.\n")
	} else {
		for _, finding := range report.ActionableFindings {
			fmt.Fprintf(&b, "- %s %s status=%s %s:%d %s %s\n", finding.Severity, finding.ID, finding.Status, finding.Path, finding.Line, finding.URL, finding.Summary)
		}
	}
	b.WriteString("\n## Blocking Findings\n\n")
	if len(report.BlockingFindings) == 0 {
		b.WriteString("- None.\n")
	} else {
		for _, finding := range report.BlockingFindings {
			fmt.Fprintf(&b, "- %s %s status=%s %s:%d %s %s\n", finding.Severity, finding.ID, finding.Status, finding.Path, finding.Line, finding.URL, finding.Summary)
		}
	}
	b.WriteString("\n## Resolved Findings\n\n")
	if len(report.ResolvedFindings) == 0 {
		b.WriteString("- None.\n")
	} else {
		for _, finding := range report.ResolvedFindings {
			fmt.Fprintf(&b, "- %s %s status=%s %s:%d %s %s\n", finding.Severity, finding.ID, finding.Status, finding.Path, finding.Line, finding.URL, finding.Summary)
		}
	}
	b.WriteString("\n## Checks\n\n")
	writeReviewChecks(&b, "Failed", report.FailedChecks)
	writeReviewChecks(&b, "Pending", report.PendingChecks)
	writeReviewChecks(&b, "Passed", report.PassedChecks)
	b.WriteString("\n## Verdict\n\n")
	if report.OK {
		b.WriteString("Review sync passed.\n")
	} else {
		b.WriteString("Review sync blocked.\n")
	}
	return b.String(), nil
}

func writeReviewChecks(b *strings.Builder, label string, checks []reviewCheck) {
	fmt.Fprintf(b, "### %s\n\n", label)
	if len(checks) == 0 {
		b.WriteString("- None.\n")
		return
	}
	for _, check := range checks {
		fmt.Fprintf(b, "- %s state=%s conclusion=%s %s\n", check.Name, check.State, check.Conclusion, check.URL)
	}
}

func sortReviewFindings(findings []reviewFinding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].CommentID == findings[j].CommentID {
			return findings[i].ID < findings[j].ID
		}
		return findings[i].CommentID < findings[j].CommentID
	})
}

func blocksReview(severity string) bool {
	switch model.NormalizeFindingSeverity(severity) {
	case "P0", "P1":
		return true
	default:
		return false
	}
}

func findingSeverity(body string) string {
	body = strings.ToUpper(body)
	for _, severity := range []string{"P0", "P1", "P2"} {
		if strings.Contains(body, severity) {
			return severity
		}
	}
	return "P2"
}

func firstFindingSummary(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "<!--") {
			continue
		}
		if isFindingMetadataLine(line) {
			continue
		}
		if len(line) > 120 {
			return line[:120] + "..."
		}
		return line
	}
	return ""
}

func isFindingMetadataLine(line string) bool {
	for _, prefix := range []string{"Agent:", "Agent Session ID:", "Agent Session Source:", "Type:", "ID:", "Finding:", "Severity:", "Status:", "Process:", "Spec:", "Spec Comment:"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
