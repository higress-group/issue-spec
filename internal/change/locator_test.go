package change

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func TestLocateFromImplementFollowsExactReverseAuthority(t *testing.T) {
	issue := func(kind string, number int, predecessor string) github.Issue {
		body := fmt.Sprintf("<!-- issue-spec:issue=%s change=change-x version=1 -->\n", kind)
		if predecessor != "" {
			body += predecessor + "\n"
		}
		return github.Issue{Number: number, HTMLURL: fmt.Sprintf("https://github.test/o/r/issues/%d", number), Body: body}
	}
	backend := locatorBackend{issues: map[int]github.Issue{
		1: issue("proposal", 1, ""),
		2: issue("design", 2, "- Proposal Issue: https://github.test/o/r/issues/1"),
		3: issue("implement", 3, "- Design Issue: 2"),
	}}
	got, err := LocateFromImplement(t.Context(), backend, "o/r", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.Change != "change-x" || got.Proposal.Number != 1 || got.Design.Number != 2 || got.Implement.Number != 3 {
		t.Fatalf("located=%+v", got)
	}
}

func TestLocateFromImplementRejectsInvalidReverseAuthority(t *testing.T) {
	base := map[int]github.Issue{
		1: {Number: 1, Body: "<!-- issue-spec:issue=proposal change=change-x version=1 -->"},
		2: {Number: 2, Body: "<!-- issue-spec:issue=design change=change-x version=1 -->\n- Proposal Issue: 1"},
		3: {Number: 3, Body: "<!-- issue-spec:issue=implement change=change-x version=1 -->\n- Design Issue: 2"},
	}
	for _, test := range []struct {
		name string
		edit func(map[int]github.Issue)
		want string
	}{
		{name: "missing design reference", edit: func(v map[int]github.Issue) {
			item := v[3]
			item.Body = strings.ReplaceAll(item.Body, "- Design Issue: 2", "")
			v[3] = item
		}, want: "exactly once, got 0"},
		{name: "duplicate design reference", edit: func(v map[int]github.Issue) { item := v[3]; item.Body += "\n- Design Issue: 2"; v[3] = item }, want: "exactly once, got 2"},
		{name: "ambiguous proposal reference", edit: func(v map[int]github.Issue) {
			item := v[2]
			item.Body = strings.ReplaceAll(item.Body, "Proposal Issue: 1", "Proposal Issue: 1 4")
			v[2] = item
		}, want: "ambiguous"},
		{name: "comment URL reference", edit: func(v map[int]github.Issue) {
			item := v[2]
			item.Body = strings.ReplaceAll(item.Body, "Proposal Issue: 1", "Proposal Issue: https://github.test/o/r/issues/1#issuecomment-4")
			v[2] = item
		}, want: "not an exact issue number or URL"},
		{name: "duplicate marker", edit: func(v map[int]github.Issue) {
			item := v[3]
			item.Body += "\n<!-- issue-spec:issue=implement change=change-x version=1 -->"
			v[3] = item
		}, want: "exactly once, got 2"},
		{name: "wrong design marker", edit: func(v map[int]github.Issue) {
			item := v[2]
			item.Body = strings.ReplaceAll(item.Body, "issue=design", "issue=proposal")
			v[2] = item
		}, want: "marker is"},
		{name: "mismatched design change", edit: func(v map[int]github.Issue) {
			item := v[2]
			item.Body = strings.ReplaceAll(item.Body, "change=change-x", "change=change-y")
			v[2] = item
		}, want: "change change-x"},
		{name: "missing exact issue", edit: func(v map[int]github.Issue) { delete(v, 2) }, want: "returned issue 0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			issues := map[int]github.Issue{}
			for number, issue := range base {
				issues[number] = issue
			}
			test.edit(issues)
			backend := locatorBackend{issues: issues}
			if _, err := LocateFromImplement(t.Context(), backend, "o/r", 3); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v want=%q", err, test.want)
			}
		})
	}

	errorBackend := locatorBackendWithError{err: errors.New("provider failed")}
	if _, err := LocateFromImplement(t.Context(), errorBackend, "o/r", 3); err == nil || !strings.Contains(err.Error(), "provider failed") {
		t.Fatalf("provider error=%v", err)
	}
}

type locatorBackendWithError struct{ err error }

func (b locatorBackendWithError) GetIssue(context.Context, string, int) (github.Issue, error) {
	return github.Issue{}, b.err
}
func (b locatorBackendWithError) ListIssueComments(context.Context, string, int) ([]github.Comment, error) {
	return nil, b.err
}
