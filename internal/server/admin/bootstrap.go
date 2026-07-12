package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/recovery"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

type BootstrapClaimInput struct {
	Secret      string
	UserID      *uuid.UUID
	Login       string
	DisplayName string
	Email       *string
	RequestID   string
}

type BootstrapClaimResult struct {
	User     serverauth.User        `json:"user"`
	Status   models.BootstrapStatus `json:"status"`
	Recovery recovery.Created       `json:"recovery"`
}

func (s *Service) BootstrapStatus(ctx context.Context) (models.BootstrapStatus, error) {
	var status models.BootstrapStatus
	err := s.pool.QueryRow(ctx, `SELECT completed, completed_by_user_id, completed_at,
		representation_version FROM bootstrap_state WHERE singleton_key`).
		Scan(&status.Completed, &status.CompletedByID, &status.CompletedAt, &status.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		status.Available = s.hasBootstrap
		return status, nil
	}
	if err != nil {
		return models.BootstrapStatus{}, fmt.Errorf("admin: bootstrap status: %w", err)
	}
	status.Available = s.hasBootstrap && !status.Completed
	return status, nil
}

func (s *Service) ClaimBootstrap(ctx context.Context, input BootstrapClaimInput) (BootstrapClaimResult, error) {
	if !s.verifyBootstrapSecret(input.Secret) {
		return BootstrapClaimResult{}, ErrInvalidBootstrapSecret
	}
	if strings.TrimSpace(input.RequestID) == "" {
		return BootstrapClaimResult{}, ErrInvalidInput
	}
	if input.UserID == nil && (strings.TrimSpace(input.Login) == "" || strings.TrimSpace(input.DisplayName) == "") {
		return BootstrapClaimResult{}, ErrInvalidInput
	}
	var result BootstrapClaimResult
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO bootstrap_state (id) VALUES ($1)
			ON CONFLICT (singleton_key) DO NOTHING`, uuid.New()); err != nil {
			return err
		}
		var stateID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id, completed, completed_by_user_id, completed_at,
			representation_version FROM bootstrap_state WHERE singleton_key FOR UPDATE`).
			Scan(&stateID, &result.Status.Completed, &result.Status.CompletedByID,
				&result.Status.CompletedAt, &result.Status.Version); err != nil {
			return err
		}
		if result.Status.Completed {
			return ErrBootstrapCompleted
		}
		var existingAdmins int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM site_role_assignments WHERE role = 'site_admin'`).Scan(&existingAdmins); err != nil {
			return err
		}
		if existingAdmins != 0 {
			return ErrConflict
		}
		if input.UserID != nil {
			var err error
			result.User, err = scanUser(tx.QueryRow(ctx, `SELECT id, login, display_name, email, status
				FROM users WHERE id = $1 AND status = 'active' FOR UPDATE`, *input.UserID))
			if err != nil {
				return err
			}
		} else {
			result.User = serverauth.User{ID: uuid.New(), Login: strings.TrimSpace(input.Login),
				DisplayName: strings.TrimSpace(input.DisplayName), Email: input.Email, Status: "active"}
			if _, err := tx.Exec(ctx, `INSERT INTO users (id, login, display_name, email)
				VALUES ($1, $2, $3, $4)`, result.User.ID, result.User.Login, result.User.DisplayName, result.User.Email); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO site_role_assignments (id, user_id, role)
			VALUES ($1, $2, 'site_admin')`, uuid.New(), result.User.ID); err != nil {
			return err
		}
		var recoveryErr error
		result.Recovery, recoveryErr = recovery.New(s.pool, s.secrets).MintInTx(ctx, tx, result.User.ID,
			"bootstrap", "initial site administrator takeover", input.RequestID, 15*time.Minute)
		if recoveryErr != nil {
			return recoveryErr
		}
		now := s.now().Truncate(time.Microsecond)
		if err := tx.QueryRow(ctx, `UPDATE bootstrap_state SET completed = true,
			completed_by_user_id = $2, completed_at = $3, updated_at = $3,
			representation_version = representation_version + 1 WHERE id = $1
			RETURNING completed, completed_by_user_id, completed_at, representation_version`,
			stateID, result.User.ID, now).Scan(&result.Status.Completed, &result.Status.CompletedByID,
			&result.Status.CompletedAt, &result.Status.Version); err != nil {
			return err
		}
		result.Status.Available = false
		return audit(ctx, tx, Actor{UserID: result.User.ID, IdentityKey: "bootstrap:" + result.User.ID.String(),
			RequestID: input.RequestID}, uuid.Nil, uuid.Nil, stateID, "bootstrap.claim", "bootstrap_state",
			map[string]any{"created_user": input.UserID == nil})
	})
	if err != nil {
		if errors.Is(err, ErrBootstrapCompleted) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidInput) {
			return BootstrapClaimResult{}, err
		}
		return BootstrapClaimResult{}, mapError(err)
	}
	return result, nil
}
