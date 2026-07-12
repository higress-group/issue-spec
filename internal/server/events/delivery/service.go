package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/events/networkpolicy"
	"github.com/higress-group/issue-spec/internal/server/events/outbox"
	"github.com/higress-group/issue-spec/internal/server/events/subscriptions"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var deliveryNamespace = uuid.MustParse("ca7f151e-5502-56bc-b430-b6f318b7d102")
var errCredentialUnavailable = errors.New("delivery credential unavailable")

type Service struct {
	pool       *pgxpool.Pool
	authorizer Authorizer
	secrets    SecretProvider
	sender     Sender
	config     Config
	semaphore  chan struct{}
	quiesce    chan struct{}
	quiesceOne sync.Once
}

func New(pool *pgxpool.Pool, authorizer Authorizer, secrets SecretProvider, sender Sender, config Config) (*Service, error) {
	if pool == nil || authorizer == nil || secrets == nil || sender == nil {
		return nil, errors.New("webhook delivery: database, authorizer, secrets and sender are required")
	}
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = 30 * time.Second
	}
	if config.MaxConcurrency <= 0 {
		config.MaxConcurrency = 8
	}
	if config.Clock == nil {
		config.Clock = func() time.Time { return time.Now().UTC() }
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 100 * time.Millisecond
	}
	if strings.TrimSpace(config.APIOrigin) == "" {
		config.APIOrigin = os.Getenv("API_PUBLIC_URL")
	}
	if strings.TrimSpace(config.WebOrigin) == "" {
		config.WebOrigin = os.Getenv("WEB_PUBLIC_URL")
	}
	if strings.TrimSpace(config.APIOrigin) == "" {
		listen := strings.TrimSpace(os.Getenv("LISTEN_ADDR"))
		if listen == "" {
			listen = "127.0.0.1:8080"
		}
		if strings.HasPrefix(listen, ":") {
			listen = "127.0.0.1" + listen
		}
		listen = strings.Replace(listen, "0.0.0.0:", "127.0.0.1:", 1)
		listen = strings.Replace(listen, "[::]:", "127.0.0.1:", 1)
		config.APIOrigin = "http://" + listen
	}
	if strings.TrimSpace(config.WebOrigin) == "" {
		config.WebOrigin = config.APIOrigin
	}
	return &Service{pool: pool, authorizer: authorizer, secrets: secrets, sender: sender,
		config: config, semaphore: make(chan struct{}, config.MaxConcurrency), quiesce: make(chan struct{})}, nil
}

// Quiesce stops workers before their next expansion or delivery claim. Work
// that has already entered ProcessOne keeps using the Run context so the
// composition owner can give it a bounded drain window before cancellation.
// It is safe to call Quiesce more than once.
func (s *Service) Quiesce() {
	if s == nil {
		return
	}
	s.quiesceOne.Do(func() { close(s.quiesce) })
}

// StopClaims is the lifecycle-oriented alias used by server composition.
func (s *Service) StopClaims() { s.Quiesce() }

func (s *Service) quiescing() bool {
	select {
	case <-s.quiesce:
		return true
	default:
		return false
	}
}

func (s *Service) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	workerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsCh := make(chan error, s.config.MaxConcurrency)
	var workers sync.WaitGroup
	for range s.config.MaxConcurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				if s.quiescing() {
					return
				}
				expanded, err := s.ExpandOne(workerContext)
				if err != nil {
					if workerContext.Err() != nil {
						return
					}
					select {
					case errorsCh <- err:
						cancel()
					default:
					}
					return
				}
				if s.quiescing() {
					return
				}
				err = s.ProcessOne(workerContext)
				if err != nil && !errors.Is(err, ErrNoWork) {
					if workerContext.Err() != nil {
						return
					}
					select {
					case errorsCh <- err:
						cancel()
					default:
					}
					return
				}
				if !expanded && errors.Is(err, ErrNoWork) {
					timer := time.NewTimer(s.config.PollInterval)
					select {
					case <-workerContext.Done():
						timer.Stop()
						return
					case <-s.quiesce:
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		cancel()
		<-done
		return nil
	case <-s.quiesce:
		<-done
		return nil
	case err := <-errorsCh:
		cancel()
		<-done
		return err
	case <-done:
		select {
		case err := <-errorsCh:
			return err
		default:
			return nil
		}
	}
}

// ExpandOne expands the earliest unpublished event of one repository. The
// NOT EXISTS guard plus SKIP LOCKED keeps expansion ordered per repository.
func (s *Service) ExpandOne(ctx context.Context) (bool, error) {
	now := s.config.Clock()
	expanded := false
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var eventID, orgID, repoID uuid.UUID
		var eventType string
		var eventPayload []byte
		err := tx.QueryRow(ctx, `SELECT event.id, event.organization_id, event.repository_id, event.event_type,
			event.payload
			FROM event_outbox event WHERE event.published_at IS NULL AND event.available_at <= $1
			AND NOT EXISTS (SELECT 1 FROM event_outbox prior
				WHERE prior.organization_id = event.organization_id AND prior.repository_id = event.repository_id
				AND prior.published_at IS NULL AND prior.repository_sequence < event.repository_sequence)
			ORDER BY event.available_at, event.created_at, event.id
			FOR UPDATE OF event SKIP LOCKED LIMIT 1`, now).Scan(&eventID, &orgID, &repoID, &eventType, &eventPayload)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT subscription.id, secret.id,
			subscription.delivery_format, subscription.signing_mode,
			subscription.issue_actions, subscription.comment_actions, subscription.issue_kinds,
			subscription.comment_classes, subscription.actor_classes, subscription.url,
			COALESCE(subscription.destination_query_key_id, ''), subscription.destination_query_ciphertext,
			subscription.destination_query_version
			FROM webhook_subscriptions subscription
			JOIN LATERAL (SELECT id FROM webhook_secret_versions
				WHERE organization_id = subscription.organization_id
				AND subscription_id = subscription.id AND active AND revoked_at IS NULL
				ORDER BY version DESC LIMIT 1) secret ON true
			WHERE subscription.organization_id = $1 AND subscription.active
			AND subscription.revoked_at IS NULL
			AND $3 = ANY(subscription.event_types)
			AND ((subscription.scope_type = 'organization' AND subscription.repository_id IS NULL)
				OR (subscription.scope_type = 'repository' AND subscription.repository_id = $2))
			ORDER BY subscription.created_at, subscription.id`, orgID, repoID, eventType)
		if err != nil {
			return err
		}
		type target struct {
			subscriptionID, secretID uuid.UUID
			format                   subscriptions.DeliveryFormat
			signing                  subscriptions.SigningMode
			policy                   notificationPolicy
			url, queryKey            string
			queryCipher              []byte
			queryVersion             int64
		}
		var targets []target
		for rows.Next() {
			var item target
			if err := rows.Scan(&item.subscriptionID, &item.secretID, &item.format, &item.signing,
				&item.policy.IssueActions, &item.policy.CommentActions, &item.policy.IssueKinds,
				&item.policy.CommentClasses, &item.policy.ActorClasses, &item.url, &item.queryKey,
				&item.queryCipher, &item.queryVersion); err != nil {
				rows.Close()
				return err
			}
			targets = append(targets, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		var envelope outbox.Envelope
		if err := json.Unmarshal(eventPayload, &envelope); err != nil {
			return fmt.Errorf("decode outbox envelope: %w", err)
		}
		for _, target := range targets {
			deliveryID := stableDeliveryID(eventID, target.subscriptionID)
			payload, eventName, action := eventPayload, "issue-spec", envelope.Action
			if target.format == subscriptions.DeliveryFormatGitHubV3 {
				action = notificationAction(envelope)
				matched, reason := matchesNotification(envelope, target.policy)
				if !matched {
					facts := envelope.Notification
					issueKind, commentClass, actorClass := "ordinary", "", "human"
					if facts != nil {
						issueKind, commentClass, actorClass = facts.IssueKind, facts.CommentClass, facts.ActorClass
					}
					if _, err := tx.Exec(ctx, `INSERT INTO webhook_suppressions
						(id, organization_id, repository_id, event_id, subscription_id, event_type,
						 action, issue_kind, comment_class, actor_class, reason)
						VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10,$11)
						ON CONFLICT (organization_id, repository_id, event_id, subscription_id) DO NOTHING`,
						uuid.New(), orgID, repoID, eventID, target.subscriptionID, eventType,
						action, issueKind, commentClass, actorClass, reason); err != nil {
						return err
					}
					continue
				}
				payload, eventName, err = renderGitHub(envelope, s.config.APIOrigin, s.config.WebOrigin)
				if err != nil {
					return fmt.Errorf("render github webhook: %w", err)
				}
			}
			payloadHash := sha256.Sum256(payload)
			if _, err := tx.Exec(ctx, `INSERT INTO webhook_deliveries
				(id, organization_id, repository_id, event_id, subscription_id,
				 secret_version_id, state, next_attempt_at, delivery_format, event_name, action,
				 signing_mode, rendered_payload, rendered_payload_hash, destination_url,
				 destination_query_key_id, destination_query_ciphertext, destination_query_version)
				VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $8, $9, $10, $11,
				 $12, $13, $14, NULLIF($15,''), $16, $17)
				ON CONFLICT (organization_id, repository_id, event_id, subscription_id) DO NOTHING`,
				deliveryID, orgID, repoID, eventID, target.subscriptionID, target.secretID, now,
				target.format, eventName, action, target.signing, payload, payloadHash[:], target.url,
				target.queryKey, target.queryCipher, target.queryVersion); err != nil {
				return err
			}
		}
		// created_at is owned by PostgreSQL. A worker clock captured before the
		// event transaction commits can be slightly earlier, so publication must
		// use the database clock and the authoritative row timestamp. Keep the
		// injectable clock for availability, leases and retries only.
		if _, err := tx.Exec(ctx, `UPDATE event_outbox
			SET published_at = GREATEST(created_at, clock_timestamp())
			WHERE id = $1 AND published_at IS NULL`, eventID); err != nil {
			return err
		}
		expanded = true
		return nil
	})
	return expanded, err
}

func stableDeliveryID(eventID, subscriptionID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(deliveryNamespace, []byte(eventID.String()+":"+subscriptionID.String()))
}

// ClaimOne leases the earliest ready delivery while fencing stale workers with
// representation_version. next_attempt_at doubles as the lease expiry while
// state is delivering.
func (s *Service) ClaimOne(ctx context.Context) (*claim, error) {
	now := s.config.Clock()
	var result claim
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT delivery.id, delivery.organization_id,
			delivery.repository_id, delivery.event_id, delivery.subscription_id,
			delivery.secret_version_id, delivery.state, delivery.next_attempt_at,
			delivery.delivered_at, delivery.last_error, delivery.representation_version,
			 delivery.created_at, delivery.updated_at, event.event_type,
			 event.repository_sequence, secret.version, delivery.delivery_format,
			 delivery.event_name, delivery.action, delivery.signing_mode,
			 delivery.destination_url, delivery.rendered_payload,
			 COALESCE(delivery.destination_query_key_id, ''), delivery.destination_query_ciphertext,
			 delivery.destination_query_version,
			subscription.retry_max_attempts,
			(extract(epoch from subscription.retry_initial_backoff) * 1000000000)::bigint,
			(extract(epoch from subscription.retry_max_backoff) * 1000000000)::bigint
			FROM webhook_deliveries delivery
			JOIN event_outbox event ON event.organization_id = delivery.organization_id
				AND event.repository_id = delivery.repository_id AND event.id = delivery.event_id
			JOIN webhook_subscriptions subscription ON subscription.organization_id = delivery.organization_id
				AND subscription.id = delivery.subscription_id
			JOIN webhook_secret_versions secret ON secret.organization_id = delivery.organization_id
				AND secret.subscription_id = delivery.subscription_id AND secret.id = delivery.secret_version_id
			WHERE ((delivery.state = 'pending' AND delivery.next_attempt_at <= $1)
				OR (delivery.state = 'delivering' AND delivery.next_attempt_at <= $1))
			AND subscription.active AND subscription.revoked_at IS NULL
			AND NOT EXISTS (SELECT 1 FROM webhook_deliveries prior_delivery
				JOIN event_outbox prior_event ON prior_event.organization_id = prior_delivery.organization_id
				AND prior_event.repository_id = prior_delivery.repository_id AND prior_event.id = prior_delivery.event_id
				WHERE prior_delivery.organization_id = delivery.organization_id
				AND prior_delivery.repository_id = delivery.repository_id
				AND prior_delivery.subscription_id = delivery.subscription_id
				AND prior_event.repository_sequence < event.repository_sequence
				AND prior_delivery.state IN ('pending', 'delivering'))
			ORDER BY event.organization_id, event.repository_id, event.repository_sequence,
				delivery.subscription_id, delivery.id
			FOR UPDATE OF delivery SKIP LOCKED LIMIT 1`, now)
		var initial, maximum int64
		if err := row.Scan(&result.ID, &result.Scope.OrgID, &result.Scope.RepoID,
			&result.EventID, &result.SubscriptionID, &result.SecretVersionID, &result.State,
			&result.NextAttemptAt, &result.DeliveredAt, &result.LastError,
			&result.RepresentationVersion, &result.CreatedAt, &result.UpdatedAt,
			&result.EventType, &result.RepositorySequence, &result.SecretVersion,
			&result.DeliveryFormat, &result.EventName, &result.Action, &result.SigningMode,
			&result.URL, &result.Payload, &result.DestinationQueryKeyID,
			&result.DestinationQueryCiphertext, &result.DestinationQueryVersion,
			&result.Retry.MaxAttempts, &initial, &maximum); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoWork
			}
			return err
		}
		result.Retry.InitialBackoff, result.Retry.MaxBackoff = time.Duration(initial), time.Duration(maximum)
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(attempt_number), 0), count(*) FILTER (
			WHERE started_at > COALESCE((SELECT max(created_at) FROM audit_events
				WHERE resource_type = 'webhook_delivery' AND resource_id = $1
				AND action = 'webhook.delivery.redelivered'), '-infinity'::timestamptz))
			FROM webhook_delivery_attempts WHERE organization_id = $2 AND repository_id = $3
			AND delivery_id = $1`, result.ID, result.Scope.OrgID, result.Scope.RepoID).
			Scan(&result.GlobalAttempt, &result.CycleAttempt); err != nil {
			return err
		}
		result.GlobalAttempt++
		result.CycleAttempt++
		leaseUntil := now.Add(s.config.LeaseDuration)
		if err := tx.QueryRow(ctx, `UPDATE webhook_deliveries SET state = 'delivering',
			next_attempt_at = $2, representation_version = representation_version + 1,
			updated_at = $1 WHERE id = $3 RETURNING representation_version, updated_at`,
			now, leaseUntil, result.ID).Scan(&result.LeaseVersion, &result.UpdatedAt); err != nil {
			return err
		}
		result.State, result.NextAttemptAt, result.RepresentationVersion = "delivering", leaseUntil, result.LeaseVersion
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Service) ProcessOne(ctx context.Context) error {
	select {
	case s.semaphore <- struct{}{}:
		defer func() { <-s.semaphore }()
	case <-ctx.Done():
		return ctx.Err()
	}
	claimed, err := s.ClaimOne(ctx)
	if err != nil {
		return err
	}
	now := s.config.Clock()
	var secretID uuid.UUID
	var secret []byte
	var destinationQuery []byte
	_, err = (networkpolicy.Policy{}).ValidateURL(claimed.URL)
	if err == nil && claimed.DestinationQueryVersion > 0 {
		provider, ok := s.secrets.(interface {
			DecryptDestinationQuery(context.Context, uuid.UUID, string, int64, []byte) ([]byte, error)
		})
		if !ok {
			err = errCredentialUnavailable
		} else {
			destinationQuery, err = provider.DecryptDestinationQuery(ctx, claimed.SubscriptionID,
				claimed.DestinationQueryKeyID, claimed.DestinationQueryVersion, claimed.DestinationQueryCiphertext)
		}
	}
	if err == nil && claimed.SigningMode == subscriptions.SigningModeBearer {
		secretID, secret, err = s.acceptedSecret(ctx, claimed, now)
	} else if err == nil && claimed.SigningMode == subscriptions.SigningModeHMACSHA256 {
		provider, ok := s.secrets.(interface {
			SecretVersion(context.Context, uuid.UUID, uuid.UUID, int64) ([]byte, error)
		})
		if !ok {
			err = errCredentialUnavailable
		} else {
			secret, err = provider.SecretVersion(ctx, claimed.Scope.OrgID, claimed.SubscriptionID, claimed.SecretVersion)
			secretID = claimed.SecretVersionID
		}
	}
	var result networkpolicy.Result
	if err == nil {
		signature := ""
		if claimed.SigningMode == subscriptions.SigningModeHMACSHA256 {
			mac := hmac.New(sha256.New, secret)
			_, _ = mac.Write(claimed.Payload)
			signature = "sha256=" + hex.EncodeToString(mac.Sum(nil))
		}
		requestSecret := secret
		if claimed.DeliveryFormat == subscriptions.DeliveryFormatGitHubV3 {
			requestSecret = nil
		}
		result, err = s.sender.Send(ctx, networkpolicy.Request{URL: claimed.URL, Secret: requestSecret,
			EventID: claimed.EventID.String(), DeliveryID: claimed.ID.String(), Timestamp: now,
			Body: claimed.Payload, DeliveryFormat: string(claimed.DeliveryFormat), EventName: claimed.EventName,
			Action: claimed.Action, Signature: signature, DestinationQuery: destinationQuery})
	}
	err = redactSensitiveError(err, secret, destinationQuery)
	clear(secret)
	clear(destinationQuery)
	return s.finalize(ctx, claimed, secretID, result, err, now, s.config.Clock())
}

func (s *Service) acceptedSecret(ctx context.Context, claimed *claim, at time.Time) (uuid.UUID, []byte, error) {
	accepted, err := s.secrets.AcceptedSecrets(ctx, claimed.Scope.OrgID, claimed.SubscriptionID, at)
	if err != nil {
		return uuid.Nil, nil, err
	}
	defer func() {
		for index := range accepted {
			clear(accepted[index].Secret)
		}
	}()
	for _, candidate := range accepted {
		if candidate.Version == claimed.SecretVersion {
			return claimed.SecretVersionID, append([]byte(nil), candidate.Secret...), nil
		}
	}
	var currentID uuid.UUID
	var currentVersion int64
	err = s.pool.QueryRow(ctx, `SELECT secret.id, secret.version FROM webhook_secret_versions secret
		JOIN webhook_subscriptions subscription ON subscription.organization_id = secret.organization_id
			AND subscription.id = secret.subscription_id
		WHERE secret.organization_id = $1 AND secret.subscription_id = $2
		AND subscription.active AND subscription.revoked_at IS NULL
		AND secret.active AND secret.revoked_at IS NULL
		ORDER BY secret.version DESC LIMIT 1`, claimed.Scope.OrgID, claimed.SubscriptionID).
		Scan(&currentID, &currentVersion)
	if err != nil {
		return uuid.Nil, nil, errCredentialUnavailable
	}
	for _, candidate := range accepted {
		if candidate.Version == currentVersion {
			return currentID, append([]byte(nil), candidate.Secret...), nil
		}
	}
	return uuid.Nil, nil, errCredentialUnavailable
}

func (s *Service) finalize(ctx context.Context, claimed *claim, secretID uuid.UUID,
	result networkpolicy.Result, sendErr error, started, completed time.Time) error {
	state := "failed"
	var deliveredAt *time.Time
	var lastError *string
	nextAttempt := completed
	if successful(result.StatusCode) && sendErr == nil {
		state, deliveredAt = "succeeded", &completed
	} else {
		message := safeError(sendErr, result.StatusCode)
		lastError = &message
		if retryable(result.StatusCode, sendErr) && !errors.Is(sendErr, errCredentialUnavailable) {
			if claimed.CycleAttempt >= claimed.Retry.MaxAttempts {
				state = "dead"
			} else {
				state = "pending"
				nextAttempt = nextRetry(completed, claimed.Retry, claimed.CycleAttempt, result.Header)
			}
		}
	}
	requestHeaders, _ := json.Marshal(requestHeaderLedger(claimed))
	responseHeaders, _ := json.Marshal(safeResponseHeaders(result.Header))
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO webhook_delivery_attempts
			(id, organization_id, repository_id, delivery_id, attempt_number,
			 request_headers, response_status, response_headers, error, started_at, completed_at)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8::jsonb, $9, $10, $11)`,
			uuid.New(), claimed.Scope.OrgID, claimed.Scope.RepoID, claimed.ID,
			claimed.GlobalAttempt, string(requestHeaders), nullableStatus(result.StatusCode),
			string(responseHeaders), lastError, started, completed); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE webhook_deliveries SET state = $1, next_attempt_at = $2,
			delivered_at = $3, last_error = $4, secret_version_id = COALESCE(NULLIF($5::uuid, $6::uuid), secret_version_id),
			representation_version = representation_version + 1, updated_at = $7
			WHERE organization_id = $8 AND repository_id = $9 AND id = $10
			AND state = 'delivering' AND representation_version = $11`, state, nextAttempt,
			deliveredAt, lastError, secretID, uuid.Nil, completed, claimed.Scope.OrgID,
			claimed.Scope.RepoID, claimed.ID, claimed.LeaseVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

func requestHeaderLedger(claimed *claim) map[string]string {
	result := map[string]string{"content-type": "application/json"}
	if claimed.DeliveryFormat == subscriptions.DeliveryFormatGitHubV3 {
		result["user-agent"] = "GitHub-Hookshot/issue-spec"
		result["x-github-event"] = claimed.EventName
		result["x-github-delivery"] = claimed.ID.String()
		if claimed.SigningMode == subscriptions.SigningModeHMACSHA256 {
			result["x-hub-signature-256"] = "[REDACTED]"
		}
		return result
	}
	result["user-agent"] = "issue-spec-webhook/1"
	result["x-issue-spec-event"] = claimed.EventID.String()
	result["x-issue-spec-delivery"] = claimed.ID.String()
	return result
}

func redactSecretError(err error, secret []byte) error {
	return redactSensitiveError(err, secret, nil)
}

func redactSensitiveError(err error, secret, destinationQuery []byte) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if len(secret) > 0 {
		message = strings.ReplaceAll(message, string(secret), "[REDACTED]")
	}
	if len(destinationQuery) > 0 {
		message = strings.ReplaceAll(message, string(destinationQuery), "[REDACTED]")
	}
	if errors.Is(err, errCredentialUnavailable) {
		return errCredentialUnavailable
	}
	if errors.Is(err, networkpolicy.ErrInvalidDestination) {
		return networkpolicy.ErrInvalidDestination
	}
	if errors.Is(err, networkpolicy.ErrAddressDenied) {
		return networkpolicy.ErrAddressDenied
	}
	return errors.New(message)
}

func nullableStatus(status int) any {
	if status == 0 {
		return nil
	}
	return status
}

func safeError(err error, status int) string {
	if err == nil {
		return fmt.Sprintf("http status %d", status)
	}
	message := err.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func safeResponseHeaders(header http.Header) http.Header {
	result := http.Header{}
	for _, name := range []string{"Content-Type", "Retry-After"} {
		if value := header.Get(name); value != "" {
			if len(value) > 256 {
				value = value[:256]
			}
			result.Set(name, value)
		}
	}
	return result
}

func (s *Service) List(ctx context.Context, subject authz.Subject, scope models.RepoScope) ([]Delivery, error) {
	if err := s.authorize(ctx, subject, scope); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+deliveryColumns+` FROM webhook_deliveries delivery
		JOIN event_outbox event ON event.organization_id = delivery.organization_id
		AND event.repository_id = delivery.repository_id AND event.id = delivery.event_id
		JOIN webhook_secret_versions secret ON secret.organization_id = delivery.organization_id
		AND secret.subscription_id = delivery.subscription_id AND secret.id = delivery.secret_version_id
		WHERE delivery.organization_id = $1 AND delivery.repository_id = $2
		ORDER BY event.repository_sequence DESC, delivery.id`, scope.OrgID, scope.RepoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Delivery, 0)
	for rows.Next() {
		item, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) Get(ctx context.Context, subject authz.Subject, scope models.RepoScope, id uuid.UUID) (Detail, error) {
	if err := s.authorize(ctx, subject, scope); err != nil {
		return Detail{}, err
	}
	item, err := scanDelivery(s.pool.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM webhook_deliveries delivery
		JOIN event_outbox event ON event.organization_id = delivery.organization_id
		AND event.repository_id = delivery.repository_id AND event.id = delivery.event_id
		JOIN webhook_secret_versions secret ON secret.organization_id = delivery.organization_id
		AND secret.subscription_id = delivery.subscription_id AND secret.id = delivery.secret_version_id
		WHERE delivery.organization_id = $1 AND delivery.repository_id = $2 AND delivery.id = $3`,
		scope.OrgID, scope.RepoID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, attempt_number, response_status,
		response_headers, error, started_at, completed_at FROM webhook_delivery_attempts
		WHERE organization_id = $1 AND repository_id = $2 AND delivery_id = $3
		ORDER BY attempt_number`, scope.OrgID, scope.RepoID, id)
	if err != nil {
		return Detail{}, err
	}
	defer rows.Close()
	detail := Detail{Delivery: item, Attempts: make([]Attempt, 0)}
	for rows.Next() {
		var attempt Attempt
		if err := rows.Scan(&attempt.ID, &attempt.AttemptNumber, &attempt.ResponseStatus,
			&attempt.ResponseHeaders, &attempt.Error, &attempt.StartedAt, &attempt.CompletedAt); err != nil {
			return Detail{}, err
		}
		detail.Attempts = append(detail.Attempts, attempt)
	}
	return detail, rows.Err()
}

func (s *Service) Redeliver(ctx context.Context, actor Actor, subject authz.Subject,
	scope models.RepoScope, id uuid.UUID) (Delivery, error) {
	if actor.UserID == uuid.Nil || strings.TrimSpace(actor.IdentityKey) == "" || strings.TrimSpace(actor.RequestID) == "" {
		return Delivery{}, ErrInvalid
	}
	var result Delivery
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		decision, err := s.authorizer.EvaluateRepositoryTx(ctx, tx, subject,
			authz.RepositoryRequest{Scope: scope, Operation: authz.OperationManageIntegrations})
		if err != nil {
			return err
		}
		if err := authorizationResult(decision); err != nil {
			return err
		}
		now := s.config.Clock()
		result, err = scanDelivery(tx.QueryRow(ctx, `UPDATE webhook_deliveries delivery
			SET state = 'pending', next_attempt_at = $4, delivered_at = NULL, last_error = NULL,
			representation_version = delivery.representation_version + 1, updated_at = $4
			FROM event_outbox event, webhook_secret_versions secret, webhook_subscriptions subscription
			WHERE delivery.organization_id = $1 AND delivery.repository_id = $2 AND delivery.id = $3
			AND delivery.state IN ('failed', 'dead')
			AND subscription.organization_id = delivery.organization_id
			AND subscription.id = delivery.subscription_id
			AND subscription.active AND subscription.revoked_at IS NULL
			AND event.organization_id = delivery.organization_id AND event.repository_id = delivery.repository_id
			AND event.id = delivery.event_id AND secret.organization_id = delivery.organization_id
			AND secret.subscription_id = delivery.subscription_id AND secret.id = delivery.secret_version_id
			RETURNING `+deliveryColumns, scope.OrgID, scope.RepoID, id, now))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		metadata := `{"reason":"manual"}`
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(id, organization_id, repository_id, actor_user_id, actor_identity_key,
			 action, resource_type, resource_id, request_id, metadata, created_at)
			VALUES ($1, $2, $3, $4, $5, 'webhook.delivery.redelivered',
			 'webhook_delivery', $6, $7, $8::jsonb, $9)`, uuid.New(), scope.OrgID, scope.RepoID,
			actor.UserID, actor.IdentityKey, id, actor.RequestID, metadata, now)
		return err
	})
	return result, err
}

const deliveryColumns = `delivery.id, delivery.organization_id, delivery.repository_id,
	delivery.event_id, delivery.subscription_id, delivery.state, delivery.next_attempt_at,
	delivery.delivered_at, delivery.last_error, delivery.representation_version,
	delivery.created_at, delivery.updated_at, event.event_type, event.repository_sequence, secret.version,
	delivery.delivery_format, delivery.event_name, delivery.action`

type rowScanner interface{ Scan(...any) error }

func scanDelivery(row rowScanner) (Delivery, error) {
	var item Delivery
	err := row.Scan(&item.ID, &item.Scope.OrgID, &item.Scope.RepoID, &item.EventID,
		&item.SubscriptionID, &item.State, &item.NextAttemptAt, &item.DeliveredAt,
		&item.LastError, &item.RepresentationVersion, &item.CreatedAt, &item.UpdatedAt,
		&item.EventType, &item.RepositorySequence, &item.SecretVersion,
		&item.DeliveryFormat, &item.EventName, &item.Action)
	return item, err
}

func (s *Service) authorize(ctx context.Context, subject authz.Subject, scope models.RepoScope) error {
	if err := scope.Validate(); err != nil {
		return ErrInvalid
	}
	decision, err := s.authorizer.EvaluateRepository(ctx, subject,
		authz.RepositoryRequest{Scope: scope, Operation: authz.OperationManageIntegrations})
	if err != nil {
		return err
	}
	return authorizationResult(decision)
}

func authorizationResult(decision authz.Decision) error {
	if decision.Allowed {
		return nil
	}
	if !decision.Exists || !decision.Visible {
		return ErrNotFound
	}
	return ErrForbidden
}
