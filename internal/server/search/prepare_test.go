package search

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type fakeCapabilityConn struct {
	rows []fakeRow
}

func (f *fakeCapabilityConn) QueryRow(context.Context, string, ...any) pgx.Row {
	row := f.rows[0]
	f.rows = f.rows[1:]
	return row
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
		}
	}
	return nil
}

func TestValidateCapabilities(t *testing.T) {
	valid := func(preload string) *fakeCapabilityConn {
		return &fakeCapabilityConn{rows: []fakeRow{
			{values: []any{true, true}}, {values: []any{preload}}, {values: []any{"'消费者':1", "%锁%"}},
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
