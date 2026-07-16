// Package delegation issues short-lived, revocable runner child credentials.
package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/capability"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	MinTTL     = 30 * time.Second
	DefaultTTL = 7 * 24 * time.Hour
	MaxTTL     = DefaultTTL
)

type IssueInput struct {
	Issuer     serverauth.Principal
	Repo       models.RepoScope
	JobID      string
	Purpose    string
	Audience   string
	Subject    string
	Scopes     []string
	Operations []capability.Operation
	TTL        time.Duration
	RequestID  string
	Replace    bool
}

type Created struct {
	ID        uuid.UUID `json:"id"`
	Plaintext string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Expected struct {
	Repo     models.RepoScope
	JobID    string
	Purpose  string
	Audience string
}

type Service struct {
	pool    *pgxpool.Pool
	secrets *serverauth.Secrets
	now     func() time.Time
	authz   *authz.Service
	// afterJobLock is test-only synchronization for deterministic transaction
	// interleaving coverage. Production constructors leave it nil.
	afterJobLock func(string)
}

func New(pool *pgxpool.Pool, secrets *serverauth.Secrets) *Service {
	authorizer, _ := authz.New(pool)
	return &Service{pool: pool, secrets: secrets, authz: authorizer, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Issue(ctx context.Context, input IssueInput) (Created, error) {
	if err := input.Repo.Validate(); err != nil || input.Issuer.User.ID == uuid.Nil ||
		!validBinding(input.JobID) || !validBinding(input.Purpose) ||
		!validBinding(input.Audience) || !validBinding(input.Subject) {
		return Created{}, serverauth.ErrInvalidCredential
	}
	if input.Issuer.Kind != serverauth.CredentialPAT || input.Issuer.CredentialID == uuid.Nil ||
		!input.Issuer.HasScope("runner:delegate") || !exactRepositoryCap(input.Issuer, input.Repo) {
		return Created{}, serverauth.ErrInsufficientScope
	}
	if input.TTL < MinTTL || input.TTL > MaxTTL {
		return Created{}, serverauth.ErrExpiredCredential
	}
	allowedScopes := make([]string, 0, len(input.Scopes))
	seen := make(map[string]struct{}, len(input.Scopes))
	for _, scope := range input.Scopes {
		scope = strings.TrimSpace(scope)
		if !input.Issuer.HasScope(scope) || scope == "runner:delegate" || scope == "" {
			return Created{}, serverauth.ErrInsufficientScope
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		allowedScopes = append(allowedScopes, scope)
	}
	if len(allowedScopes) == 0 {
		return Created{}, serverauth.ErrInsufficientScope
	}
	sort.Strings(allowedScopes)
	allowedOperations, err := validateDelegatedOperations(input.Operations, allowedScopes)
	if err != nil {
		return Created{}, err
	}
	claims, _ := json.Marshal(map[string]any{"scopes": allowedScopes, "operations": allowedOperations})
	plaintext, _, err := s.secrets.RandomToken("dgt")
	if err != nil {
		return Created{}, err
	}
	now := s.now()
	created := Created{ID: uuid.New(), Plaintext: plaintext, ExpiresAt: now.Add(input.TTL)}
	patID := input.Issuer.CredentialID
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		requestID = "delegated:issue:" + created.ID.String()
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := lockJob(ctx, tx, input.Repo, input.JobID); err != nil {
			return err
		}
		if s.afterJobLock != nil {
			s.afterJobLock("issue")
		}
		var revoked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM delegated_job_revocations
			WHERE organization_id = $1 AND repository_id = $2 AND job_id = $3
		)`, input.Repo.OrgID, input.Repo.RepoID, input.JobID).Scan(&revoked); err != nil {
			return err
		}
		if revoked {
			return serverauth.ErrRevokedCredential
		}
		liveParent, err := loadParentPrincipal(ctx, tx, input.Issuer.CredentialID, input.Issuer.User, now)
		if err != nil {
			return err
		}
		if !liveParent.HasScope("runner:delegate") || !exactRepositoryCap(liveParent, input.Repo) {
			return serverauth.ErrInsufficientScope
		}
		for _, scope := range allowedScopes {
			if !liveParent.HasScope(scope) {
				return serverauth.ErrInsufficientScope
			}
		}
		decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, authz.Authenticated(liveParent), authz.RepositoryRequest{
			Scope: input.Repo, Operation: authz.OperationWrite,
		})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return serverauth.ErrInsufficientScope
		}
		var activeIDs []uuid.UUID
		rows, err := tx.Query(ctx, `SELECT id FROM delegated_tokens
			WHERE organization_id = $1 AND repository_id = $2 AND job_id = $3
			AND purpose = $4 AND audience = $5 AND subject = $6
			AND revoked_at IS NULL AND expires_at > $7 FOR UPDATE`, input.Repo.OrgID, input.Repo.RepoID,
			input.JobID, input.Purpose, input.Audience, input.Subject, now)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			activeIDs = append(activeIDs, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(activeIDs) > 0 && !input.Replace {
			return serverauth.ErrConflict
		}
		if len(activeIDs) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, $2)
				WHERE id = ANY($1)`, activeIDs, now); err != nil {
				return err
			}
		}
		metadata, _ := json.Marshal(map[string]any{"job_id": input.JobID, "purpose": input.Purpose,
			"audience": input.Audience, "replaced_count": len(activeIDs)})
		if err := tx.QueryRow(ctx, `INSERT INTO delegated_tokens
			(id, user_id, personal_access_token_id, organization_id, repository_id, job_id,
			 purpose, token_hash, audience, subject, claims, expires_at, created_at)
			SELECT $1, u.id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
			FROM users u WHERE u.id = $2 AND u.status = 'active' RETURNING id`, created.ID, input.Issuer.User.ID,
			patID, input.Repo.OrgID, input.Repo.RepoID, input.JobID, input.Purpose,
			s.secrets.Digest("delegated-token", plaintext), input.Audience, input.Subject, claims, created.ExpiresAt, now).Scan(&created.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(id, organization_id, repository_id, actor_user_id, actor_identity_key,
			 action, resource_type, resource_id, request_id, metadata)
			VALUES ($1, $2, $3, $4, $5, 'delegated_token.issue', 'delegated_token', $6, $7, $8)`,
			uuid.New(), input.Repo.OrgID, input.Repo.RepoID, input.Issuer.User.ID,
			"user:"+input.Issuer.User.ID.String(), created.ID, requestID, metadata)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Created{}, serverauth.ErrDisabledAccount
	}
	if err != nil {
		return Created{}, fmt.Errorf("delegation: issue: %w", err)
	}
	return created, nil
}

func (s *Service) Authenticate(ctx context.Context, plaintext string, expected Expected) (serverauth.Principal, error) {
	if _, err := serverauth.TokenPrefix(plaintext, "dgt"); err != nil {
		return serverauth.Principal{}, err
	}
	var principal serverauth.Principal
	var digest, claims []byte
	var revoked *time.Time
	var parentID *uuid.UUID
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT d.id, d.token_hash, d.organization_id, d.repository_id,
			d.job_id, d.purpose, d.audience, d.claims, d.expires_at, d.revoked_at,
			d.personal_access_token_id, u.id, u.login, COALESCE(u.nickname, u.display_name), u.email, u.status
			FROM delegated_tokens d JOIN users u ON u.id = d.user_id
			WHERE d.token_hash = $1 FOR SHARE OF d, u`, s.secrets.Digest("delegated-token", plaintext)).
			Scan(&principal.CredentialID, &digest, &principal.OrgID, &principal.RepoID,
				&principal.JobID, &principal.Purpose, &principal.Audience, &claims, &principal.ExpiresAt, &revoked,
				&parentID, &principal.User.ID, &principal.User.Login, &principal.User.DisplayName, &principal.User.Email,
				&principal.User.Status)
		if err != nil {
			return err
		}
		if principal.User.Status != "active" {
			return serverauth.ErrDisabledAccount
		}
		if revoked != nil {
			return serverauth.ErrRevokedCredential
		}
		now := s.now()
		if !now.Before(principal.ExpiresAt) {
			return serverauth.ErrExpiredCredential
		}
		if parentID == nil {
			return serverauth.ErrRevokedCredential
		}
		parent, err := loadParentPrincipal(ctx, tx, *parentID, principal.User, now)
		if err != nil {
			return err
		}
		decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, authz.Authenticated(parent), authz.RepositoryRequest{
			Scope: models.RepoScope{OrgID: principal.OrgID, RepoID: principal.RepoID}, Operation: authz.OperationWrite,
		})
		if err != nil {
			return err
		}
		if !decision.Allowed || !parent.HasScope("runner:delegate") || !exactRepositoryCap(parent,
			models.RepoScope{OrgID: principal.OrgID, RepoID: principal.RepoID}) {
			return serverauth.ErrInsufficientScope
		}
		var payload struct {
			Scopes     []string `json:"scopes"`
			Operations []string `json:"operations,omitempty"`
		}
		if err := json.Unmarshal(claims, &payload); err != nil || len(payload.Scopes) == 0 {
			return serverauth.ErrInvalidCredential
		}
		for _, scope := range payload.Scopes {
			if !parent.HasScope(scope) || scope == "runner:delegate" {
				return serverauth.ErrInsufficientScope
			}
		}
		claimedOperations := make([]capability.Operation, len(payload.Operations))
		for index, operation := range payload.Operations {
			claimedOperations[index] = capability.Operation(operation)
		}
		validatedOperations, operationErr := validateDelegatedOperations(claimedOperations, payload.Scopes)
		if operationErr != nil || !equalStrings(validatedOperations, payload.Operations) {
			return serverauth.ErrInsufficientScope
		}
		principal.Scopes = payload.Scopes
		principal.Operations = payload.Operations
		if (expected.Repo.OrgID != uuid.Nil && (expected.Repo.OrgID != principal.OrgID || expected.Repo.RepoID != principal.RepoID)) ||
			(expected.JobID != "" && expected.JobID != principal.JobID) ||
			(expected.Purpose != "" && expected.Purpose != principal.Purpose) ||
			(expected.Audience != "" && expected.Audience != principal.Audience) {
			return serverauth.ErrInsufficientScope
		}
		_, err = tx.Exec(ctx, `UPDATE delegated_tokens SET used_at = $2
			WHERE id = $1 AND revoked_at IS NULL`, principal.CredentialID, now)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	if err != nil {
		return serverauth.Principal{}, err
	}
	if !serverauth.EqualDigest(digest, s.secrets.Digest("delegated-token", plaintext)) {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	principal.Kind = serverauth.CredentialDelegated
	principal.RepoRestricted = true
	principal.RepositoryCaps = []serverauth.RepositoryCap{{OrgID: principal.OrgID, RepoID: principal.RepoID}}
	return principal, nil
}

func validateDelegatedOperations(operations []capability.Operation, scopes []string) ([]string, error) {
	granted := map[string]bool{}
	for _, scope := range scopes {
		granted[scope] = true
	}
	seen := map[string]bool{}
	var result []string
	for _, operation := range operations {
		value := string(operation)
		if value == "" || seen[value] {
			continue
		}
		switch operation {
		case capability.OperationIssueRead:
			if !granted["issues:read"] && !granted["issues:write"] && !granted["repo"] {
				return nil, serverauth.ErrInsufficientScope
			}
		case capability.OperationIssueCommentWrite, capability.OperationArtifactWrite:
			if !granted["issues:write"] && !granted["repo"] {
				return nil, serverauth.ErrInsufficientScope
			}
		default:
			return nil, serverauth.ErrInsufficientScope
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func exactRepositoryCap(principal serverauth.Principal, repo models.RepoScope) bool {
	return principal.RepoRestricted && len(principal.RepositoryCaps) == 1 &&
		principal.RepositoryCaps[0].OrgID == repo.OrgID && principal.RepositoryCaps[0].RepoID == repo.RepoID
}

func lockJob(ctx context.Context, tx pgx.Tx, repo models.RepoScope, jobID string) error {
	key := fmt.Sprintf("delegated-job|%s|%s|%q", repo.OrgID, repo.RepoID, strings.TrimSpace(jobID))
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return fmt.Errorf("delegation: lock job: %w", err)
	}
	return nil
}

func validBinding(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char == 0x7f {
			return false
		}
	}
	return true
}

func loadParentPrincipal(ctx context.Context, tx pgx.Tx, id uuid.UUID, user serverauth.User, now time.Time) (serverauth.Principal, error) {
	parent := serverauth.Principal{User: user, Kind: serverauth.CredentialPAT, CredentialID: id}
	var revokedAt, expiresAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT revoked_at, expires_at FROM personal_access_tokens
		WHERE id = $1 AND user_id = $2 FOR SHARE`, id, user.ID).Scan(&revokedAt, &expiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return parent, serverauth.ErrRevokedCredential
		}
		return parent, err
	}
	if revokedAt != nil {
		return parent, serverauth.ErrRevokedCredential
	}
	if expiresAt != nil && !now.Before(*expiresAt) {
		return parent, serverauth.ErrExpiredCredential
	}
	rows, err := tx.Query(ctx, `SELECT scope FROM pat_scopes WHERE personal_access_token_id = $1 ORDER BY scope`, id)
	if err != nil {
		return parent, err
	}
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			rows.Close()
			return parent, err
		}
		parent.Scopes = append(parent.Scopes, scope)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return parent, err
	}
	rows.Close()
	repos, err := tx.Query(ctx, `SELECT organization_id, repository_id FROM pat_repositories
		WHERE personal_access_token_id = $1 ORDER BY organization_id, repository_id`, id)
	if err != nil {
		return parent, err
	}
	for repos.Next() {
		var cap serverauth.RepositoryCap
		if err := repos.Scan(&cap.OrgID, &cap.RepoID); err != nil {
			repos.Close()
			return parent, err
		}
		parent.RepositoryCaps = append(parent.RepositoryCaps, cap)
	}
	if err := repos.Err(); err != nil {
		repos.Close()
		return parent, err
	}
	repos.Close()
	parent.RepoRestricted = len(parent.RepositoryCaps) > 0
	return parent, nil
}

func (s *Service) AuthenticateBearer(ctx context.Context, plaintext string) (serverauth.Principal, error) {
	return s.Authenticate(ctx, plaintext, Expected{})
}

func (s *Service) Revoke(ctx context.Context, tokenID uuid.UUID) error {
	now := s.now()
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var orgID, repoID uuid.UUID
		var jobID string
		err := tx.QueryRow(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, $2)
			WHERE id = $1 RETURNING organization_id, repository_id, job_id`, tokenID, now).
			Scan(&orgID, &repoID, &jobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return serverauth.ErrNotFound
		}
		if err != nil {
			return err
		}
		metadata, _ := json.Marshal(map[string]string{"job_id": jobID})
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(id, organization_id, repository_id, actor_identity_key, action, resource_type,
			 resource_id, request_id, metadata)
			VALUES ($1, $2, $3, 'system:credential-broker', 'delegated_token.revoke',
			'delegated_token', $4, $5, $6)`, uuid.New(), orgID, repoID, tokenID,
			"delegated:revoke:"+tokenID.String(), metadata)
		return err
	})
}

func (s *Service) RevokeJob(ctx context.Context, repo models.RepoScope, jobID string) error {
	return s.revokeJob(ctx, serverauth.Principal{}, repo, jobID, "")
}

// RevokeJobAs revokes every child lease for one repository/job after the
// requesting parent PAT is revalidated in the same transaction.
func (s *Service) RevokeJobAs(ctx context.Context, issuer serverauth.Principal, repo models.RepoScope, jobID, requestID string) error {
	if issuer.Kind != serverauth.CredentialPAT || !issuer.HasScope("runner:delegate") || !exactRepositoryCap(issuer, repo) {
		return serverauth.ErrInsufficientScope
	}
	return s.revokeJob(ctx, issuer, repo, jobID, requestID)
}

func (s *Service) revokeJob(ctx context.Context, issuer serverauth.Principal, repo models.RepoScope, jobID, requestID string) error {
	if err := repo.Validate(); err != nil || !validBinding(jobID) {
		return serverauth.ErrInvalidCredential
	}
	now := s.now()
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := lockJob(ctx, tx, repo, jobID); err != nil {
			return err
		}
		if s.afterJobLock != nil {
			s.afterJobLock("revoke")
		}
		actorKey := "system:credential-broker"
		var actorUserID any
		if issuer.User.ID != uuid.Nil {
			liveParent, err := loadParentPrincipal(ctx, tx, issuer.CredentialID, issuer.User, now)
			if err != nil {
				return err
			}
			if !liveParent.HasScope("runner:delegate") || !exactRepositoryCap(liveParent, repo) {
				return serverauth.ErrInsufficientScope
			}
			decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, authz.Authenticated(liveParent), authz.RepositoryRequest{
				Scope: repo, Operation: authz.OperationWrite,
			})
			if err != nil {
				return err
			}
			if !decision.Allowed {
				return serverauth.ErrInsufficientScope
			}
			actorKey = "user:" + issuer.User.ID.String()
			actorUserID = issuer.User.ID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO delegated_job_revocations
			(organization_id, repository_id, job_id, revoked_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (organization_id, repository_id, job_id) DO NOTHING`, repo.OrgID, repo.RepoID, jobID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, $4)
			WHERE organization_id = $1 AND repository_id = $2 AND job_id = $3`, repo.OrgID, repo.RepoID, jobID, now); err != nil {
			return err
		}
		metadata, _ := json.Marshal(map[string]string{"job_id": jobID})
		requestID = strings.TrimSpace(requestID)
		if requestID == "" {
			requestID = "delegated:revoke-job:" + jobID
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit_events
			(id, organization_id, repository_id, actor_user_id, actor_identity_key, action, resource_type,
			 request_id, metadata) VALUES ($1, $2, $3, $4, $5,
			'delegated_token.revoke_job', 'runner_job', $6, $7)`, uuid.New(), repo.OrgID, repo.RepoID,
			actorUserID, actorKey, requestID, metadata)
		return err
	})
}
