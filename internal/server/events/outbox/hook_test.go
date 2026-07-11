package outbox

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/github/issues"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestBuildEnvelopePreservesRawRevisionAndStableIdentity(t *testing.T) {
	orgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	repoID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	issueID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	commentID := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	actorID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	eventID := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	created := time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC)
	updated := created.Add(time.Minute)
	raw := "<!-- issue-spec:type=PROCESS id=PROCESS-007 version=1 -->\r\n命令  \n"
	hash := sha256.Sum256([]byte(raw))
	scope := models.RepoScope{OrgID: orgID, RepoID: repoID}
	snapshot := models.CommentSnapshot{Comment: models.Comment{ID: commentID, Scope: scope,
		IssueID: issueID, AuthorID: &actorID, Body: raw, RepresentationVersion: 2,
		CreatedAt: created, UpdatedAt: updated}, IssueNumber: 17, AuthorLogin: "worker"}
	envelope, aggregateID, err := BuildEnvelope(eventID, issues.MutationEvent{
		Type: "issue_comment.edited", Scope: scope,
		Issue: models.Issue{ID: issueID, Scope: scope, Number: 17}, Comment: &snapshot,
		RawBody: raw, BodyHash: hash, ActorUserID: actorID, RepresentationVersion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if aggregateID != commentID || envelope.SchemaVersion != 1 || envelope.EventID != eventID ||
		envelope.EventKey != "issue_comment.edited:44444444-4444-4444-4444-444444444444:v2" ||
		envelope.Action != "edited" || envelope.RawBody != raw ||
		envelope.BodyHash != "3beccaaac1f25f65a9221fabb6808835353d1ccba8f2e55ef36d3c69b5d1cd1f" ||
		envelope.Comment == nil || envelope.Comment.StableID != commentID ||
		envelope.Comment.RepresentationVersion != 2 || envelope.Author.Login != "worker" {
		t.Fatalf("envelope = %+v", envelope)
	}
	broken := hash
	broken[0] ^= 0xff
	if _, _, err := BuildEnvelope(eventID, issues.MutationEvent{
		Type: "issue_comment.edited", Scope: scope,
		Issue: models.Issue{ID: issueID, Scope: scope, Number: 17}, Comment: &snapshot,
		RawBody: raw, BodyHash: broken, ActorUserID: actorID, RepresentationVersion: 2,
	}); err == nil {
		t.Fatal("raw body/hash mismatch was accepted")
	}
}
