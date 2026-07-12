package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationAdvisoryLock int64 = 0x4953535545535043 // "ISSUESPC"

const LatestSchemaVersion int64 = 16

//go:embed migrations/*.sql
var migrationFiles embed.FS

type MigrationInfo struct {
	Version  int64
	Name     string
	Checksum string
}

type migration struct {
	MigrationInfo
	sql      string
	checksum []byte
}

// EmbeddedMigrations returns immutable metadata for diagnostics and tests.
func EmbeddedMigrations() ([]MigrationInfo, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	result := make([]MigrationInfo, len(migrations))
	for i := range migrations {
		result[i] = migrations[i].MigrationInfo
	}
	return result, nil
}

// RunMigrations serializes forward-only migrations with a PostgreSQL advisory
// lock held by one acquired connection for the entire run. Each migration and
// its version record commit atomically on that same connection.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("migrate: nil pgx pool")
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire connection: %w", err)
	}
	release := true
	defer func() {
		if release {
			conn.Release()
		}
	}()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationAdvisoryLock); err != nil {
		return fmt.Errorf("migrate: acquire advisory lock: %w", err)
	}
	locked := true
	defer func() {
		if !locked {
			return
		}
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLock).Scan(&unlocked); err != nil || !unlocked {
			raw := conn.Hijack()
			release = false
			_ = raw.Close(context.Background())
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version bigint PRIMARY KEY,
			name text NOT NULL,
			checksum bytea NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
		)`); err != nil {
		return fmt.Errorf("migrate: create version table: %w", err)
	}

	var newest int64
	if err := conn.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&newest); err != nil {
		return fmt.Errorf("migrate: inspect current version: %w", err)
	}
	if len(migrations) == 0 {
		if newest > 0 {
			return fmt.Errorf("migrate: database version %d is newer than this binary", newest)
		}
		locked = false
		var unlocked bool
		if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLock).Scan(&unlocked); err != nil || !unlocked {
			return fmt.Errorf("migrate: release advisory lock: %w", err)
		}
		return nil
	}
	if newest > migrations[len(migrations)-1].Version {
		return fmt.Errorf("migrate: database version %d is newer than binary version %d", newest, migrations[len(migrations)-1].Version)
	}

	for _, item := range migrations {
		var name string
		var checksum []byte
		err := conn.QueryRow(ctx, `SELECT name, checksum FROM schema_migrations WHERE version = $1`, item.Version).Scan(&name, &checksum)
		switch {
		case err == nil:
			if name != item.Name || !equalBytes(checksum, item.checksum) {
				return fmt.Errorf("migrate: applied migration %d differs from embedded %s", item.Version, item.Name)
			}
			continue
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("migrate: inspect version %d: %w", item.Version, err)
		}

		tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("migrate: begin version %d: %w", item.Version, err)
		}
		if _, err := tx.Exec(ctx, item.sql); err != nil {
			_ = tx.Rollback(context.Background())
			return fmt.Errorf("migrate: apply %s: %w", item.Name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`, item.Version, item.Name, item.checksum); err != nil {
			_ = tx.Rollback(context.Background())
			return fmt.Errorf("migrate: record %s: %w", item.Name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrate: commit %s: %w", item.Name, err)
		}
	}

	locked = false
	var unlocked bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLock).Scan(&unlocked); err != nil || !unlocked {
		return fmt.Errorf("migrate: release advisory lock: %w", err)
	}
	return nil
}

// ValidateMigrations fails when the database is missing an embedded migration,
// has a modified checksum, or is newer than this binary. It never mutates the
// database and is used by validate-only startup mode.
func ValidateMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("migrate: nil pgx pool")
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	var tableExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass(current_schema() || '.schema_migrations') IS NOT NULL`).Scan(&tableExists); err != nil {
		return fmt.Errorf("migrate: inspect version table: %w", err)
	}
	if !tableExists {
		return errors.New("migrate: schema_migrations does not exist")
	}
	var newest int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&newest); err != nil {
		return fmt.Errorf("migrate: inspect current version: %w", err)
	}
	if len(migrations) == 0 {
		if newest > 0 {
			return fmt.Errorf("migrate: database version %d is newer than this binary", newest)
		}
		return nil
	}
	if newest > migrations[len(migrations)-1].Version {
		return fmt.Errorf("migrate: database version %d is newer than binary version %d", newest, migrations[len(migrations)-1].Version)
	}
	for _, item := range migrations {
		var name string
		var checksum []byte
		if err := pool.QueryRow(ctx, `SELECT name, checksum FROM schema_migrations WHERE version = $1`, item.Version).Scan(&name, &checksum); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("migrate: required migration %s is not applied", item.Name)
			}
			return fmt.Errorf("migrate: inspect version %d: %w", item.Version, err)
		}
		if name != item.Name || !equalBytes(checksum, item.checksum) {
			return fmt.Errorf("migrate: applied migration %d differs from embedded %s", item.Version, item.Name)
		}
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migrate: list embedded files: %w", err)
	}
	result := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migrate: filename %q must be VERSION_name.sql", entry.Name())
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("migrate: invalid version in %q", entry.Name())
		}
		contents, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", entry.Name(), err)
		}
		digest := sha256.Sum256(contents)
		result = append(result, migration{
			MigrationInfo: MigrationInfo{Version: version, Name: entry.Name(), Checksum: hex.EncodeToString(digest[:])},
			sql:           string(contents),
			checksum:      digest[:],
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Version < result[j].Version })
	for i := 1; i < len(result); i++ {
		if result[i-1].Version == result[i].Version {
			return nil, fmt.Errorf("migrate: duplicate version %d", result[i].Version)
		}
	}
	return result, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for i := range a {
		different |= a[i] ^ b[i]
	}
	return different == 0
}
