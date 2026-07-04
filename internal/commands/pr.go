package commands

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func (a *app) runPR(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec pr rationale ...\n")
		return 2
	}
	switch args[0] {
	case "rationale":
		return a.runPRRationale(ctx, args[1:])
	case "link-process":
		return a.runPRLinkProcess(ctx, args[1:])
	default:
		a.errorf("unknown pr command %q\n", args[0])
		return 2
	}
}

func (a *app) runPRLinkProcess(ctx context.Context, args []string) int {
	fs := newFlagSet("pr link-process", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	issueFlag := fs.String("issue", "", "issue number or URL containing PROCESS comment")
	prFlag := fs.Int("pr", 0, "pull request number")
	processID := fs.String("process", "", "PROCESS id")
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
	if *prFlag <= 0 {
		a.errorf("--pr must be a positive pull request number\n")
		return 2
	}
	if strings.TrimSpace(*processID) == "" {
		a.errorf("--process is required\n")
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for pr link-process on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	pr, err := client.GetPullRequest(ctx, repo, *prFlag)
	if err != nil {
		a.errorf("read PR #%d: %v\n", *prFlag, err)
		return 1
	}
	artifact, body, err := findArtifactByID(ctx, client, repo, issueNumber, *processID)
	if err != nil {
		a.errorf("%v\n", err)
		return 1
	}
	if artifact.Comment.Type != "PROCESS" {
		a.errorf("%s is %s, not PROCESS\n", *processID, artifact.Comment.Type)
		return 1
	}
	updatedBody, changed, err := model.AddPRLink(body, pr.HTMLURL)
	if err != nil {
		a.errorf("update PROCESS PR link: %v\n", err)
		return 1
	}
	if changed {
		if _, err := client.UpdateComment(ctx, repo, artifact.CommentID, updatedBody); err != nil {
			a.errorf("patch %s: %v\n", *processID, err)
			return 1
		}
	}
	result := map[string]any{"ok": true, "changed": changed, "process": *processID, "issue": issueNumber, "pr": *prFlag, "pr_url": pr.HTMLURL}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "linked %s to PR %s\n", *processID, pr.HTMLURL)
	return 0
}

func (a *app) runPRRationale(ctx context.Context, args []string) int {
	fs := newFlagSet("pr rationale", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	prFlag := fs.Int("pr", 0, "pull request number")
	pathFlag := fs.String("path", "", "changed file path")
	lineFlag := fs.Int("line", 0, "RIGHT-side line number in the PR diff")
	bodyFile := fs.String("body-file", "", "rationale body file, or - for stdin")
	bodyText := fs.String("body", "", "rationale body text")
	processID := fs.String("process", "", "PROCESS id")
	specID := fs.String("spec", "", "SPEC id")
	specURL := fs.String("spec-url", "", "SPEC comment URL")
	agent := fs.String("agent", "Worker Agent", "logical agent identity")
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
		a.errorf("auth required for pr rationale on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	result, err := createRationale(ctx, client, repo, *prFlag, *pathFlag, *lineFlag, *processID, *specID, *specURL, *agent, body)
	if err != nil {
		a.errorf("create rationale: %v\n", err)
		return 1
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	if result.Created {
		fmt.Fprintf(a.out, "created rationale comment: %s\n", result.URL)
	} else {
		fmt.Fprintf(a.out, "rationale comment already exists: %s\n", result.URL)
	}
	return 0
}

type rationaleResult struct {
	OK        bool   `json:"ok"`
	Created   bool   `json:"created"`
	CommentID int64  `json:"comment_id"`
	URL       string `json:"url"`
	PR        int    `json:"pr"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Process   string `json:"process"`
	Spec      string `json:"spec"`
}

func createRationale(ctx context.Context, client interface {
	GetPullRequest(context.Context, string, int) (github.PullRequest, error)
	ListPullRequestFiles(context.Context, string, int) ([]github.PullRequestFile, error)
	ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error)
	CreatePullRequestReviewComment(context.Context, string, int, string, string, string, int, string) (github.PullRequestReviewComment, error)
}, repo string, prNumber int, path string, line int, processID, specID, specURL, agent, rationale string) (rationaleResult, error) {
	path = strings.TrimSpace(path)
	processID = strings.TrimSpace(processID)
	specID = strings.TrimSpace(specID)
	files, err := client.ListPullRequestFiles(ctx, repo, prNumber)
	if err != nil {
		return rationaleResult{}, err
	}
	if !lineExistsInPullRequestFiles(path, line, files) {
		return rationaleResult{}, fmt.Errorf("%s:%d is not a RIGHT-side changed line in PR #%d", path, line, prNumber)
	}
	existing, err := client.ListPullRequestReviewComments(ctx, repo, prNumber)
	if err != nil {
		return rationaleResult{}, err
	}
	for _, comment := range existing {
		marker, ok, err := model.FindRationaleMarker(comment.Body)
		if err != nil {
			return rationaleResult{}, err
		}
		if ok && model.SameRationale(marker, processID, specID, path, line) {
			return rationaleResult{OK: true, Created: false, CommentID: comment.ID, URL: comment.HTMLURL, PR: prNumber, Path: path, Line: line, Process: processID, Spec: specID}, nil
		}
	}
	pr, err := client.GetPullRequest(ctx, repo, prNumber)
	if err != nil {
		return rationaleResult{}, err
	}
	body, err := model.RenderRationaleBody(agent, processID, specID, specURL, rationale, path, line)
	if err != nil {
		return rationaleResult{}, err
	}
	comment, err := client.CreatePullRequestReviewComment(ctx, repo, prNumber, body, pr.Head.SHA, path, line, "RIGHT")
	if err != nil {
		return rationaleResult{}, err
	}
	return rationaleResult{OK: true, Created: true, CommentID: comment.ID, URL: comment.HTMLURL, PR: prNumber, Path: path, Line: line, Process: processID, Spec: specID}, nil
}

func lineExistsInPullRequestFiles(path string, line int, files []github.PullRequestFile) bool {
	for _, file := range files {
		if file.Filename != path {
			continue
		}
		lines := changedRightLines(file.Patch)
		i := sort.SearchInts(lines, line)
		return i < len(lines) && lines[i] == line
	}
	return false
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

func changedRightLines(patch string) []int {
	var lines []int
	rightLine := 0
	for _, raw := range strings.Split(patch, "\n") {
		if match := hunkHeaderRe.FindStringSubmatch(raw); match != nil {
			start, _ := strconv.Atoi(match[1])
			rightLine = start
			continue
		}
		if rightLine == 0 || raw == "" {
			continue
		}
		switch raw[0] {
		case '+':
			lines = append(lines, rightLine)
			rightLine++
		case '-':
		default:
			rightLine++
		}
	}
	sort.Ints(lines)
	return lines
}
