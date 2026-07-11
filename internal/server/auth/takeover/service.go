// Package takeover atomically exchanges an already-minted, one-time recovery
// credential for a normal browser session. It deliberately has no mint API.
package takeover

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RecoveryConsumer interface {
	ConsumeInTx(context.Context, pgx.Tx, string, string) (serverauth.Principal, error)
}

type SessionCreator interface {
	CreateInTx(context.Context, pgx.Tx, uuid.UUID, string, string) (session.Created, error)
}

type Service struct {
	pool     *pgxpool.Pool
	recovery RecoveryConsumer
	sessions SessionCreator
}

func New(pool *pgxpool.Pool, recovery RecoveryConsumer, sessions SessionCreator) (*Service, error) {
	if pool == nil || recovery == nil || sessions == nil {
		return nil, errors.New("takeover: database, recovery and session services are required")
	}
	return &Service{pool: pool, recovery: recovery, sessions: sessions}, nil
}

func (s *Service) Exchange(ctx context.Context, token, requestID, userAgent, remoteAddress string) (session.Created, error) {
	if s == nil || s.pool == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(requestID) == "" {
		return session.Created{}, serverauth.ErrInvalidCredential
	}
	var created session.Created
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		principal, err := s.recovery.ConsumeInTx(ctx, tx, token, requestID)
		if err != nil {
			return err
		}
		created, err = s.sessions.CreateInTx(ctx, tx, principal.User.ID, userAgent, remoteAddress)
		return err
	})
	if err != nil {
		return session.Created{}, fmt.Errorf("takeover: exchange: %w", err)
	}
	return created, nil
}
