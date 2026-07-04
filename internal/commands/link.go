package commands

import (
	"context"
	"fmt"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/model"
)

func (a *app) runLink(ctx context.Context, args []string) int {
	fs := newFlagSet("link", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	fromID := fs.String("from", "", "source typed comment id")
	fromIssueFlag := fs.String("from-issue", "", "source issue number or URL")
	toID := fs.String("to", "", "target typed comment id")
	toIssueFlag := fs.String("to-issue", "", "target issue number or URL")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	if *fromID == "" || *toID == "" {
		a.errorf("--from and --to are required\n")
		return 2
	}
	fromIssue, err := parseIssueFlag(*fromIssueFlag, "from-issue")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	toIssue, err := parseIssueFlag(*toIssueFlag, "to-issue")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for link on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	fromArtifact, fromBody, err := findArtifactByID(ctx, client, repo, fromIssue, *fromID)
	if err != nil {
		a.errorf("%v\n", err)
		return 1
	}
	toArtifact, toBody, err := findArtifactByID(ctx, client, repo, toIssue, *toID)
	if err != nil {
		a.errorf("%v\n", err)
		return 1
	}

	newFromBody, fromChanged, err := model.AddRelatedCommentLink(fromBody, toArtifact.URL)
	if err != nil {
		a.errorf("update %s links: %v\n", *fromID, err)
		return 1
	}
	newToBody, toChanged, err := model.AddRelatedCommentLink(toBody, fromArtifact.URL)
	if err != nil {
		a.errorf("update %s links: %v\n", *toID, err)
		return 1
	}
	if fromChanged {
		if _, err := client.UpdateComment(ctx, repo, fromArtifact.CommentID, newFromBody); err != nil {
			a.errorf("patch %s: %v\n", *fromID, err)
			return 1
		}
	}
	if toChanged {
		if _, err := client.UpdateComment(ctx, repo, toArtifact.CommentID, newToBody); err != nil {
			a.errorf("patch %s: %v\n", *toID, err)
			return 1
		}
	}
	result := map[string]any{"ok": true, "from": *fromID, "from_url": fromArtifact.URL, "to": *toID, "to_url": toArtifact.URL, "from_changed": fromChanged, "to_changed": toChanged}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "linked %s <-> %s\n", *fromID, *toID)
	return 0
}
