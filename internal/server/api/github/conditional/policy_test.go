package conditional

import (
	"testing"
	"time"
)

func TestPolicyUsesStableNextUTCHour(t *testing.T) {
	now := time.Date(2026, 7, 11, 3, 17, 42, 99, time.FixedZone("local", 8*60*60))
	policy := Policy{Clock: func() time.Time { return now }}
	first, second := policy.Rate(), policy.Rate()
	want := time.Date(2026, 7, 10, 20, 0, 0, 0, time.UTC)
	if !first.Reset.Equal(want) || first != second {
		t.Fatalf("rates = %+v / %+v, want stable reset %s", first, second, want)
	}
}
