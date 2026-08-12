package search

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeCapabilityConn struct {
	rows  []fakeRow
	execs []string
}

func (f *fakeCapabilityConn) QueryRow(context.Context, string, ...any) pgx.Row {
	row := f.rows[0]
	f.rows = f.rows[1:]
	return row
}

func (f *fakeCapabilityConn) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, query)
	return pgconn.CommandTag{}, nil
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, value := range r.values {
		switch target := dest[i].(type) {
		case *bool:
			*target = value.(bool)
		case *string:
			*target = value.(string)
		case *int:
			*target = value.(int)
		case *float32:
			*target = value.(float32)
		}
	}
	return nil
}

func TestValidateCapabilities(t *testing.T) {
	valid := func(preload string) *fakeCapabilityConn {
		return &fakeCapabilityConn{rows: []fakeRow{
			{values: []any{true, true}}, {values: []any{preload}}, {values: []any{"'消费者':1", "%锁%", float32(0.1)}},
		}}
	}
	if err := validateCapabilities(t.Context(), valid("pg_stat_statements, pg_jieba")); err != nil {
		t.Fatal(err)
	}
	missing := valid("pg_stat_statements")
	if err := validateCapabilities(t.Context(), missing); err == nil || !strings.Contains(err.Error(), "shared_preload_libraries") {
		t.Fatalf("missing preload error = %v", err)
	}
	extension := &fakeCapabilityConn{rows: []fakeRow{{values: []any{true, false}}}}
	if err := validateCapabilities(t.Context(), extension); err == nil || !strings.Contains(err.Error(), "operator-installed") {
		t.Fatalf("missing extension error = %v", err)
	}
	inspection := &fakeCapabilityConn{rows: []fakeRow{{err: errors.New("secret database error")}}}
	if err := validateCapabilities(t.Context(), inspection); err == nil || !strings.Contains(err.Error(), "inspect extensions") {
		t.Fatalf("inspection error = %v", err)
	}
}

func TestReconcileIndexRecoversMissingAndStaleIndexes(t *testing.T) {
	expected := searchIndexes[0]

	missing := &fakeCapabilityConn{rows: []fakeRow{{err: pgx.ErrNoRows}}}
	if err := reconcileIndex(t.Context(), missing, expected); err != nil {
		t.Fatal(err)
	}
	if len(missing.execs) != 1 || missing.execs[0] != expected.statement {
		t.Fatalf("missing index execs = %#v", missing.execs)
	}

	for _, test := range []struct {
		name       string
		definition string
		valid      bool
		ready      bool
	}{
		{name: "invalid", definition: "CREATE INDEX broken", valid: false, ready: true},
		{name: "not ready", definition: "CREATE INDEX broken", valid: true, ready: false},
		{name: "definition drift", definition: "CREATE INDEX broken", valid: true, ready: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stale := &fakeCapabilityConn{rows: []fakeRow{{values: []any{test.definition, test.valid, test.ready}}}}
			if err := reconcileIndex(t.Context(), stale, expected); err != nil {
				t.Fatal(err)
			}
			if len(stale.execs) != 2 || !strings.HasPrefix(stale.execs[0], `DROP INDEX CONCURRENTLY "`+expected.name+`"`) || stale.execs[1] != expected.statement {
				t.Fatalf("stale index execs = %#v", stale.execs)
			}
		})
	}

	healthy := &fakeCapabilityConn{rows: []fakeRow{{values: []any{strings.Join(expected.signatures, " "), true, true}}}}
	if err := reconcileIndex(t.Context(), healthy, expected); err != nil {
		t.Fatal(err)
	}
	if len(healthy.execs) != 0 {
		t.Fatalf("healthy index execs = %#v", healthy.execs)
	}
}
