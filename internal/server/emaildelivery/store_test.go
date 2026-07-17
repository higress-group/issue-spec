package emaildelivery

import (
	"encoding/json"
	"testing"
)

func TestEqualSnapshotUsesJSONSemantics(t *testing.T) {
	t.Parallel()
	left := json.RawMessage(`{"short":1,"nested":{"value":true},"longer":[1,2]}`)
	right := json.RawMessage(`{ "longer": [1, 2], "nested": {"value": true}, "short": 1.0 }`)
	if !equalSnapshot(left, right) {
		t.Fatal("equivalent JSON snapshots were treated as different")
	}
	if equalSnapshot(left, json.RawMessage(`{"short":2}`)) {
		t.Fatal("different JSON snapshots were treated as equivalent")
	}
}
