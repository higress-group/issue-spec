package githuboauth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errOrganizationIdentityMismatch = errors.New("githuboauth: organization identity mismatch")

type admissionStore struct {
	pool       *pgxpool.Pool
	secrets    *serverauth.Secrets
	providerID uuid.UUID
}

func (s *admissionStore) bindVerifiedOrganization(ctx context.Context, configured ApprovedOrganization,
	externalID int64, observedLogin string) (uuid.UUID, error) {
	if s == nil || s.pool == nil || s.providerID == uuid.Nil || externalID <= 0 ||
		configured.Login == "" || observedLogin != configured.Login ||
		(configured.stableID > 0 && configured.stableID != externalID) {
		return uuid.Nil, errOrganizationIdentityMismatch
	}
	var bindingID uuid.UUID
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		var storedExternalID int64
		err := tx.QueryRow(ctx, `SELECT id, external_org_id
			FROM github_admission_organizations
			WHERE provider_id = $1 AND login_key = lower($2)
			FOR UPDATE`, s.providerID, configured.Login).Scan(&bindingID, &storedExternalID)
		if err == nil {
			if storedExternalID != externalID {
				return errOrganizationIdentityMismatch
			}
			_, err = tx.Exec(ctx, `UPDATE github_admission_organizations
				SET last_observed_login = $3, last_verified_at = $4,
					representation_version = representation_version + 1
				WHERE id = $1 AND provider_id = $2`, bindingID, s.providerID, observedLogin, time.Now().UTC())
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		err = tx.QueryRow(ctx, `SELECT id, configured_login
			FROM github_admission_organizations
			WHERE provider_id = $1 AND external_org_id = $2
			FOR UPDATE`, s.providerID, externalID).Scan(&bindingID, new(string))
		if err == nil {
			if configured.stableID == 0 {
				return errOrganizationIdentityMismatch
			}
			_, err = tx.Exec(ctx, `UPDATE github_admission_organizations
				SET configured_login = $3, last_observed_login = $4, last_verified_at = $5,
					representation_version = representation_version + 1
				WHERE id = $1 AND provider_id = $2`, bindingID, s.providerID, configured.Login, observedLogin, time.Now().UTC())
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		bindingID = uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO github_admission_organizations
			(id, provider_id, external_org_id, configured_login, last_observed_login)
			VALUES ($1, $2, $3, $4, $5)`, bindingID, s.providerID, externalID, configured.Login, observedLogin)
		return err
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.Is(err, errOrganizationIdentityMismatch) ||
			(errors.As(err, &pgErr) && pgErr.Code == "23505") {
			return uuid.Nil, errOrganizationIdentityMismatch
		}
		return uuid.Nil, fmt.Errorf("githuboauth: persist organization binding: %w", err)
	}
	return bindingID, nil
}

func (s *admissionStore) record(ctx context.Context, subject, requestID string, mode AdmissionMode,
	decision, reason string, bindingID uuid.UUID) (serverauth.AdmissionEvidence, error) {
	if s == nil || s.pool == nil || s.secrets == nil || s.providerID == uuid.Nil ||
		strings.TrimSpace(subject) == "" || strings.TrimSpace(requestID) == "" {
		return serverauth.AdmissionEvidence{}, errors.New("githuboauth: incomplete admission audit evidence")
	}
	subjectRef := hex.EncodeToString(s.secrets.Digest("github-admission-subject", s.providerID.String()+"\x00"+subject))
	policy := "explicit-unrestricted"
	if mode == AdmissionOrganizationRestricted {
		policy = "github-organization"
	}
	metadata := map[string]any{"policy": policy, "decision": decision, "reason": reason}
	if bindingID != uuid.Nil {
		metadata["organization_binding_id"] = bindingID.String()
	}
	rawMetadata, _ := json.Marshal(metadata)
	action := "auth.github_admission." + decision
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_events
		(id, actor_identity_key, action, resource_type, resource_id, request_id, metadata)
		VALUES ($1, $2, $3, 'auth_provider', $4, $5, $6)`, uuid.New(), "github:"+subjectRef,
		action, s.providerID, requestID, rawMetadata)
	if err != nil {
		return serverauth.AdmissionEvidence{}, fmt.Errorf("githuboauth: persist admission audit: %w", err)
	}
	return serverauth.AdmissionEvidence{Policy: policy, Decision: decision, Subject: subjectRef,
		RequestID: requestID, Audited: true}, nil
}
