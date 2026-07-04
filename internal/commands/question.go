package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

func (a *app) runQuestion(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec question create|resolve ...\n")
		return 2
	}
	switch args[0] {
	case "create":
		return a.runQuestionCreate(ctx, args[1:])
	case "resolve":
		return a.runQuestionResolve(ctx, args[1:])
	default:
		a.errorf("unknown question command %q\n", args[0])
		return 2
	}
}

func (a *app) runQuestionCreate(ctx context.Context, args []string) int {
	fs := newFlagSet("question create", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	issueFlag := fs.String("issue", "", "issue number or URL")
	id := fs.String("id", "", "QUESTION id")
	question := fs.String("question", "", "question text")
	bodyFile := fs.String("body-file", "", "question body file, or - for stdin")
	blocking := fs.Bool("blocking", false, "create a blocking question with Status: blocked")
	assumption := fs.String("assumption", "", "default assumption while blocked")
	statusFlag := fs.String("status", "", "override status")
	agent := fs.String("agent", "Coordinator", "logical agent identity")
	agentSession := addAgentSessionFlag(fs)
	scope := fs.String("scope", "N/A", "question scope")
	related := fs.String("related", "", "comma-separated related comment URLs")
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
		a.errorf("auth required for question create on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	issue, err := client.GetIssue(ctx, repo, issueNumber)
	if err != nil {
		a.errorf("read issue #%d: %v\n", issueNumber, err)
		return 1
	}

	questionText := strings.TrimSpace(*question)
	if *bodyFile != "" {
		body, ok := a.readBodyFile(*bodyFile)
		if !ok {
			return 2
		}
		questionText = strings.TrimSpace(body)
	}
	if questionText == "" {
		a.errorf("--question or --body-file is required\n")
		return 2
	}
	status := strings.TrimSpace(*statusFlag)
	if status == "" {
		if *blocking {
			status = "blocked"
		} else {
			status = "draft"
		}
	}
	links := issueLinksForRole(issue.Body, issue.HTMLURL)
	if values := parseCSVLinks(*related); len(values) > 0 {
		links["Related Comments"] = values
	}
	session := resolveWriterSession(*agentSession)
	body, err := templates.QuestionComment(templates.QuestionOptions{
		ID:                 *id,
		Agent:              *agent,
		AgentSessionID:     session.ID,
		AgentSessionSource: session.Source,
		Status:             status,
		Scope:              *scope,
		Blocking:           *blocking,
		Question:           questionText,
		Assumption:         *assumption,
		Links:              links,
	})
	if err != nil {
		a.errorf("render question body: %v\n", err)
		return 2
	}
	action, comment, err := upsertTypedComment(ctx, client, repo, issueNumber, "QUESTION", *id, body)
	if err != nil {
		a.errorf("upsert QUESTION %s: %v\n", *id, err)
		return 1
	}
	result := map[string]any{"ok": true, "action": action, "issue": issueNumber, "comment_id": comment.ID, "url": comment.HTMLURL, "api_url": comment.URL, "type": "QUESTION", "id": *id, "status": status, "blocking": *blocking}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "%s QUESTION %s on issue #%d: %s\n", action, *id, issueNumber, comment.HTMLURL)
	return 0
}

func (a *app) runQuestionResolve(ctx context.Context, args []string) int {
	fs := newFlagSet("question resolve", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	issueFlag := fs.String("issue", "", "issue number or URL")
	id := fs.String("id", "", "QUESTION id")
	resolution := fs.String("resolution", "", "resolution text")
	resolutionFile := fs.String("resolution-file", "", "resolution file, or - for stdin")
	status := fs.String("status", "confirmed", "resolved status")
	acceptedAssumption := fs.Bool("accepted-assumption", false, "record that the default assumption was accepted")
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
	resolutionText := strings.TrimSpace(*resolution)
	if *resolutionFile != "" {
		body, ok := a.readBodyFile(*resolutionFile)
		if !ok {
			return 2
		}
		resolutionText = strings.TrimSpace(body)
	}
	if resolutionText == "" {
		a.errorf("--resolution or --resolution-file is required\n")
		return 2
	}
	if *acceptedAssumption {
		resolutionText = "Accepted default assumption. " + resolutionText
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for question resolve on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	artifact, body, err := findArtifactByID(ctx, client, repo, issueNumber, *id)
	if err != nil {
		a.errorf("%v\n", err)
		return 1
	}
	if artifact.Comment.Type != "QUESTION" {
		a.errorf("%s is %s, not QUESTION\n", *id, artifact.Comment.Type)
		return 1
	}
	updatedBody, err := model.SetTypedCommentStatus(body, *status)
	if err != nil {
		a.errorf("update QUESTION status: %v\n", err)
		return 1
	}
	updatedBody = model.AppendResolutionLog(updatedBody, resolutionText)
	comment, err := client.UpdateComment(ctx, repo, artifact.CommentID, updatedBody)
	if err != nil {
		a.errorf("patch QUESTION %s: %v\n", *id, err)
		return 1
	}
	result := map[string]any{"ok": true, "action": "updated", "issue": issueNumber, "comment_id": comment.ID, "url": comment.HTMLURL, "api_url": comment.URL, "type": "QUESTION", "id": *id, "status": *status}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "resolved QUESTION %s on issue #%d: %s\n", *id, issueNumber, comment.HTMLURL)
	return 0
}

func issueLinksForRole(issueBody, issueURL string) map[string][]string {
	links := map[string][]string{}
	switch {
	case strings.Contains(issueBody, "issue-spec:issue=proposal"):
		links["Proposal Issue"] = []string{issueURL}
	case strings.Contains(issueBody, "issue-spec:issue=design"):
		links["Design Issue"] = []string{issueURL}
	case strings.Contains(issueBody, "issue-spec:issue=implement"):
		links["Implement Issue"] = []string{issueURL}
	}
	return links
}

func parseCSVLinks(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
