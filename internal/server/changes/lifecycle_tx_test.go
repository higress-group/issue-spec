package changes

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
)

func TestLifecycleSnapshotUsesCanonicalLifecycleValues(t *testing.T) {
	for _, lifecycle := range []Lifecycle{LifecycleActive, LifecycleBlocked, LifecycleClosed, LifecycleCompleted} {
		snapshot := LifecycleSnapshot{ChangeKey: "change", Lifecycle: lifecycle}
		if snapshot.ChangeKey == "" || snapshot.Lifecycle == "" {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	}
}

func TestLifecycleQueriesAreRepositoryAndChangeBounded(t *testing.T) {
	for _, expected := range []string{
		"repository_id = $2", "lower(btrim(change_key)) = $3",
		"lower(btrim(substring(candidate.body", "= $3",
	} {
		if !strings.Contains(lifecycleArtifactsQuery, expected) {
			t.Fatalf("artifact query missing %q", expected)
		}
	}
	if strings.Contains(lifecycleArtifactsQuery, "repository_id = ANY") {
		t.Fatal("artifact query accepts a repository-wide ID set")
	}
	for _, expected := range []string{"repository_id = $2", "issue_id = ANY($3::uuid[])"} {
		if !strings.Contains(lifecycleTypedArtifactsQuery, expected) {
			t.Fatalf("typed query missing %q", expected)
		}
	}
}

func TestLifecycleForIssueTxBoundsLargeRepositoryAndMatchesBoard(t *testing.T) {
	env := newChangesEnvironment(t)
	for index := 0; index < 128; index++ {
		env.addArtifact(t, env.scope, fmt.Sprintf("unrelated-%03d", index), StageProposal, "1",
			"issue-spec/proposal", "N/A", "N/A")
	}
	proposal := env.addArtifact(t, env.scope, "Target-Change", StageProposal, "1",
		"issue-spec/proposal", "N/A", "N/A")
	design := env.addArtifact(t, env.scope, "Target-Change", StageDesign, "1",
		"issue-spec/design", proposalURL(proposal), "N/A")
	implement := env.addArtifact(t, env.scope, "Target-Change", StageImplement, "1",
		"issue-spec/implement", proposalURL(proposal), proposalURL(design))
	// Preserve the board's duplicate-artifact compatibility semantics: the
	// active projected issue wins while the matching anomaly is still part of
	// the bounded change snapshot.
	env.addArtifact(t, env.scope, "Target-Change", StageDesign, "1",
		"issue-spec/design", proposalURL(proposal), "N/A")
	env.addTyped(t, env.scope, implement.ID, "VERIFY", "VERIFY-TARGET", "done",
		[]string{"https://code.example/acme/widgets/changes/42"})
	env.closeIssues(t, env.scope, proposal.ID, design.ID, implement.ID)
	repository := store.New(env.pool).ScopedRepo(env.scope)
	ordinary, err := repository.CreateIssue(t.Context(), models.NewIssue{ID: uuid.New(), Title: "ordinary", Body: "plain"})
	if err != nil {
		t.Fatal(err)
	}

	page, err := env.service.RepositoryBoard(t.Context(), authz.Authenticated(env.principal), env.scope, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := requireCard(t, page.Cards, "target-change").Lifecycle
	if want != LifecycleCompleted {
		t.Fatalf("board lifecycle = %s", want)
	}

	tx, err := env.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	artifacts, err := loadLifecycleArtifacts(t.Context(), tx, env.scope, "target-change")
	if err != nil || len(artifacts) != 4 {
		t.Fatalf("bounded artifacts = %d err=%v", len(artifacts), err)
	}
	issueIDs := make([]uuid.UUID, 0, len(artifacts))
	for _, artifact := range artifacts {
		issueIDs = append(issueIDs, artifact.issueID)
		if artifact.changeKey != "target-change" {
			t.Fatalf("unrelated artifact returned: %+v", artifact)
		}
	}
	typed, err := loadLifecycleTypedArtifacts(t.Context(), tx, env.scope, issueIDs)
	if err != nil || len(typed) != 1 || typed[0].key != "VERIFY-TARGET" {
		t.Fatalf("bounded typed artifacts = %+v err=%v", typed, err)
	}
	snapshot, err := LifecycleForIssueTx(t.Context(), tx, env.scope, implement.ID)
	if err != nil || snapshot.ChangeKey != "target-change" || snapshot.Lifecycle != want {
		t.Fatalf("transaction lifecycle = %+v want=%s err=%v", snapshot, want, err)
	}
	ordinarySnapshot, err := LifecycleForIssueTx(t.Context(), tx, env.scope, ordinary.ID)
	if err != nil || ordinarySnapshot != (LifecycleSnapshot{}) {
		t.Fatalf("ordinary lifecycle = %+v err=%v", ordinarySnapshot, err)
	}
}
