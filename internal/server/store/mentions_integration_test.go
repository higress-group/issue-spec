package store

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMentionCandidatesAndFirstSeenPersistence(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "mention-org")
	repoID := insertRepo(t, pool, orgID, "mention-repo")
	requesterID := insertMentionUser(t, pool, "requester", "Requester", "", true)
	_ = insertMentionUser(t, pool, "ali", "Exact Login", "", true)
	aliceID := insertMentionUser(t, pool, "alice", "Alice", "alice@example.test", true)
	_ = insertMentionUser(t, pool, "zeta", "Alina Preferred", "", true)
	disabledID := insertMentionUser(t, pool, "alina", "Disabled Match", "", false)
	serviceID := insertMentionUser(t, pool, "alibot", "Service Match", "", true)
	if _, err := pool.Exec(t.Context(), `INSERT INTO service_accounts
		(id, user_id, organization_id, name, created_by_user_id)
		VALUES ($1,$2,$3,'mention-bot',$4)`, uuid.New(), serviceID, orgID, requesterID); err != nil {
		t.Fatal(err)
	}

	directory := New(pool)
	candidates, err := directory.MentionCandidates(t.Context(), requesterID, "ali", MaxMentionCandidates)
	if err != nil {
		t.Fatal(err)
	}
	logins := make([]string, len(candidates))
	for index, candidate := range candidates {
		logins[index] = candidate.Login
	}
	if want := []string{"ali", "alice", "zeta"}; !reflect.DeepEqual(logins, want) {
		t.Fatalf("candidate order = %v, want %v", logins, want)
	}
	if _, err := directory.MentionCandidates(t.Context(), serviceID, "ali", MaxMentionCandidates); !errors.Is(err, ErrMentionCallerIneligible) {
		t.Fatalf("service caller error = %v", err)
	}
	if _, err := directory.MentionCandidates(t.Context(), disabledID, "ali", MaxMentionCandidates); !errors.Is(err, ErrMentionCallerIneligible) {
		t.Fatalf("disabled caller error = %v", err)
	}

	repository := directory.Repo(orgID, repoID)
	issue, err := repository.CreateIssue(t.Context(), models.NewIssue{ID: uuid.New(), AuthorID: &requesterID,
		Title: "Mention persistence", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := repository.CreateComment(t.Context(), models.NewComment{ID: uuid.New(), IssueNumber: issue.Number,
		AuthorID: &requesterID, Body: "@alice @unknown @alibot @alina"})
	if err != nil {
		t.Fatal(err)
	}
	err = directory.WithinTx(t.Context(), func(tx *Tx) error {
		txRepo := tx.Repo(orgID, repoID)
		mentionContext, err := txRepo.MentionContext(t.Context(), comment.Comment.ID, requesterID)
		if err != nil {
			return err
		}
		if mentionContext.Organization != "mention-org" || mentionContext.Repository != "mention-repo" ||
			mentionContext.CompatibilityID <= 0 || mentionContext.IssueTitle != "Mention persistence" {
			t.Fatalf("mention context = %+v", mentionContext)
		}
		identities, err := txRepo.ResolveMentionIdentities(t.Context(), []string{"alice", "unknown", "alibot", "alina"})
		if err != nil {
			return err
		}
		if len(identities) != 1 || identities[0].UserID != aliceID || !identities[0].NotificationEligible {
			t.Fatalf("resolved identities = %+v", identities)
		}
		first, err := txRepo.SyncCommentMentions(t.Context(), MentionSyncInput{CommentID: comment.Comment.ID,
			IssueID: issue.ID, RepresentationVersion: 1, MentionedUserIDs: []uuid.UUID{aliceID}})
		if err != nil || len(first) != 1 || first[0] != aliceID {
			t.Fatalf("first sync = %v/%v", first, err)
		}
		removed, err := txRepo.SyncCommentMentions(t.Context(), MentionSyncInput{CommentID: comment.Comment.ID,
			IssueID: issue.ID, RepresentationVersion: 2, MentionedUserIDs: []uuid.UUID{}})
		if err != nil || len(removed) != 0 {
			t.Fatalf("remove sync = %v/%v", removed, err)
		}
		readded, err := txRepo.SyncCommentMentions(t.Context(), MentionSyncInput{CommentID: comment.Comment.ID,
			IssueID: issue.ID, RepresentationVersion: 3, MentionedUserIDs: []uuid.UUID{aliceID}})
		if err != nil || len(readded) != 0 {
			t.Fatalf("re-add sync = %v/%v", readded, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	var firstVersion, lastVersion int64
	var present bool
	if err := pool.QueryRow(t.Context(), `SELECT count(*), min(first_seen_representation_version),
		max(last_seen_representation_version), bool_and(present) FROM comment_mentions
		WHERE organization_id=$1 AND repository_id=$2 AND comment_id=$3 AND mentioned_user_id=$4`,
		orgID, repoID, comment.Comment.ID, aliceID).Scan(&count, &firstVersion, &lastVersion, &present); err != nil {
		t.Fatal(err)
	}
	if count != 1 || firstVersion != 1 || lastVersion != 3 || !present {
		t.Fatalf("persisted mention = count:%d versions:%d/%d present:%v", count, firstVersion, lastVersion, present)
	}
}

func insertMentionUser(t *testing.T, database *pgxpool.Pool, login, displayName, notificationEmail string, active bool) uuid.UUID {
	t.Helper()
	userID := uuid.New()
	status := "active"
	if !active {
		status = "disabled"
	}
	var email any
	var verified any
	if notificationEmail != "" {
		email, verified = notificationEmail, time.Now().UTC()
	}
	if _, err := database.Exec(t.Context(), `INSERT INTO users
		(id, login, display_name, status, notification_email, notification_email_verified_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, userID, login, displayName, status, email, verified); err != nil {
		t.Fatal(err)
	}
	return userID
}
