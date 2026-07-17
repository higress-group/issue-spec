package store

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestRepositoryNotificationStoreRejectsUnscopedAndNonTransactionalMutation(t *testing.T) {
	if _, _, err := (RepoStore{}).SetManualRepositorySubscription(t.Context(), uuid.New(), true); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unscoped mutation error = %v", err)
	}
}
