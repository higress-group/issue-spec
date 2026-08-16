package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestExecuteOwnerLinkToleratesBotSuffixAfterStrictWrite(t *testing.T) {
	backend := newLinkBackend()
	backend.suffixAfterWrite = true
	request := linkTargets{Version: 1, Owner: linkTarget{Issue: 3, Type: "TASK", ID: "TASK-001"},
		Add: []linkTarget{{Issue: 1, Type: "SPEC", ID: "SPEC-001"}}}
	result, err := executeOwnerLink(context.Background(), backend, "o/r", request, true, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "updated" || backend.writes[31] != 1 {
		t.Fatalf("result=%+v writes=%+v", result, backend.writes)
	}
	if !strings.Contains(backend.comments[3][0].Body, linkTranslationDivider) {
		t.Fatal("provider suffix must survive the mutation")
	}
	second, err := executeOwnerLink(context.Background(), backend, "o/r", request, true, false, "")
	if err != nil || second.Action != "unchanged" || backend.writes[31] != 1 {
		t.Fatalf("idempotent re-run with suffix result=%+v writes=%+v err=%v", second, backend.writes, err)
	}
}

func TestExecuteOwnerLinkNonAtomicPassesSuffixOnlyDrift(t *testing.T) {
	strict := newLinkBackend()
	strict.suffixOnOwnerList = 2
	plain := struct{ github.IssueBackend }{IssueBackend: strict}
	request := linkTargets{Version: 1, Owner: linkTarget{Issue: 3, Type: "TASK", ID: "TASK-001"},
		Add: []linkTarget{{Issue: 1, Type: "SPEC", ID: "SPEC-001"}}}
	digest := model.RepresentationDigest(strict.comments[3][0].Body)
	result, err := executeOwnerLink(context.Background(), plain, "o/r", request, false, true, digest)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "updated" || strict.writes[31] != 1 {
		t.Fatalf("result=%+v writes=%+v", result, strict.writes)
	}
}
