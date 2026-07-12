package evidence

import (
	"errors"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/codereview"
)

func TestOrderProviderFactsOrdersSupersessionAndRejectsCycles(t *testing.T) {
	observedAt := time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)
	predecessor := codereview.ProviderFact{ID: "first", ExternalID: "ci", Kind: codereview.EvidenceCheck,
		State: "failed", SubjectRevision: "abc", Name: "ci", ObservedAt: observedAt,
		PayloadDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	successor := predecessor
	successor.ID = "second"
	successor.State = "passed"
	successor.SupersedesID = predecessor.ID
	successor.PayloadDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	ordered, err := orderProviderFacts([]codereview.ProviderFact{successor, predecessor})
	if err != nil || len(ordered) != 2 || ordered[0].ID != predecessor.ID || ordered[1].ID != successor.ID {
		t.Fatalf("orderProviderFacts() = %+v, %v", ordered, err)
	}

	predecessor.SupersedesID = successor.ID
	if _, err := orderProviderFacts([]codereview.ProviderFact{predecessor, successor}); !errors.Is(err, codereview.ErrInvalidProviderData) {
		t.Fatalf("cyclic orderProviderFacts() error = %v", err)
	}
}
