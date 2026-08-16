package change

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

var (
	issueMarker   = regexp.MustCompile(`(?m)^<!--\s*issue-spec:issue=(proposal|design|implement)\s+change=([^\s>]+)\s+version=([0-9]+)\s*-->$`)
	issueURL      = regexp.MustCompile(`/issues/([0-9]+)(?:#issuecomment-[0-9]+)?`)
	designIssue   = regexp.MustCompile(`(?m)^[ \t]*-[ \t]+Design Issue:[ \t]*(.+?)[ \t]*$`)
	proposalIssue = regexp.MustCompile(`(?m)^[ \t]*-[ \t]+Proposal Issue:[ \t]*(.+?)[ \t]*$`)
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

// LocateFromImplement derives the canonical change root only from the direct
// predecessor metadata stored in the current implementation and design issue
// bodies. It never searches forward from a caller-selected proposal.
func LocateFromImplement(ctx context.Context, backend Backend, repo string, implement int) (Located, error) {
	if implement <= 0 {
		return Located{}, fmt.Errorf("implement issue must be positive")
	}
	implementation, err := exactIssue(ctx, backend, repo, implement, "implement")
	if err != nil {
		return Located{}, err
	}
	kind, key, err := parseMarker(implementation.Body)
	if err != nil || kind != "implement" {
		return Located{}, markerMismatch(implement, "implement", kind, err)
	}
	designNumber, err := exactPredecessor(implementation.Body, designIssue, "Design Issue")
	if err != nil {
		return Located{}, fmt.Errorf("implement issue %d: %w", implement, err)
	}
	if designNumber == implement {
		return Located{}, fmt.Errorf("implement issue %d references itself as Design Issue", implement)
	}
	design, err := exactIssue(ctx, backend, repo, designNumber, "design")
	if err != nil {
		return Located{}, err
	}
	designKind, designKey, err := parseMarker(design.Body)
	if err != nil || designKind != "design" || designKey != key {
		return Located{}, markerMismatch(designNumber, "design for change "+key, designKind+" for change "+designKey, err)
	}
	proposalNumber, err := exactPredecessor(design.Body, proposalIssue, "Proposal Issue")
	if err != nil {
		return Located{}, fmt.Errorf("design issue %d: %w", designNumber, err)
	}
	if proposalNumber == designNumber || proposalNumber == implement {
		return Located{}, fmt.Errorf("design issue %d has cyclic Proposal Issue %d", designNumber, proposalNumber)
	}
	proposal, err := exactIssue(ctx, backend, repo, proposalNumber, "proposal")
	if err != nil {
		return Located{}, err
	}
	proposalKind, proposalKey, err := parseMarker(proposal.Body)
	if err != nil || proposalKind != "proposal" || proposalKey != key {
		return Located{}, markerMismatch(proposalNumber, "proposal for change "+key, proposalKind+" for change "+proposalKey, err)
	}
	return Located{Change: key, Proposal: ref(proposal), Design: ref(design), Implement: ref(implementation)}, nil
}

// Locate follows canonical issue/comment URLs from a proposal and verifies the
// marker kind and change key of every peer. It never guesses from titles.
func Locate(ctx context.Context, backend Backend, repo string, proposal int) (Located, error) {
	root, err := backend.GetIssue(ctx, repo, proposal)
	if err != nil {
		return Located{}, fmt.Errorf("read proposal: %w", err)
	}
	kind, key, err := parseMarker(root.Body)
	if err != nil {
		return Located{}, fmt.Errorf("issue %d is not a canonical proposal: %w", proposal, err)
	}
	if kind != "proposal" {
		return Located{}, fmt.Errorf("issue %d marker is %q, want proposal", proposal, kind)
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
	matches := issueMarker.FindAllStringSubmatch(model.CanonicalView(body), -1)
	if len(matches) == 0 {
		return "", "", fmt.Errorf("canonical issue marker missing")
	}
	if len(matches) != 1 {
		return "", "", fmt.Errorf("canonical issue marker must appear exactly once, got %d", len(matches))
	}
	match := matches[0]
	if strings.TrimSpace(match[2]) == "" || match[3] != "1" {
		return "", "", fmt.Errorf("unsupported issue marker")
	}
	return match[1], match[2], nil
}

func exactIssue(ctx context.Context, backend Backend, repo string, number int, kind string) (github.Issue, error) {
	issue, err := backend.GetIssue(ctx, repo, number)
	if err != nil {
		return github.Issue{}, fmt.Errorf("read %s issue %d: %w", kind, number, err)
	}
	if issue.Number != number {
		return github.Issue{}, fmt.Errorf("read %s issue %d returned issue %d", kind, number, issue.Number)
	}
	return issue, nil
}

func exactPredecessor(body string, pattern *regexp.Regexp, label string) (int, error) {
	matches := pattern.FindAllStringSubmatch(model.CanonicalView(body), -1)
	if len(matches) != 1 {
		return 0, fmt.Errorf("canonical %s reference must appear exactly once, got %d", label, len(matches))
	}
	value := strings.TrimSpace(matches[0][1])
	if len(strings.Fields(value)) != 1 {
		return 0, fmt.Errorf("canonical %s reference %q is ambiguous", label, value)
	}
	if strings.HasPrefix(value, "#") {
		value = strings.TrimPrefix(value, "#")
	}
	if number, err := strconv.Atoi(value); err == nil && number > 0 {
		return number, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return 0, fmt.Errorf("canonical %s reference %q is not an exact issue number or URL", label, value)
	}
	number, err := github.ParseIssueNumber(value)
	if err != nil {
		return 0, fmt.Errorf("canonical %s reference %q: %w", label, value, err)
	}
	return number, nil
}

func markerMismatch(number int, want, got string, err error) error {
	if err != nil {
		return fmt.Errorf("issue %d is not canonical %s authority: %w", number, want, err)
	}
	return fmt.Errorf("issue %d marker is %q, want %q", number, got, want)
}

func ref(issue github.Issue) IssueRef { return IssueRef{Number: issue.Number, URL: issue.HTMLURL} }
