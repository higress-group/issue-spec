package admin

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestCreateOrganizationRejectsReservedRepositoryOwnersBeforePersistence(t *testing.T) {
	service := &Service{}
	actor := Actor{UserID: uuid.New(), IdentityKey: "user:test", RequestID: "reserved-owner"}
	for _, name := range []string{"users", "Issues", "_repos"} {
		_, err := service.CreateOrganization(t.Context(), actor, CreateOrganizationInput{
			Name: name, DisplayName: name, BasePermission: models.BasePermissionRead,
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("CreateOrganization(%q) error = %v, want ErrInvalidInput", name, err)
		}
	}
}
