package change

import (
	"context"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
)

type locatorBackend struct {
	issues   map[int]github.Issue
	comments map[int][]github.Comment
}

func (b locatorBackend) GetIssue(_ context.Context, _ string, n int) (github.Issue, error) {
	return b.issues[n], nil
}
func (b locatorBackend) ListIssueComments(_ context.Context, _ string, n int) ([]github.Comment, error) {
	return b.comments[n], nil
}

func TestLocateFollowsCanonicalChangeChain(t *testing.T) {
	marker := func(kind string, n int) github.Issue {
		return github.Issue{Number: n, HTMLURL: "https://github.test/o/r/issues/" + string(rune('0'+n)), Body: "<!-- issue-spec:issue=" + kind + " change=change-x version=1 -->\n"}
	}
	b := locatorBackend{issues: map[int]github.Issue{1: marker("proposal", 1), 2: marker("design", 2), 3: marker("implement", 3)}, comments: map[int][]github.Comment{
		1: {{Body: "- Related Comments: https://github.test/o/r/issues/2#issuecomment-20"}},
		2: {{Body: "- Related Comments: https://github.test/o/r/issues/3#issuecomment-30"}},
	}}
	got, err := Locate(context.Background(), b, "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Change != "change-x" || got.Design.Number != 2 || got.Implement.Number != 3 {
		t.Fatalf("got=%+v", got)
	}
}

func TestLocateRejectsAmbiguousKind(t *testing.T) {
	body := func(kind string, n int) github.Issue {
		return github.Issue{Number: n, Body: "<!-- issue-spec:issue=" + kind + " change=x version=1 -->"}
	}
	b := locatorBackend{issues: map[int]github.Issue{1: body("proposal", 1), 2: body("design", 2), 4: body("design", 4)}, comments: map[int][]github.Comment{1: {{Body: "https://x/issues/2 https://x/issues/4"}}}}
	if _, err := Locate(context.Background(), b, "o/r", 1); err == nil {
		t.Fatal("expected ambiguity")
	}
}
