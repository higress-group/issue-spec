package auth

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	mailservice "github.com/higress-group/issue-spec/internal/server/profilemail"
)

func TestPrivateProfileMergesNotificationEmailWithoutProviderFallback(t *testing.T) {
	userID := uuid.New()
	pendingID := uuid.New()
	verified := "verified@example.test"
	completed, verifiedAt := time.Now().Add(-time.Hour), time.Now().Add(-time.Minute)
	h := handlers{deps: Dependencies{EmailEnabled: true, ProfileMail: profileReader{profile: mailservice.Profile{
		UserID: userID, OnboardingCompletedAt: &completed, NotificationEmail: &verified,
		NotificationVerifiedAt: &verifiedAt, RepresentationVersion: 7,
		Pending: &mailservice.Verification{ID: pendingID, PendingEmail: "replacement@example.test",
			ExpiresAt: time.Now().Add(time.Hour), RepresentationVersion: 2},
	}}}, canonicalWebOrigin: "https://web.example.test"}
	response, err := h.privateProfileResponse(context.Background(), serverauth.Profile{ID: userID, Login: "person",
		DisplayName: "Person", IdentityDisplayName: "Provider Person", RepresentationVersion: 6})
	if err != nil {
		t.Fatal(err)
	}
	if response["notification_email"] != &verified || response["notification_email_available"] != true ||
		response["onboarding_completed"] != true || response["representation_version"] != int64(7) {
		t.Fatalf("private profile = %+v", response)
	}
	pending, ok := response["pending_notification_email"].(map[string]any)
	if !ok || pending["email"] != "replacement@example.test" {
		t.Fatalf("pending profile = %+v", response["pending_notification_email"])
	}
	if _, exposed := response["email"]; exposed {
		t.Fatalf("provider email was exposed as notification email: %+v", response)
	}
}

func TestPrivateProfileSafelyDegradesWhenEmailCapabilityIsDisabled(t *testing.T) {
	h := handlers{deps: Dependencies{EmailEnabled: false}, canonicalWebOrigin: "https://web.example.test"}
	response, err := h.privateProfileResponse(context.Background(), serverauth.Profile{ID: uuid.New(), Login: "person",
		DisplayName: "Person", IdentityDisplayName: "Provider Person", RepresentationVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if response["notification_email_available"] != false || response["onboarding_completed"] != true ||
		response["notification_email"] != nil || response["pending_notification_email"] != nil {
		t.Fatalf("disabled private profile = %+v", response)
	}
}

type profileReader struct{ profile mailservice.Profile }

func (r profileReader) Get(context.Context, uuid.UUID) (mailservice.Profile, error) {
	return r.profile, nil
}

var _ ProfileMailReader = profileReader{}
