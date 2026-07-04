package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/model"
)

func (a *app) runComment(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec comment upsert|list ...\n")
		return 2
	}
	switch args[0] {
	case "upsert":
		return a.runCommentUpsert(ctx, args[1:])
	case "list":
		return a.runCommentList(ctx, args[1:])
	default:
		a.errorf("unknown comment command %q\n", args[0])
		return 2
	}
}

func (a *app) runCommentUpsert(ctx context.Context, args []string) int {
	fs := newFlagSet("comment upsert", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	issueFlag := fs.String("issue", "", "issue number or URL")
	commentType := fs.String("type", "", "typed comment type")
	id := fs.String("id", "", "typed comment id")
	bodyFile := fs.String("body-file", "", "markdown body file, or - for stdin")
	agent := fs.String("agent", "Coordinator", "logical agent identity")
	status := fs.String("status", "draft", "typed comment status")
	scope := fs.String("scope", "N/A", "typed comment scope")
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
	rawBody, ok := a.readBodyFile(*bodyFile)
	if !ok {
		return 2
	}
	body, err := model.EnsureTypedBody(*commentType, *id, rawBody, model.BodyOptions{Agent: *agent, Status: *status, Scope: *scope})
	if err != nil {
		a.errorf("prepare typed comment body: %v\n", err)
		return 2
	}

	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for comment upsert on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	action, comment, err := upsertTypedComment(ctx, client, repo, issueNumber, *commentType, *id, body)
	if err != nil {
		a.errorf("upsert comment: %v\n", err)
		return 1
	}
	result := map[string]any{"ok": true, "action": action, "issue": issueNumber, "comment_id": comment.ID, "url": comment.HTMLURL, "api_url": comment.URL, "type": strings.ToUpper(*commentType), "id": *id}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "%s %s %s on issue #%d: %s\n", action, strings.ToUpper(*commentType), *id, issueNumber, comment.HTMLURL)
	return 0
}

func (a *app) runCommentList(ctx context.Context, args []string) int {
	fs := newFlagSet("comment list", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	issueFlag := fs.String("issue", "", "issue number or URL")
	commentType := fs.String("type", "", "filter by typed comment type")
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
		a.errorf("auth required for comment list on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	comments, err := client.ListIssueComments(ctx, repo, issueNumber)
	if err != nil {
		a.errorf("list issue comments: %v\n", err)
		return 1
	}
	var artifacts []model.Artifact
	for _, comment := range comments {
		if !model.IsLikelyTyped(comment.Body) {
			continue
		}
		tc := model.ParseTypedComment(comment.Body)
		if *commentType != "" && tc.Type != strings.ToUpper(*commentType) {
			continue
		}
		artifacts = append(artifacts, model.Artifact{Issue: issueNumber, CommentID: comment.ID, URL: comment.HTMLURL, APIURL: comment.URL, Comment: tc})
	}
	if *jsonOut {
		return a.outputJSON(map[string]any{"ok": true, "issue": issueNumber, "comments": artifacts})
	}
	for _, artifact := range artifacts {
		tc := artifact.Comment
		fmt.Fprintf(a.out, "%-9s %-12s %-12s %-30s %s\n", tc.Type, tc.ID, tc.Status, tc.Scope, artifact.URL)
		if len(tc.Errors) > 0 {
			for _, parseErr := range tc.Errors {
				fmt.Fprintf(a.out, "  malformed: %s\n", parseErr)
			}
		}
	}
	return 0
}
