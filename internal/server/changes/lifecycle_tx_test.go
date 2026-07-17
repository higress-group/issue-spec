package changes

import "testing"

func TestLifecycleSnapshotUsesCanonicalLifecycleValues(t *testing.T) {
	for _, lifecycle := range []Lifecycle{LifecycleActive, LifecycleBlocked, LifecycleClosed, LifecycleCompleted} {
		snapshot := LifecycleSnapshot{ChangeKey: "change", Lifecycle: lifecycle}
		if snapshot.ChangeKey == "" || snapshot.Lifecycle == "" {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	}
}
