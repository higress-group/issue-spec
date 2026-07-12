package subscriptions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/events/networkpolicy"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
)

type Authorizer interface {
	EvaluateRepository(context.Context, authz.Subject, authz.RepositoryRequest) (authz.Decision, error)
	EvaluateRepositoryTx(context.Context, pgx.Tx, authz.Subject, authz.RepositoryRequest) (authz.Decision, error)
	EvaluateOrganization(context.Context, authz.Subject, models.OrgScope, authz.Operation) (authz.Decision, error)
	EvaluateOrganizationTx(context.Context, pgx.Tx, authz.Subject, models.OrgScope, authz.Operation) (authz.Decision, error)
}

type Config struct {
	Production           bool
	SecretOverlap        time.Duration
	DestinationPreflight DestinationPreflight
}

type DestinationPreflight interface {
	Validate(context.Context, string) error
}

type Service struct {
	database   *store.Store
	authorizer Authorizer
	keys       *Keyring
	config     Config
}

func New(database *store.Store, authorizer Authorizer, keys *Keyring, config Config) (*Service, error) {
	if database == nil || authorizer == nil || keys == nil || config.SecretOverlap < 0 {
		return nil, errors.New("webhook subscriptions: store, authorizer, keyring and valid config are required")
	}
	if config.SecretOverlap == 0 {
		config.SecretOverlap = 5 * time.Minute
	}
	return &Service{database: database, authorizer: authorizer, keys: keys, config: config}, nil
}

func (s *Service) Create(ctx context.Context, actor Actor, subject authz.Subject, input CreateInput) (SecretResult, error) {
	input.Retry = normalizeRetry(input.Retry)
	input = normalizeCreate(input)
	baseURL, destinationQuery, err := splitDestination(input.URL)
	if err != nil {
		return SecretResult{}, ErrInvalidInput
	}
	if destinationQuery != "" && input.DeliveryFormat != DeliveryFormatGitHubV3 {
		return SecretResult{}, ErrInvalidInput
	}
	input.URL = baseURL
	if err := validateActor(actor); err != nil {
		return SecretResult{}, err
	}
	scopeType, err := validateCreate(input, s.config.Production)
	if err != nil {
		return SecretResult{}, err
	}
	if err := s.authorize(ctx, subject, input.OrganizationID, input.RepositoryID, true); err != nil {
		return SecretResult{}, err
	}
	if s.config.DestinationPreflight != nil {
		if err := s.config.DestinationPreflight.Validate(ctx, input.URL); err != nil {
			return SecretResult{}, ErrInvalidInput
		}
	}
	secret, err := s.keys.GenerateSecret()
	if err != nil {
		return SecretResult{}, err
	}
	id := uuid.New()
	keyID, ciphertext, err := s.keys.Encrypt(id, 1, []byte(secret))
	if err != nil {
		return SecretResult{}, err
	}
	var queryKeyID string
	var queryCiphertext []byte
	var queryVersion int64
	if destinationQuery != "" {
		queryVersion = 1
		queryKeyID, queryCiphertext, err = s.keys.EncryptPurpose(id, queryVersion, "destination-query", []byte(destinationQuery))
		if err != nil {
			return SecretResult{}, err
		}
	}
	var created Subscription
	err = s.database.WithinTx(ctx, func(tx *store.Tx) error {
		if err := s.authorizeTx(ctx, tx.PGX(), subject, input.OrganizationID, input.RepositoryID, true); err != nil {
			return err
		}
		row := tx.PGX().QueryRow(ctx, `INSERT INTO webhook_subscriptions
			(id, organization_id, repository_id, scope_type, url, active, event_types,
			 retry_max_attempts, retry_initial_backoff, retry_max_backoff, created_by_user_id,
			 delivery_format, signing_mode, issue_actions, comment_actions, issue_kinds,
			 comment_classes, actor_classes, destination_query_key_id,
			 destination_query_ciphertext, destination_query_version)
			VALUES ($1, $2, $3, $4, $5, true, $6, $7, $8::interval, $9::interval, $10,
			 $11, $12, $13, $14, $15, $16, $17, NULLIF($18, ''), $19, $20)
			RETURNING `+subscriptionColumns,
			id, input.OrganizationID, input.RepositoryID, scopeType, input.URL, input.EventTypes,
			input.Retry.MaxAttempts, input.Retry.InitialBackoff.String(), input.Retry.MaxBackoff.String(), actor.UserID,
			input.DeliveryFormat, input.SigningMode, input.ContentPolicy.IssueActions,
			input.ContentPolicy.CommentActions, input.ContentPolicy.IssueKinds,
			input.ContentPolicy.CommentClasses, input.ContentPolicy.ActorClasses,
			queryKeyID, nullableBytes(queryCiphertext), queryVersion)
		created, err = scanSubscription(row)
		if err != nil {
			return fmt.Errorf("create webhook subscription: %w", err)
		}
		if _, err := tx.PGX().Exec(ctx, `INSERT INTO webhook_secret_versions
			(id, organization_id, repository_id, subscription_id, version, secret_ciphertext,
			 encryption_key_id, active, created_by_user_id)
			VALUES ($1, $2, $3, $4, 1, $5, $6, true, $7)`, uuid.New(), input.OrganizationID,
			input.RepositoryID, id, ciphertext, keyID, actor.UserID); err != nil {
			return fmt.Errorf("create webhook secret: %w", err)
		}
		if input.RepositoryID != nil {
			if _, err := tx.ScopedRepo(models.RepoScope{OrgID: input.OrganizationID, RepoID: *input.RepositoryID}).
				IncrementCollectionVersions(ctx, store.RepoCollectionWebhooks); err != nil {
				return err
			}
		}
		return audit(ctx, tx.PGX(), actor, created, "webhook.subscription.created", map[string]any{"secret_version": 1})
	})
	if err != nil {
		return SecretResult{}, err
	}
	return SecretResult{Subscription: created, Secret: secret, SecretVersion: 1}, nil
}

func (s *Service) List(ctx context.Context, subject authz.Subject, orgID uuid.UUID, repoID *uuid.UUID) ([]Subscription, error) {
	if orgID == uuid.Nil {
		return nil, ErrInvalidInput
	}
	if err := s.authorize(ctx, subject, orgID, repoID, true); err != nil {
		return nil, err
	}
	query := `SELECT ` + subscriptionColumns + ` FROM webhook_subscriptions WHERE organization_id = $1`
	args := []any{orgID}
	if repoID != nil {
		query += ` AND repository_id = $2`
		args = append(args, *repoID)
	} else {
		query += ` AND repository_id IS NULL`
	}
	query += ` ORDER BY created_at, id`
	rows, err := s.database.Pool().Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Subscription
	for rows.Next() {
		item, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		if err := s.validateStoredDestination(item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) Get(ctx context.Context, subject authz.Subject, orgID, id uuid.UUID) (Subscription, error) {
	item, err := s.load(ctx, s.database.Pool(), orgID, id, false)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.authorize(ctx, subject, item.OrganizationID, item.RepositoryID, true); err != nil {
		return Subscription{}, err
	}
	if err := s.validateStoredDestination(item); err != nil {
		return Subscription{}, err
	}
	return item, nil
}

func (s *Service) ListSuppressions(ctx context.Context, subject authz.Subject, orgID, id uuid.UUID) ([]Suppression, error) {
	item, err := s.load(ctx, s.database.Pool(), orgID, id, false)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, subject, item.OrganizationID, item.RepositoryID, true); err != nil {
		return nil, err
	}
	rows, err := s.database.Pool().Query(ctx, `SELECT id, organization_id, repository_id, event_id,
		subscription_id, event_type, action, issue_kind, comment_class, actor_class, reason, created_at
		FROM webhook_suppressions WHERE organization_id = $1 AND subscription_id = $2
		ORDER BY created_at DESC, id DESC`, orgID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Suppression, 0)
	for rows.Next() {
		var suppression Suppression
		if err := rows.Scan(&suppression.ID, &suppression.OrganizationID, &suppression.RepositoryID,
			&suppression.EventID, &suppression.SubscriptionID, &suppression.EventType,
			&suppression.Action, &suppression.IssueKind, &suppression.CommentClass,
			&suppression.ActorClass, &suppression.Reason, &suppression.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, suppression)
	}
	return result, rows.Err()
}

func (s *Service) Update(ctx context.Context, actor Actor, subject authz.Subject, orgID, id uuid.UUID, input UpdateInput) (Subscription, error) {
	input.Retry = normalizeRetry(input.Retry)
	input = normalizeUpdate(input)
	baseURL, destinationQuery, splitErr := splitDestination(input.URL)
	if splitErr != nil {
		return Subscription{}, ErrInvalidInput
	}
	input.URL = baseURL
	if validateActor(actor) != nil || input.ExpectedVersion < 1 || validateURL(input.URL, s.config.Production) != nil ||
		validatePolicy(input.DeliveryFormat, input.SigningMode, input.ContentPolicy, input.EventTypes) != nil || validateRetry(input.Retry) != nil {
		return Subscription{}, ErrInvalidInput
	}
	current, err := s.load(ctx, s.database.Pool(), orgID, id, false)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.authorize(ctx, subject, current.OrganizationID, current.RepositoryID, true); err != nil {
		return Subscription{}, err
	}
	if current.RevokedAt != nil {
		return Subscription{}, ErrRevoked
	}
	if input.DeliveryFormat != DeliveryFormatGitHubV3 &&
		(destinationQuery != "" || (current.HasDestinationQuery && !input.ClearDestinationQuery)) {
		return Subscription{}, ErrInvalidInput
	}
	if s.config.DestinationPreflight != nil {
		if err := s.config.DestinationPreflight.Validate(ctx, input.URL); err != nil {
			return Subscription{}, ErrInvalidInput
		}
	}
	var updated Subscription
	err = s.database.WithinTx(ctx, func(tx *store.Tx) error {
		current, err := s.load(ctx, tx.PGX(), orgID, id, true)
		if err != nil {
			return err
		}
		if err := s.authorizeTx(ctx, tx.PGX(), subject, orgID, current.RepositoryID, true); err != nil {
			return err
		}
		if current.RevokedAt != nil {
			return ErrRevoked
		}
		queryKeyID, queryCiphertext, queryVersion := current.DestinationQueryKeyID,
			current.DestinationQuery, current.DestinationQueryVersion
		if input.ClearDestinationQuery {
			queryKeyID, queryCiphertext, queryVersion = "", nil, 0
		} else if destinationQuery != "" {
			queryVersion++
			if queryVersion < 1 {
				queryVersion = 1
			}
			queryKeyID, queryCiphertext, err = s.keys.EncryptPurpose(id, queryVersion, "destination-query", []byte(destinationQuery))
			if err != nil {
				return err
			}
		}
		row := tx.PGX().QueryRow(ctx, `UPDATE webhook_subscriptions SET url = $3, active = $4,
			event_types = $5, retry_max_attempts = $6, retry_initial_backoff = $7::interval,
			retry_max_backoff = $8::interval, representation_version = representation_version + 1,
			updated_at = clock_timestamp(), delivery_format = $10, signing_mode = $11,
			issue_actions = $12, comment_actions = $13, issue_kinds = $14,
			comment_classes = $15, actor_classes = $16,
			destination_query_key_id = NULLIF($17, ''), destination_query_ciphertext = $18,
			destination_query_version = $19
			WHERE organization_id = $1 AND id = $2 AND revoked_at IS NULL
			AND representation_version = $9 RETURNING `+subscriptionColumns,
			orgID, id, input.URL, input.Active, input.EventTypes, input.Retry.MaxAttempts,
			input.Retry.InitialBackoff.String(), input.Retry.MaxBackoff.String(), input.ExpectedVersion,
			input.DeliveryFormat, input.SigningMode, input.ContentPolicy.IssueActions,
			input.ContentPolicy.CommentActions, input.ContentPolicy.IssueKinds,
			input.ContentPolicy.CommentClasses, input.ContentPolicy.ActorClasses,
			queryKeyID, nullableBytes(queryCiphertext), queryVersion)
		updated, err = scanSubscription(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionConflict
		}
		if err != nil {
			return err
		}
		if current.RepositoryID != nil {
			if _, err := tx.ScopedRepo(models.RepoScope{OrgID: orgID, RepoID: *current.RepositoryID}).
				IncrementCollectionVersions(ctx, store.RepoCollectionWebhooks); err != nil {
				return err
			}
		}
		return audit(ctx, tx.PGX(), actor, updated, "webhook.subscription.updated", nil)
	})
	return updated, err
}

func (s *Service) RotateSecret(ctx context.Context, actor Actor, subject authz.Subject, orgID, id uuid.UUID) (SecretResult, error) {
	if validateActor(actor) != nil || orgID == uuid.Nil || id == uuid.Nil {
		return SecretResult{}, ErrInvalidInput
	}
	secret, err := s.keys.GenerateSecret()
	if err != nil {
		return SecretResult{}, err
	}
	var item Subscription
	var version int64
	err = s.database.WithinTx(ctx, func(tx *store.Tx) error {
		item, err = s.load(ctx, tx.PGX(), orgID, id, true)
		if err != nil {
			return err
		}
		if err := s.authorizeTx(ctx, tx.PGX(), subject, orgID, item.RepositoryID, true); err != nil {
			return err
		}
		if item.RevokedAt != nil {
			return ErrRevoked
		}
		if err := s.validateStoredDestination(item); err != nil {
			return err
		}
		if !item.Active {
			return ErrInvalidInput
		}
		if err := tx.PGX().QueryRow(ctx, `SELECT COALESCE(max(version), 0) + 1
			FROM webhook_secret_versions WHERE organization_id = $1 AND subscription_id = $2`,
			orgID, id).Scan(&version); err != nil {
			return err
		}
		keyID, ciphertext, err := s.keys.Encrypt(id, version, []byte(secret))
		if err != nil {
			return err
		}
		var now time.Time
		if err := tx.PGX().QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		if _, err := tx.PGX().Exec(ctx, `UPDATE webhook_secret_versions
			SET accept_until = NULL, revoked_at = $3
			WHERE organization_id = $1 AND subscription_id = $2 AND NOT active
			AND revoked_at IS NULL AND accept_until IS NOT NULL`, orgID, id, now); err != nil {
			return err
		}
		if _, err := tx.PGX().Exec(ctx, `UPDATE webhook_secret_versions SET active = false,
			retired_at = $3, accept_until = $4 WHERE organization_id = $1 AND subscription_id = $2
			AND active AND revoked_at IS NULL`, orgID, id, now, now.Add(s.config.SecretOverlap)); err != nil {
			return err
		}
		if _, err := tx.PGX().Exec(ctx, `INSERT INTO webhook_secret_versions
			(id, organization_id, repository_id, subscription_id, version, secret_ciphertext,
			 encryption_key_id, active, created_by_user_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, true, $8)`, uuid.New(), orgID,
			item.RepositoryID, id, version, ciphertext, keyID, actor.UserID); err != nil {
			return err
		}
		item, err = scanSubscription(tx.PGX().QueryRow(ctx, `UPDATE webhook_subscriptions
			SET representation_version = representation_version + 1, updated_at = clock_timestamp()
			WHERE organization_id = $1 AND id = $2 RETURNING `+subscriptionColumns, orgID, id))
		if err != nil {
			return err
		}
		if item.RepositoryID != nil {
			if _, err := tx.ScopedRepo(models.RepoScope{OrgID: orgID, RepoID: *item.RepositoryID}).
				IncrementCollectionVersions(ctx, store.RepoCollectionWebhooks); err != nil {
				return err
			}
		}
		return audit(ctx, tx.PGX(), actor, item, "webhook.secret.rotated", map[string]any{"secret_version": version})
	})
	if err != nil {
		return SecretResult{}, err
	}
	return SecretResult{Subscription: item, Secret: secret, SecretVersion: version}, nil
}

func (s *Service) Revoke(ctx context.Context, actor Actor, subject authz.Subject, orgID, id uuid.UUID) error {
	if validateActor(actor) != nil || orgID == uuid.Nil || id == uuid.Nil {
		return ErrInvalidInput
	}
	return s.database.WithinTx(ctx, func(tx *store.Tx) error {
		item, err := s.load(ctx, tx.PGX(), orgID, id, true)
		if err != nil {
			return err
		}
		if err := s.authorizeTx(ctx, tx.PGX(), subject, orgID, item.RepositoryID, true); err != nil {
			return err
		}
		if item.RevokedAt != nil {
			return nil
		}
		var now time.Time
		if err := tx.PGX().QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		item, err = scanSubscription(tx.PGX().QueryRow(ctx, `UPDATE webhook_subscriptions SET active = false,
			revoked_at = $3, representation_version = representation_version + 1, updated_at = $3
			WHERE organization_id = $1 AND id = $2 AND revoked_at IS NULL RETURNING `+subscriptionColumns,
			orgID, id, now))
		if err != nil {
			return err
		}
		if _, err := tx.PGX().Exec(ctx, `UPDATE webhook_secret_versions SET active = false,
			retired_at = COALESCE(retired_at, $3), accept_until = NULL, revoked_at = $3
			WHERE organization_id = $1 AND subscription_id = $2 AND revoked_at IS NULL`, orgID, id, now); err != nil {
			return err
		}
		if item.RepositoryID != nil {
			if _, err := tx.ScopedRepo(models.RepoScope{OrgID: orgID, RepoID: *item.RepositoryID}).
				IncrementCollectionVersions(ctx, store.RepoCollectionWebhooks); err != nil {
				return err
			}
		}
		return audit(ctx, tx.PGX(), actor, item, "webhook.subscription.revoked", nil)
	})
}

func (s *Service) AcceptedSecrets(ctx context.Context, orgID, id uuid.UUID, at time.Time) ([]AcceptedSecret, error) {
	if orgID == uuid.Nil || id == uuid.Nil || at.IsZero() {
		return nil, ErrInvalidInput
	}
	rows, err := s.database.Pool().Query(ctx, `SELECT secret.version, secret.encryption_key_id, secret.secret_ciphertext
		FROM webhook_secret_versions secret JOIN webhook_subscriptions subscription
		ON subscription.organization_id = secret.organization_id AND subscription.id = secret.subscription_id
		WHERE secret.organization_id = $1 AND secret.subscription_id = $2 AND subscription.active
		AND subscription.revoked_at IS NULL
		AND secret.revoked_at IS NULL AND (secret.active OR secret.accept_until > $3)
		ORDER BY secret.version DESC`, orgID, id, at.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AcceptedSecret
	for rows.Next() {
		var version int64
		var keyID string
		var ciphertext []byte
		if err := rows.Scan(&version, &keyID, &ciphertext); err != nil {
			return nil, err
		}
		plaintext, err := s.keys.Decrypt(keyID, id, version, ciphertext)
		if err != nil {
			return nil, err
		}
		result = append(result, AcceptedSecret{Version: version, Secret: plaintext})
	}
	return result, rows.Err()
}

func (s *Service) DecryptDestinationQuery(_ context.Context, subscriptionID uuid.UUID,
	keyID string, version int64, ciphertext []byte) ([]byte, error) {
	if subscriptionID == uuid.Nil || strings.TrimSpace(keyID) == "" || version < 1 || len(ciphertext) == 0 {
		return nil, ErrInvalidInput
	}
	return s.keys.DecryptPurpose(keyID, subscriptionID, version, "destination-query", ciphertext)
}

func (s *Service) SecretVersion(ctx context.Context, orgID, subscriptionID uuid.UUID, version int64) ([]byte, error) {
	if orgID == uuid.Nil || subscriptionID == uuid.Nil || version < 1 {
		return nil, ErrInvalidInput
	}
	var keyID string
	var ciphertext []byte
	err := s.database.Pool().QueryRow(ctx, `SELECT encryption_key_id, secret_ciphertext
		FROM webhook_secret_versions WHERE organization_id = $1 AND subscription_id = $2 AND version = $3`,
		orgID, subscriptionID, version).Scan(&keyID, &ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.keys.Decrypt(keyID, subscriptionID, version, ciphertext)
}

const subscriptionColumns = `id, organization_id, repository_id, scope_type, url, active, revoked_at,
	event_types, delivery_format, signing_mode, issue_actions, comment_actions, issue_kinds,
	comment_classes, actor_classes, COALESCE(destination_query_key_id, ''), destination_query_ciphertext,
	destination_query_version, retry_max_attempts,
	(extract(epoch from retry_initial_backoff) * 1000000000)::bigint,
	(extract(epoch from retry_max_backoff) * 1000000000)::bigint,
	representation_version, created_at, updated_at`

type rowScanner interface{ Scan(...any) error }

func scanSubscription(row rowScanner) (Subscription, error) {
	var item Subscription
	var initial, maximum int64
	err := row.Scan(&item.ID, &item.OrganizationID, &item.RepositoryID, &item.ScopeType,
		&item.URL, &item.Active, &item.RevokedAt, &item.EventTypes, &item.DeliveryFormat,
		&item.SigningMode, &item.ContentPolicy.IssueActions, &item.ContentPolicy.CommentActions,
		&item.ContentPolicy.IssueKinds, &item.ContentPolicy.CommentClasses, &item.ContentPolicy.ActorClasses,
		&item.DestinationQueryKeyID, &item.DestinationQuery, &item.DestinationQueryVersion,
		&item.Retry.MaxAttempts, &initial, &maximum,
		&item.RepresentationVersion, &item.CreatedAt, &item.UpdatedAt)
	item.HasDestinationQuery = len(item.DestinationQuery) > 0
	item.Retry.InitialBackoff, item.Retry.MaxBackoff = time.Duration(initial), time.Duration(maximum)
	return item, err
}

func (s *Service) load(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, orgID, id uuid.UUID, lock bool) (Subscription, error) {
	query := `SELECT ` + subscriptionColumns + ` FROM webhook_subscriptions WHERE organization_id = $1 AND id = $2`
	if lock {
		query += ` FOR UPDATE`
	}
	item, err := scanSubscription(db.QueryRow(ctx, query, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	return item, err
}

func (s *Service) authorize(ctx context.Context, subject authz.Subject, orgID uuid.UUID, repoID *uuid.UUID, manage bool) error {
	var decision authz.Decision
	var err error
	if repoID == nil {
		op := authz.OperationReadOrganization
		if manage {
			op = authz.OperationManageIntegrations
		}
		decision, err = s.authorizer.EvaluateOrganization(ctx, subject, models.OrgScope{OrgID: orgID}, op)
	} else {
		op := authz.OperationRead
		if manage {
			op = authz.OperationManageIntegrations
		}
		decision, err = s.authorizer.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: models.RepoScope{OrgID: orgID, RepoID: *repoID}, Operation: op})
	}
	return authorizationResult(decision, err)
}

func (s *Service) authorizeTx(ctx context.Context, tx pgx.Tx, subject authz.Subject, orgID uuid.UUID, repoID *uuid.UUID, manage bool) error {
	var decision authz.Decision
	var err error
	if repoID == nil {
		op := authz.OperationReadOrganization
		if manage {
			op = authz.OperationManageIntegrations
		}
		decision, err = s.authorizer.EvaluateOrganizationTx(ctx, tx, subject, models.OrgScope{OrgID: orgID}, op)
	} else {
		op := authz.OperationRead
		if manage {
			op = authz.OperationManageIntegrations
		}
		decision, err = s.authorizer.EvaluateRepositoryTx(ctx, tx, subject, authz.RepositoryRequest{Scope: models.RepoScope{OrgID: orgID, RepoID: *repoID}, Operation: op})
	}
	return authorizationResult(decision, err)
}

func authorizationResult(decision authz.Decision, err error) error {
	if err != nil {
		return err
	}
	if decision.Allowed {
		return nil
	}
	if !decision.Exists || !decision.Visible {
		return ErrNotFound
	}
	return ErrForbidden
}

func audit(ctx context.Context, tx pgx.Tx, actor Actor, item Subscription, action string, metadata map[string]any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["organization_id"] = item.OrganizationID
	encoded, _ := json.Marshal(metadata)
	_, err := tx.Exec(ctx, `INSERT INTO audit_events
		(id, organization_id, repository_id, actor_user_id, actor_identity_key,
		 action, resource_type, resource_id, request_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, 'webhook_subscription', $7, $8, $9::jsonb)`,
		uuid.New(), item.OrganizationID, item.RepositoryID, actor.UserID, actor.IdentityKey,
		action, item.ID, actor.RequestID, string(encoded))
	return err
}

func validateCreate(input CreateInput, production bool) (ScopeType, error) {
	if input.OrganizationID == uuid.Nil || validateURL(input.URL, production) != nil ||
		validatePolicy(input.DeliveryFormat, input.SigningMode, input.ContentPolicy, input.EventTypes) != nil || validateRetry(input.Retry) != nil {
		return "", ErrInvalidInput
	}
	if input.RepositoryID == nil {
		return ScopeOrganization, nil
	}
	if *input.RepositoryID == uuid.Nil {
		return "", ErrInvalidInput
	}
	return ScopeRepository, nil
}

func validateURL(raw string, production bool) error {
	if _, err := (networkpolicy.Policy{Production: production}).ValidateURL(raw); err != nil {
		return ErrInvalidInput
	}
	return nil
}

func (s *Service) validateStoredDestination(item Subscription) error {
	if validateURL(item.URL, s.config.Production) != nil {
		return ErrUnsafeDestination
	}
	return nil
}

func validateEventTypes(values []string) error {
	if len(values) == 0 {
		return ErrInvalidInput
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return ErrInvalidInput
		}
		if _, ok := seen[value]; ok {
			return ErrInvalidInput
		}
		seen[value] = struct{}{}
	}
	sort.Strings(values)
	return nil
}

func normalizeCreate(input CreateInput) CreateInput {
	input.DeliveryFormat, input.SigningMode, input.ContentPolicy, input.EventTypes =
		normalizePolicy(input.DeliveryFormat, input.SigningMode, input.ContentPolicy, input.EventTypes)
	return input
}

func normalizeUpdate(input UpdateInput) UpdateInput {
	input.DeliveryFormat, input.SigningMode, input.ContentPolicy, input.EventTypes =
		normalizePolicy(input.DeliveryFormat, input.SigningMode, input.ContentPolicy, input.EventTypes)
	return input
}

func normalizePolicy(format DeliveryFormat, signing SigningMode, policy ContentPolicy, eventTypes []string) (DeliveryFormat, SigningMode, ContentPolicy, []string) {
	if format == "" {
		format = DeliveryFormatIssueSpecV1
	}
	if format == DeliveryFormatIssueSpecV1 {
		if signing == "" {
			signing = SigningModeBearer
		}
	} else if signing == "" {
		signing = SigningModeNone
	}
	policy.IssueActions = normalizeSet(policy.IssueActions, []string{"opened", "edited", "closed", "reopened"})
	policy.CommentActions = normalizeSet(policy.CommentActions, []string{"created", "edited"})
	policy.IssueKinds = normalizeSet(policy.IssueKinds, []string{"ordinary", "proposal", "design", "implement"})
	policy.CommentClasses = normalizeSet(policy.CommentClasses, []string{"human-untyped", "typed"})
	policy.ActorClasses = normalizeSet(policy.ActorClasses, []string{"human"})
	if format == DeliveryFormatGitHubV3 {
		eventTypes = eventTypesForPolicy(policy)
	} else {
		eventTypes = normalizeEventTypes(eventTypes)
	}
	return format, signing, policy, eventTypes
}

func normalizeSet(values, defaults []string) []string {
	if values == nil {
		values = defaults
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	sort.Strings(result)
	return result
}

func eventTypesForPolicy(policy ContentPolicy) []string {
	result := make([]string, 0, len(policy.IssueActions)+len(policy.CommentActions))
	for _, action := range policy.IssueActions {
		switch action {
		case "opened":
			result = append(result, "issue.created")
		case "edited", "closed", "reopened":
			result = append(result, "issue."+action)
		}
	}
	for _, action := range policy.CommentActions {
		result = append(result, "issue_comment."+action)
	}
	sort.Strings(result)
	return result
}

func validatePolicy(format DeliveryFormat, signing SigningMode, policy ContentPolicy, eventTypes []string) error {
	if format == DeliveryFormatIssueSpecV1 {
		if signing != SigningModeBearer {
			return ErrInvalidInput
		}
		return validateEventTypes(eventTypes)
	}
	if format != DeliveryFormatGitHubV3 || (signing != SigningModeNone && signing != SigningModeHMACSHA256) {
		return ErrInvalidInput
	}
	if len(policy.IssueActions) == 0 && len(policy.CommentActions) == 0 {
		return ErrInvalidInput
	}
	for values, allowed := range map[*[]string]map[string]struct{}{
		&policy.IssueActions:   setOf("opened", "edited", "closed", "reopened"),
		&policy.CommentActions: setOf("created", "edited"),
		&policy.IssueKinds:     setOf("ordinary", "proposal", "design", "implement"),
		&policy.CommentClasses: setOf("human-untyped", "typed"),
		&policy.ActorClasses:   setOf("human"),
	} {
		if validateValues(*values, allowed) != nil {
			return ErrInvalidInput
		}
	}
	if len(policy.IssueKinds) == 0 || len(policy.ActorClasses) == 0 ||
		(len(policy.CommentActions) > 0 && len(policy.CommentClasses) == 0) {
		return ErrInvalidInput
	}
	return validateEventTypes(eventTypes)
}

func setOf(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func validateValues(values []string, allowed map[string]struct{}) error {
	seen := map[string]struct{}{}
	for _, value := range values {
		if _, ok := allowed[value]; !ok {
			return ErrInvalidInput
		}
		if _, ok := seen[value]; ok {
			return ErrInvalidInput
		}
		seen[value] = struct{}{}
	}
	return nil
}

func splitDestination(raw string) (string, string, error) {
	if raw != strings.TrimSpace(raw) {
		return "", "", ErrInvalidInput
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Fragment != "" || parsed.ForceQuery {
		return "", "", ErrInvalidInput
	}
	query := parsed.RawQuery
	parsed.RawQuery, parsed.ForceQuery = "", false
	return parsed.String(), query, nil
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func normalizeEventTypes(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	return result
}

func normalizeRetry(policy RetryPolicy) RetryPolicy {
	if policy.MaxAttempts == 0 {
		policy.MaxAttempts = 8
	}
	if policy.InitialBackoff == 0 {
		policy.InitialBackoff = time.Second
	}
	if policy.MaxBackoff == 0 {
		policy.MaxBackoff = 5 * time.Minute
	}
	return policy
}

func validateRetry(policy RetryPolicy) error {
	if policy.MaxAttempts < 1 || policy.MaxAttempts > 100 || policy.InitialBackoff <= 0 || policy.MaxBackoff < policy.InitialBackoff {
		return ErrInvalidInput
	}
	return nil
}

func validateActor(actor Actor) error {
	if actor.UserID == uuid.Nil || strings.TrimSpace(actor.IdentityKey) == "" || strings.TrimSpace(actor.RequestID) == "" {
		return ErrInvalidInput
	}
	return nil
}

func ActorFromPrincipal(principal serverauth.Principal, requestID string) Actor {
	return Actor{UserID: principal.User.ID, IdentityKey: string(principal.Kind) + ":" + principal.User.ID.String(), RequestID: requestID}
}
