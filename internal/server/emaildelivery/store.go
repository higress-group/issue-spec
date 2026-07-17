package emaildelivery

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const deliveryColumns = `id, kind, idempotency_key, recipient_user_id,
	organization_id, repository_id, verification_request_id, comment_id, issue_id, milestone_id,
	render_snapshot, state, attempts, next_attempt_at, lease_expires_at, delivered_at,
	last_reason, representation_version, created_at, updated_at`

type Store struct{ db store.DBTX }

func NewStore(db store.DBTX) (*Store, error) {
	if db == nil {
		return nil, errors.New("email delivery: database is required")
	}
	return &Store{db: db}, nil
}

// Enqueue inserts one logical delivery through the supplied transaction or
// pool. Callers performing user/comment/issue mutations must pass their pgx
// transaction so the logical work commits or rolls back with that mutation.
func (s *Store) Enqueue(ctx context.Context, input EnqueueInput) (Delivery, bool, error) {
	canonical, err := input.validate()
	if err != nil {
		return Delivery{}, false, err
	}
	id, err := StableDeliveryID(input.Kind, input.IdempotencyKey)
	if err != nil {
		return Delivery{}, false, err
	}
	row := s.db.QueryRow(ctx, `INSERT INTO email_deliveries (
		id, kind, idempotency_key, recipient_user_id, organization_id, repository_id,
		verification_request_id, comment_id, issue_id, milestone_id, render_snapshot, next_attempt_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
			COALESCE($12::timestamptz, clock_timestamp()))
		ON CONFLICT (kind, idempotency_key) DO NOTHING
		RETURNING `+deliveryColumns, id, input.Kind, stringsTrim(input.IdempotencyKey), input.RecipientUserID,
		input.OrganizationID, input.RepositoryID, input.VerificationRequestID, input.CommentID,
		input.IssueID, input.MilestoneID, canonical, input.AvailableAt)
	delivery, scanErr := scanDelivery(row)
	if scanErr == nil {
		return delivery, true, nil
	}
	if !errors.Is(scanErr, pgx.ErrNoRows) {
		return Delivery{}, false, safeStoreError(scanErr)
	}
	existing, err := s.Get(ctx, id)
	if err != nil {
		return Delivery{}, false, err
	}
	if !sameLogicalDelivery(existing, input, canonical) {
		return Delivery{}, false, ErrConflict
	}
	return existing, false, nil
}

func stringsTrim(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t' || value[start] == '\r' || value[start] == '\n') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t' || value[end-1] == '\r' || value[end-1] == '\n') {
		end--
	}
	return value[start:end]
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (Delivery, error) {
	if id == uuid.Nil {
		return Delivery{}, ErrInvalid
	}
	delivery, err := scanDelivery(s.db.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM email_deliveries WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNoWork
	}
	if err != nil {
		return Delivery{}, safeStoreError(err)
	}
	return delivery, nil
}

func (s *Store) ClaimOne(ctx context.Context, now time.Time, lease time.Duration) (*Claim, error) {
	if now.IsZero() || lease <= 0 {
		return nil, ErrInvalid
	}
	// A worker which vanished after claiming its fifth attempt leaves no safe
	// retry budget. Converge that expired lease before selecting more work.
	if _, err := s.db.Exec(ctx, `UPDATE email_deliveries
		SET state = 'failed', lease_expires_at = NULL, last_reason = $2,
			representation_version = representation_version + 1, updated_at = $1
		WHERE state = 'delivering' AND lease_expires_at <= $1 AND attempts >= $3`,
		now, ReasonSMTPAmbiguous, MaxAttempts); err != nil {
		return nil, safeStoreError(err)
	}
	row := s.db.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM email_deliveries
		WHERE attempts < $3 AND (
			(state = 'pending' AND next_attempt_at <= $1) OR
			(state = 'delivering' AND lease_expires_at <= $1)
		)
		ORDER BY CASE WHEN state = 'delivering' THEN lease_expires_at ELSE next_attempt_at END,
			created_at, id
		FOR UPDATE SKIP LOCKED LIMIT 1
	)
	UPDATE email_deliveries delivery
	SET state = 'delivering', attempts = attempts + 1, lease_expires_at = $2,
		last_reason = NULL, representation_version = representation_version + 1, updated_at = $1
	FROM candidate WHERE delivery.id = candidate.id
	RETURNING `+prefixedColumns("delivery"), now, now.Add(lease), MaxAttempts)
	delivery, err := scanDelivery(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, safeStoreError(err)
	}
	return &Claim{Delivery: delivery, LeaseVersion: delivery.RepresentationVersion}, nil
}

func prefixedColumns(prefix string) string {
	return prefix + `.id, ` + prefix + `.kind, ` + prefix + `.idempotency_key, ` + prefix + `.recipient_user_id,
		` + prefix + `.organization_id, ` + prefix + `.repository_id, ` + prefix + `.verification_request_id,
		` + prefix + `.comment_id, ` + prefix + `.issue_id, ` + prefix + `.milestone_id, ` + prefix + `.render_snapshot,
		` + prefix + `.state, ` + prefix + `.attempts, ` + prefix + `.next_attempt_at, ` + prefix + `.lease_expires_at,
		` + prefix + `.delivered_at, ` + prefix + `.last_reason, ` + prefix + `.representation_version,
		` + prefix + `.created_at, ` + prefix + `.updated_at`
}

func (s *Store) Succeed(ctx context.Context, claim *Claim, completed time.Time) error {
	if !validFinalize(claim, completed) {
		return ErrInvalid
	}
	var count int
	err := s.db.QueryRow(ctx, `WITH finalized AS (
		UPDATE email_deliveries SET state = 'succeeded', lease_expires_at = NULL,
			delivered_at = $1, last_reason = NULL,
			representation_version = representation_version + 1, updated_at = $1
		WHERE id = $2 AND state = 'delivering' AND representation_version = $3
		RETURNING verification_request_id
	), cleared AS (
		UPDATE email_verification_requests SET token_ciphertext = NULL,
			sent_at = COALESCE(sent_at, $1), representation_version = representation_version + 1,
			updated_at = $1
		WHERE id IN (SELECT verification_request_id FROM finalized WHERE verification_request_id IS NOT NULL)
		RETURNING id
	) SELECT count(*) FROM finalized`, completed, claim.ID, claim.LeaseVersion).Scan(&count)
	if err != nil {
		return safeStoreError(err)
	}
	if count != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *Store) Retry(ctx context.Context, claim *Claim, next, completed time.Time, reason ReasonCode) error {
	if !validFinalize(claim, completed) || next.Before(completed) || !reason.Valid() || claim.Attempts >= MaxAttempts {
		return ErrInvalid
	}
	return s.finalize(ctx, claim, StatePending, next, completed, nil, reason)
}

func (s *Store) Fail(ctx context.Context, claim *Claim, completed time.Time, reason ReasonCode) error {
	if !validFinalize(claim, completed) || !reason.Valid() {
		return ErrInvalid
	}
	return s.finalize(ctx, claim, StateFailed, completed, completed, nil, reason)
}

func (s *Store) Suppress(ctx context.Context, claim *Claim, completed time.Time, reason ReasonCode) error {
	if !validFinalize(claim, completed) || !reason.Valid() {
		return ErrInvalid
	}
	return s.finalize(ctx, claim, StateSuppressed, completed, completed, nil, reason)
}

func (s *Store) finalize(ctx context.Context, claim *Claim, state State, next, completed time.Time, delivered *time.Time, reason ReasonCode) error {
	tag, err := s.db.Exec(ctx, `UPDATE email_deliveries
		SET state = $1, next_attempt_at = $2, lease_expires_at = NULL,
			delivered_at = $3, last_reason = $4,
			representation_version = representation_version + 1, updated_at = $5
		WHERE id = $6 AND state = 'delivering' AND representation_version = $7`,
		state, next, delivered, reason, completed, claim.ID, claim.LeaseVersion)
	if err != nil {
		return safeStoreError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// SuppressVerification fences pending or claimed verification work when a
// later request supersedes it. The caller should invoke this in the same
// transaction that marks the request superseded.
func (s *Store) SuppressVerification(ctx context.Context, requestID uuid.UUID, at time.Time, reason ReasonCode) error {
	if requestID == uuid.Nil || at.IsZero() || !reason.Valid() {
		return ErrInvalid
	}
	_, err := s.db.Exec(ctx, `UPDATE email_deliveries
		SET state = 'suppressed', lease_expires_at = NULL, delivered_at = NULL,
			last_reason = $1, representation_version = representation_version + 1, updated_at = $2
		WHERE verification_request_id = $3 AND state IN ('pending', 'delivering')`, reason, at, requestID)
	if err != nil {
		return safeStoreError(err)
	}
	return nil
}

func validFinalize(claim *Claim, at time.Time) bool {
	return claim != nil && claim.ID != uuid.Nil && claim.LeaseVersion > 0 && !at.IsZero()
}

func sameLogicalDelivery(got Delivery, input EnqueueInput, canonical json.RawMessage) bool {
	return got.Kind == input.Kind && got.IdempotencyKey == stringsTrim(input.IdempotencyKey) &&
		got.RecipientUserID == input.RecipientUserID && equalUUID(got.OrganizationID, input.OrganizationID) &&
		equalUUID(got.RepositoryID, input.RepositoryID) && equalUUID(got.VerificationRequestID, input.VerificationRequestID) &&
		equalUUID(got.CommentID, input.CommentID) && equalUUID(got.IssueID, input.IssueID) &&
		equalUUID(got.MilestoneID, input.MilestoneID) && equalSnapshot(got.Snapshot, canonical)
}

func equalSnapshot(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func equalUUID(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type scanner interface{ Scan(...any) error }

func scanDelivery(row scanner) (Delivery, error) {
	var result Delivery
	var orgID, repoID, verificationID, commentID, issueID, milestoneID pgtype.UUID
	var lease, delivered pgtype.Timestamptz
	var reason *string
	err := row.Scan(&result.ID, &result.Kind, &result.IdempotencyKey, &result.RecipientUserID,
		&orgID, &repoID, &verificationID, &commentID, &issueID, &milestoneID,
		&result.Snapshot, &result.State, &result.Attempts, &result.NextAttemptAt, &lease, &delivered,
		&reason, &result.RepresentationVersion, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return Delivery{}, err
	}
	result.OrganizationID = uuidPointer(orgID)
	result.RepositoryID = uuidPointer(repoID)
	result.VerificationRequestID = uuidPointer(verificationID)
	result.CommentID = uuidPointer(commentID)
	result.IssueID = uuidPointer(issueID)
	result.MilestoneID = uuidPointer(milestoneID)
	if lease.Valid {
		value := lease.Time
		result.LeaseExpiresAt = &value
	}
	if delivered.Valid {
		value := delivered.Time
		result.DeliveredAt = &value
	}
	if reason != nil {
		value := ReasonCode(*reason)
		result.LastReason = &value
	}
	return result, nil
}

func uuidPointer(value pgtype.UUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	result := uuid.UUID(value.Bytes)
	return &result
}

func safeStoreError(err error) error {
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		switch postgres.Code {
		case "23505":
			return ErrConflict
		case "23503", "23514":
			return ErrInvalid
		}
	}
	return ErrStore
}
