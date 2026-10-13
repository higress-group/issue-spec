package gates

import (
	"encoding/json"
	"testing"

	"github.com/higress-group/issue-spec/internal/relationships"
)

func TestCompactSummaryBoundsGraphAndPreservesVerdict(t *testing.T) {
	index := relationships.Index{Totals: relationships.Totals{Canonical: 500}, Edges: make([]relationships.Edge, 500), Classifications: make([]relationships.Edge, 500)}
	detail := Remediation{CommandFamily: "status", Arguments: []string{"--proposal", "41", "--json"}}
	for _, ready := range []bool{true, false} {
		summary := ProjectCompactSummary(Report{Ready: ready}, nil, nil, detail, index)
		body, err := json.Marshal(summary)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) > 1500 || summary.OK != ready || summary.Relationships.Totals.Canonical != 500 || !summary.Relationships.DetailsOmitted || summary.Detail.CommandFamily != "status" {
			t.Fatalf("lost verdict or expanded graph: %s", body)
		}
	}
}
