package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/relationships"
)

type linkStrictBackend struct {
	github.IssueBackend
	comments               map[int][]github.Comment
	versions               map[int64]int64
	writes                 map[int64]int
	lostResponse           bool
	driftBeforeConditional bool
	driftOnOwnerList       int
	ownerLists             int
}

func (b *linkStrictBackend) ListIssueComments(_ context.Context, _ string, issue int) ([]github.Comment, error) {
	if issue == 3 {
		b.ownerLists++
		if b.driftOnOwnerList > 0 && b.ownerLists == b.driftOnOwnerList {
			b.comments[3][0].Body = strings.Replace(b.comments[3][0].Body, "Scope: test", "Scope: unrelated-drift", 1)
		}
	}
	return append([]github.Comment(nil), b.comments[issue]...), nil
}

func (b *linkStrictBackend) UpdateComment(_ context.Context, _ string, id int64, body string) (github.Comment, error) {
	return b.update(id, body)
}

func (b *linkStrictBackend) GetCommentRepresentation(_ context.Context, _ string, id int64) (github.CommentRepresentation, error) {
	comment, ok := b.byID(id)
	if !ok {
		return github.CommentRepresentation{}, errors.New("not found")
	}
	return github.CommentRepresentation{Comment: comment, RepresentationVersion: b.versions[id],
		Guarantee: github.CommentMutationStrictConditional}, nil
}

func (b *linkStrictBackend) UpdateCommentConditional(_ context.Context, _ string, id, expected int64, body string) (github.CommentRepresentation, error) {
	if b.driftBeforeConditional {
		b.driftBeforeConditional = false
		for issue, comments := range b.comments {
			for index := range comments {
				if comments[index].ID == id {
					b.comments[issue][index].Body = strings.Replace(comments[index].Body, "Scope: test", "Scope: concurrent-drift", 1)
					b.versions[id]++
				}
			}
		}
	}
	if b.versions[id] != expected {
		return github.CommentRepresentation{}, &github.CommentMutationConflictError{Expected: expected, Current: b.versions[id]}
	}
	comment, err := b.update(id, body)
	if err != nil {
		return github.CommentRepresentation{}, err
	}
	result := github.CommentRepresentation{Comment: comment, RepresentationVersion: b.versions[id],
		Guarantee: github.CommentMutationStrictConditional}
	if b.lostResponse {
		b.lostResponse = false
		return github.CommentRepresentation{}, errors.New("transport response lost")
	}
	return result, nil
}

func (b *linkStrictBackend) update(id int64, body string) (github.Comment, error) {
	for issue, comments := range b.comments {
		for index := range comments {
			if comments[index].ID != id {
				continue
			}
			comments[index].Body = body
			b.comments[issue] = comments
			b.versions[id]++
			b.writes[id]++
			return comments[index], nil
		}
	}
	return github.Comment{}, errors.New("not found")
}

func (b *linkStrictBackend) byID(id int64) (github.Comment, bool) {
	for _, comments := range b.comments {
		for _, comment := range comments {
			if comment.ID == id {
				return comment, true
			}
		}
	}
	return github.Comment{}, false
}

func newLinkBackend() *linkStrictBackend {
	ownerBody := linkBody("TASK", "TASK-001", "## Task\n\n### Covers\n\n- SPEC-001")
	targetBody := linkBody("SPEC", "SPEC-001", "## Specification\n\npeer bytes")
	return &linkStrictBackend{comments: map[int][]github.Comment{
		3: {{ID: 31, HTMLURL: "https://example.test/issues/3#issuecomment-31", URL: "https://api.example.test/issues/3/comments/31", Body: ownerBody}},
		1: {{ID: 11, HTMLURL: "https://example.test/issues/1#issuecomment-11", URL: "https://api.example.test/issues/1/comments/11", Body: targetBody}},
	}, versions: map[int64]int64{31: 1, 11: 1}, writes: map[int64]int{}}
}

func linkBody(artifactType, id, semantic string) string {
	return fmt.Sprintf("<!-- issue-spec:type=%s id=%s version=1 -->\nAgent: Worker\nType: %s\nID: %s\nStatus: confirmed\nScope: test\nLinks:\n- Proposal Issue: N/A\n- Design Issue: N/A\n- Implement Issue: N/A\n- Related Comments: N/A\n- PR: N/A\n\n%s\n", artifactType, id, artifactType, id, semantic)
}

func TestExecuteOwnerLinkNormalizesPairWritesOnlyOwnerAndRecoversLostResponse(t *testing.T) {
	backend := newLinkBackend()
	backend.lostResponse = true
	peerBefore := backend.comments[1][0].Body
	request := linkTargets{Version: 1, Owner: linkTarget{Issue: 1, ID: "SPEC-001"},
		Add: []linkTarget{{Issue: 3, ID: "TASK-001"}}}
	result, err := executeOwnerLink(context.Background(), backend, "o/r", request, true, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != relationships.MutationVersion || result.Kind != relationships.TaskCoversSpec ||
		result.Owner.ID != "TASK-001" || len(result.Add) != 1 || result.Add[0].ID != "SPEC-001" ||
		result.Action != "updated" || result.ReverseWrites != 0 || !result.Atomic ||
		result.Guarantee != github.CommentMutationStrictConditional || backend.writes[31] != 1 || backend.writes[11] != 0 {
		t.Fatalf("result=%+v writes=%+v", result, backend.writes)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version":1`) ||
		!strings.Contains(string(raw), `"kind":"task-covers-spec"`) {
		t.Fatalf("pair JSON omitted versioned registry kind: %s", raw)
	}
	var human strings.Builder
	writeLinkResult(&human, result)
	if !strings.Contains(human.String(), "task-covers-spec TASK-001 -> SPEC-001") {
		t.Fatalf("pair human output lost canonical orientation: %q", human.String())
	}
	if backend.comments[1][0].Body != peerBefore {
		t.Fatal("peer representation was changed")
	}
	second, err := executeOwnerLink(context.Background(), backend, "o/r", request, true, false, "")
	if err != nil || second.Action != "unchanged" || second.Kind != relationships.TaskCoversSpec || backend.writes[31] != 1 {
		t.Fatalf("idempotent result=%+v writes=%+v err=%v", second, backend.writes, err)
	}
}

func TestExecuteOwnerLinkUnsupportedPairFailsBeforeMutation(t *testing.T) {
	backend := newLinkBackend()
	backend.comments[3][0].Body = strings.Replace(backend.comments[3][0].Body, "- SPEC-001", "- SPEC-002", 1)
	request := linkTargets{Version: 1, Owner: linkTarget{Issue: 1, ID: "SPEC-001"},
		Add: []linkTarget{{Issue: 3, ID: "TASK-001"}}}
	if _, err := executeOwnerLink(context.Background(), backend, "o/r", request, true, false, ""); !errors.Is(err, relationships.ErrUnsupported) {
		t.Fatalf("unsupported pair error=%v", err)
	}
	if backend.writes[31] != 0 || backend.writes[11] != 0 {
		t.Fatalf("unsupported pair writes=%+v", backend.writes)
	}
}

func TestExecuteOwnerLinkAmbiguousPairFailsBeforeMutation(t *testing.T) {
	firstURL := "https://example.test/issues/3#issuecomment-31"
	secondURL := "https://example.test/issues/3#issuecomment-32"
	firstBody := linkBody("PROCESS", "PROCESS-001", "## Process\n\n### Dependencies\n\n- PROCESS-002")
	var err error
	firstBody, _, err = model.StampSupersededBy(firstBody, "PROCESS-001",
		model.SupersededBy{ProcessID: "PROCESS-002", URL: secondURL})
	if err != nil {
		t.Fatal(err)
	}
	backend := &linkStrictBackend{comments: map[int][]github.Comment{3: {
		{ID: 31, HTMLURL: firstURL, URL: "https://api.example.test/issues/3/comments/31", Body: firstBody},
		{ID: 32, HTMLURL: secondURL, URL: "https://api.example.test/issues/3/comments/32",
			Body: linkBody("PROCESS", "PROCESS-002", "## Process\n\n### Dependencies\n\n- N/A")},
	}}, versions: map[int64]int64{31: 1, 32: 1}, writes: map[int64]int{}}
	request := linkTargets{Version: 1, Owner: linkTarget{Issue: 3, ID: "PROCESS-001"},
		Add: []linkTarget{{Issue: 3, ID: "PROCESS-002"}}}
	if _, err := executeOwnerLink(context.Background(), backend, "o/r", request, true, false, ""); !errors.Is(err, relationships.ErrAmbiguous) {
		t.Fatalf("ambiguous pair error=%v", err)
	}
	if backend.writes[31] != 0 || backend.writes[32] != 0 {
		t.Fatalf("ambiguous pair writes=%+v", backend.writes)
	}
}

func TestExecuteOwnerLinkStaleConditionalAndNonAtomicDriftWriteNothing(t *testing.T) {
	t.Run("conditional", func(t *testing.T) {
		backend := newLinkBackend()
		backend.driftBeforeConditional = true
		request := linkTargets{Version: 1, Owner: linkTarget{Issue: 3, Type: "TASK", ID: "TASK-001"},
			Add: []linkTarget{{Issue: 1, Type: "SPEC", ID: "SPEC-001"}}}
		if _, err := executeOwnerLink(context.Background(), backend, "o/r", request, false, false, ""); err == nil {
			t.Fatal("stale conditional update succeeded")
		}
		if backend.writes[31] != 0 || backend.writes[11] != 0 {
			t.Fatalf("writes=%+v", backend.writes)
		}
	})

	t.Run("non-atomic", func(t *testing.T) {
		strict := newLinkBackend()
		strict.driftOnOwnerList = 2
		plain := struct{ github.IssueBackend }{IssueBackend: strict}
		request := linkTargets{Version: 1, Owner: linkTarget{Issue: 3, Type: "TASK", ID: "TASK-001"},
			Add: []linkTarget{{Issue: 1, Type: "SPEC", ID: "SPEC-001"}}}
		digest := model.RepresentationDigest(strict.comments[3][0].Body)
		if _, err := executeOwnerLink(context.Background(), plain, "o/r", request, false, true, digest); err == nil {
			t.Fatal("non-atomic drift update succeeded")
		}
		if strict.writes[31] != 0 || strict.writes[11] != 0 {
			t.Fatalf("writes=%+v", strict.writes)
		}
	})
}
