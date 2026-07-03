package jobs

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

const defaultArtifactIssueLimit = 8

var (
	issueURLReferenceRe  = regexp.MustCompile(`(?i)https?://[^\s)]+/issues/([1-9][0-9]*)`)
	issueHashReferenceRe = regexp.MustCompile(`(?:^|[\s(\[])#([1-9][0-9]*)\b`)
)

type IssueSpecArtifactProvider struct {
	GitHub    github.RunnerOperations
	MaxIssues int
}

func (p *IssueSpecArtifactProvider) ArtifactsForJob(ctx context.Context, job state.Job) ([]model.Artifact, error) {
	if p == nil || p.GitHub == nil {
		return nil, fmt.Errorf("issue-spec artifact provider GitHub backend is required")
	}
	repo := strings.TrimSpace(job.Repo)
	if repo == "" {
		return nil, fmt.Errorf("job repo is required for context artifacts")
	}
	if job.IssueNumber <= 0 {
		return nil, fmt.Errorf("job issue number is required for context artifacts")
	}

	maxIssues := p.MaxIssues
	if maxIssues <= 0 {
		maxIssues = defaultArtifactIssueLimit
	}
	queue := []int{job.IssueNumber}
	queued := map[int]bool{job.IssueNumber: true}
	visited := map[int]bool{}
	seenArtifacts := map[string]bool{}
	var artifacts []model.Artifact

	enqueue := func(issue int) {
		if issue <= 0 || queued[issue] || len(queued) >= maxIssues {
			return
		}
		queued[issue] = true
		queue = append(queue, issue)
	}
	addArtifact := func(artifact model.Artifact) {
		key := artifactKey(artifact)
		if seenArtifacts[key] {
			return
		}
		seenArtifacts[key] = true
		artifacts = append(artifacts, artifact)
	}

	for len(queue) > 0 {
		issueNumber := queue[0]
		queue = queue[1:]
		if visited[issueNumber] {
			continue
		}
		visited[issueNumber] = true

		issueResult, err := p.GitHub.GetIssueContext(ctx, repo, issueNumber, github.ConditionalRequest{})
		if err != nil {
			return nil, fmt.Errorf("load issue context %s#%d: %w", repo, issueNumber, err)
		}
		issue := issueResult.Issue
		if issue.Number == 0 {
			issue.Number = issueNumber
		}
		addArtifact(issueContextArtifact(issue))
		for _, ref := range issueReferencesFromText(issue.Body) {
			enqueue(ref)
		}

		comments, err := p.issueComments(ctx, repo, issueNumber)
		if err != nil {
			return nil, err
		}
		for _, comment := range comments {
			tc := model.ParseTypedComment(comment.Body)
			if strings.TrimSpace(tc.Type) == "" || strings.TrimSpace(tc.ID) == "" {
				continue
			}
			if comment.IssueNumber == 0 {
				comment.IssueNumber = issueNumber
			}
			addArtifact(model.Artifact{
				Issue:     comment.IssueNumber,
				CommentID: comment.ID,
				URL:       comment.HTMLURL,
				APIURL:    comment.URL,
				Comment:   tc,
			})
			for _, ref := range issueReferencesFromTypedComment(tc) {
				enqueue(ref)
			}
		}
	}

	sort.SliceStable(artifacts, func(i, j int) bool {
		return artifactKey(artifacts[i]) < artifactKey(artifacts[j])
	})
	return artifacts, nil
}

func (p *IssueSpecArtifactProvider) issueComments(ctx context.Context, repo string, issueNumber int) ([]github.Comment, error) {
	var all []github.Comment
	page := github.RunnerPageOptions{}
	for {
		result, err := p.GitHub.ListIssueCommentsPage(ctx, repo, issueNumber, github.CommentListOptions{Page: page})
		if err != nil {
			return nil, fmt.Errorf("load issue comments %s#%d: %w", repo, issueNumber, err)
		}
		all = append(all, result.Comments...)
		if result.Metadata.Pagination.NextURL == "" {
			return all, nil
		}
		page = github.RunnerPageOptions{CursorURL: result.Metadata.Pagination.NextURL}
	}
}

func issueContextArtifact(issue github.Issue) model.Artifact {
	number := issue.Number
	body := strings.TrimSpace(issue.Body)
	content := fmt.Sprintf("Issue: #%d\nTitle: %s\nState: %s\nURL: %s", number, issue.Title, issue.State, issue.HTMLURL)
	if body != "" {
		content += "\n\n" + body
	}
	return model.Artifact{
		Issue:  number,
		URL:    issue.HTMLURL,
		APIURL: issue.URL,
		Comment: model.TypedComment{
			Type:   "ISSUE_CONTEXT",
			ID:     fmt.Sprintf("ISSUE-%03d", number),
			Status: issue.State,
			Scope:  "issue-context",
			Body:   content,
		},
	}
}

func issueReferencesFromTypedComment(tc model.TypedComment) []int {
	var refs []int
	for _, linkName := range []string{"Proposal Issue", "Design Issue", "Implement Issue", "Related Comments"} {
		for _, value := range model.LinkValues(tc, linkName) {
			if issue, ok := issueNumberFromReference(value); ok {
				refs = append(refs, issue)
			}
		}
	}
	refs = append(refs, issueReferencesFromText(tc.Body)...)
	return uniqueIssueNumbers(refs)
}

func issueReferencesFromText(text string) []int {
	var refs []int
	for _, match := range issueURLReferenceRe.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		if issue, err := strconv.Atoi(match[1]); err == nil {
			refs = append(refs, issue)
		}
	}
	for _, match := range issueHashReferenceRe.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		if issue, err := strconv.Atoi(match[1]); err == nil {
			refs = append(refs, issue)
		}
	}
	return uniqueIssueNumbers(refs)
}

func issueNumberFromReference(raw string) (int, bool) {
	value := strings.Trim(strings.TrimSpace(raw), ".,;)")
	if strings.HasPrefix(value, "#") {
		n, err := strconv.Atoi(strings.TrimPrefix(value, "#"))
		return n, err == nil && n > 0
	}
	n, err := github.ParseIssueNumber(value)
	return n, err == nil && n > 0
}

func uniqueIssueNumbers(values []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, value := range values {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func artifactKey(artifact model.Artifact) string {
	return fmt.Sprintf("%09d/%012d/%s/%s/%s", artifact.Issue, artifact.CommentID, artifact.Comment.Type, artifact.Comment.ID, artifact.URL)
}
