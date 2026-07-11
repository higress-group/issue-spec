package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound            = errors.New("store: not found")
	ErrConflict            = errors.New("store: conflict")
	ErrInvalidInput        = errors.New("store: invalid input")
	ErrInvalidScope        = errors.New("store: invalid scope")
	ErrVersionConflict     = errors.New("store: representation version conflict")
	ErrIdempotencyMismatch = errors.New("store: idempotency key payload mismatch")
)

// RepoScope is re-exported at the store boundary because handlers should pass
// an explicit tenant value instead of loose repository IDs.
type RepoScope = models.RepoScope

type OrgScope = models.OrgScope

// DBTX is the common pgx surface used by pool- and transaction-backed scoped
// stores. It intentionally omits transaction lifecycle operations.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	if pool == nil {
		panic("store: nil pgx pool")
	}
	return &Store{pool: pool}
}

func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	return RunMigrations(ctx, s.pool)
}

func (s *Store) ValidateMigrations(ctx context.Context) error {
	return ValidateMigrations(ctx, s.pool)
}

func (s *Store) Org(orgID uuid.UUID) OrgStore {
	return s.ScopedOrg(OrgScope{OrgID: orgID})
}

func (s *Store) ScopedOrg(scope OrgScope) OrgStore {
	return OrgStore{db: s.pool, root: s, scope: scope}
}

func (s *Store) Repo(orgID, repoID uuid.UUID) RepoStore {
	return s.ScopedRepo(RepoScope{OrgID: orgID, RepoID: repoID})
}

func (s *Store) ScopedRepo(scope RepoScope) RepoStore {
	return RepoStore{db: s.pool, root: s, scope: scope}
}

// WithinTx runs fn once and never retries it. This matters because handlers
// may perform non-database work around their transactional store calls.
func (s *Store) WithinTx(ctx context.Context, fn func(*Tx) error) error {
	return s.WithTx(ctx, pgx.TxOptions{}, fn)
}

func (s *Store) WithTx(ctx context.Context, opts pgx.TxOptions, fn func(*Tx) error) error {
	if fn == nil {
		return errors.New("store: nil transaction callback")
	}
	return pgx.BeginTxFunc(ctx, s.pool, opts, func(tx pgx.Tx) error {
		return fn(&Tx{tx: tx, root: s})
	})
}

type Tx struct {
	tx   pgx.Tx
	root *Store
}

func (t *Tx) Org(orgID uuid.UUID) OrgStore {
	return t.ScopedOrg(OrgScope{OrgID: orgID})
}

func (t *Tx) ScopedOrg(scope OrgScope) OrgStore {
	return OrgStore{db: t.tx, root: t.root, scope: scope, inTx: true}
}

func (t *Tx) Repo(orgID, repoID uuid.UUID) RepoStore {
	return t.ScopedRepo(RepoScope{OrgID: orgID, RepoID: repoID})
}

func (t *Tx) ScopedRepo(scope RepoScope) RepoStore {
	return RepoStore{db: t.tx, root: t.root, scope: scope, inTx: true}
}

type OrgStore struct {
	db    DBTX
	root  *Store
	scope OrgScope
	inTx  bool
}

func (s OrgStore) Scope() OrgScope {
	return s.scope
}

func (s OrgStore) Repo(repoID uuid.UUID) RepoStore {
	return RepoStore{
		db:    s.db,
		root:  s.root,
		scope: RepoScope{OrgID: s.scope.OrgID, RepoID: repoID},
		inTx:  s.inTx,
	}
}

type RepoStore struct {
	db    DBTX
	root  *Store
	scope RepoScope
	inTx  bool
}

func (s RepoStore) Scope() RepoScope {
	return s.scope
}

func (s RepoStore) validate() error {
	if err := s.scope.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidScope, err)
	}
	if s.db == nil {
		return errors.New("store: nil query connection")
	}
	return nil
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503", "23505", "23P01":
			return fmt.Errorf("%w: %w", ErrConflict, err)
		}
	}
	return err
}
