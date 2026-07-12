package change

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/github"
)

var (
	issueMarker = regexp.MustCompile(`(?m)^<!--\s*issue-spec:issue=(proposal|design|implement)\s+change=([^\s>]+)\s+version=([0-9]+)\s*-->$`)
	issueURL    = regexp.MustCompile(`/issues/([0-9]+)(?:#issuecomment-[0-9]+)?`)
)

type Backend interface {
	GetIssue(context.Context, string, int) (github.Issue, error)
	ListIssueComments(context.Context, string, int) ([]github.Comment, error)
}

type IssueRef struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

type Located struct {
	Change    string   `json:"change"`
	Proposal  IssueRef `json:"proposal"`
	Design    IssueRef `json:"design"`
	Implement IssueRef `json:"implement"`
}

// Locate follows canonical issue/comment URLs from a proposal and verifies the
// marker kind and change key of every peer. It never guesses from titles.
func Locate(ctx context.Context, backend Backend, repo string, proposal int) (Located, error) {
	root, err := backend.GetIssue(ctx, repo, proposal)
	if err != nil {
		return Located{}, fmt.Errorf("read proposal: %w", err)
	}
	kind, key, err := parseMarker(root.Body)
	if err != nil || kind != "proposal" {
		return Located{}, fmt.Errorf("issue %d is not a canonical proposal: %w", proposal, err)
	}
	result := Located{Change: key, Proposal: ref(root)}
	seen := map[int]bool{proposal: true}
	queue := []int{proposal}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		comments, err := backend.ListIssueComments(ctx, repo, current)
		if err != nil {
			return Located{}, fmt.Errorf("list issue %d comments: %w", current, err)
		}
		var numbers []int
		for _, comment := range comments {
			for _, match := range issueURL.FindAllStringSubmatch(comment.Body, -1) {
				n, _ := strconv.Atoi(match[1])
				if n > 0 && !seen[n] {
					numbers = append(numbers, n)
					seen[n] = true
				}
			}
		}
		sort.Ints(numbers)
		for _, number := range numbers {
			peer, err := backend.GetIssue(ctx, repo, number)
			if err != nil {
				continue // a cross-repository URL is not part of this change chain
			}
			peerKind, peerKey, markerErr := parseMarker(peer.Body)
			if markerErr != nil || peerKey != key {
				continue
			}
			switch peerKind {
			case "design":
				if result.Design.Number != 0 && result.Design.Number != number {
					return Located{}, fmt.Errorf("ambiguous design issues %d and %d for change %s", result.Design.Number, number, key)
				}
				result.Design = ref(peer)
				queue = append(queue, number)
			case "implement":
				if result.Implement.Number != 0 && result.Implement.Number != number {
					return Located{}, fmt.Errorf("ambiguous implement issues %d and %d for change %s", result.Implement.Number, number, key)
				}
				result.Implement = ref(peer)
			}
		}
	}
	if result.Design.Number == 0 || result.Implement.Number == 0 {
		return Located{}, fmt.Errorf("incomplete change %s: design=%d implement=%d", key, result.Design.Number, result.Implement.Number)
	}
	return result, nil
}

func parseMarker(body string) (string, string, error) {
	match := issueMarker.FindStringSubmatch(body)
	if len(match) == 0 {
		return "", "", fmt.Errorf("canonical issue marker missing")
	}
	if strings.TrimSpace(match[2]) == "" || match[3] != "1" {
		return "", "", fmt.Errorf("unsupported issue marker")
	}
	return match[1], match[2], nil
}

func ref(issue github.Issue) IssueRef { return IssueRef{Number: issue.Number, URL: issue.HTMLURL} }
