package subscriptions

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/authz"
)

func TestMutationValidationClassifiesPublicReasonsAndFields(t *testing.T) {
	tests := []struct {
		name   string
		run    func() error
		reason ValidationReason
		field  ValidationField
	}{
		{"destination syntax", func() error {
			_, _, err := splitDestination(" https://example.test/hook")
			return err
		}, ValidationInvalidDestinationURL, ValidationFieldURL},
		{"destination policy", func() error {
			return validateURL("http://example.test/hook", true, false)
		}, ValidationDestinationDenied, ValidationFieldURL},
		{"event type", func() error {
			return validateEventTypes([]string{"issue_comment.cretaed"})
		}, ValidationInvalidEventType, ValidationFieldEventTypes},
		{"delivery format", func() error {
			return validatePolicy("unknown", SigningModeNone, ContentPolicy{}, []string{"issue.created"})
		}, ValidationInvalidDeliveryPolicy, ValidationFieldDeliveryFormat},
		{"signing mode", func() error {
			return validatePolicy(DeliveryFormatIssueSpecV1, SigningModeNone, ContentPolicy{}, []string{"issue.created"})
		}, ValidationInvalidDeliveryPolicy, ValidationFieldSigningMode},
		{"content policy", func() error {
			return validatePolicy(DeliveryFormatGitHubV3, SigningModeNone, ContentPolicy{}, []string{"issue.created"})
		}, ValidationInvalidDeliveryPolicy, ValidationFieldContentPolicy},
		{"retry attempts", func() error {
			return validateRetry(RetryPolicy{MaxAttempts: 101, InitialBackoff: time.Second, MaxBackoff: time.Minute})
		}, ValidationInvalidRetryPolicy, ValidationFieldRetryMaxAttempts},
		{"retry initial", func() error {
			return validateRetry(RetryPolicy{MaxAttempts: 3, InitialBackoff: -time.Second, MaxBackoff: time.Minute})
		}, ValidationInvalidRetryPolicy, ValidationFieldRetryInitialBackoff},
		{"retry maximum", func() error {
			return validateRetry(RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Minute, MaxBackoff: time.Second})
		}, ValidationInvalidRetryPolicy, ValidationFieldRetryMaxBackoff},
		{"destination query", func() error {
			_, _, err := splitDestination("https://example.test/hook?")
			return err
		}, ValidationInvalidDestinationQuery, ValidationFieldURL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Reason != test.reason || validation.Field != test.field {
				t.Fatalf("error=%v validation=%+v", err, validation)
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("validation error lost ErrInvalidInput compatibility: %v", err)
			}
		})
	}
}

func TestCreateAndUpdateReturnTheSameActionableValidationContract(t *testing.T) {
	tests := []struct {
		name         string
		reason       ValidationReason
		field        ValidationField
		mutateCreate func(*CreateInput)
		mutateUpdate func(*UpdateInput)
	}{
		{"destination url", ValidationInvalidDestinationURL, ValidationFieldURL,
			func(input *CreateInput) { input.URL = "https://user:secret@example.test/hook" },
			func(input *UpdateInput) { input.URL = "https://user:secret@example.test/hook" }},
		{"destination denied", ValidationDestinationDenied, ValidationFieldURL,
			func(input *CreateInput) { input.URL = "http://example.test/hook" },
			func(input *UpdateInput) { input.URL = "http://example.test/hook" }},
		{"event type", ValidationInvalidEventType, ValidationFieldEventTypes,
			func(input *CreateInput) { input.EventTypes = []string{"issue_comment.cretaed"} },
			func(input *UpdateInput) { input.EventTypes = []string{"issue_comment.cretaed"} }},
		{"delivery policy", ValidationInvalidDeliveryPolicy, ValidationFieldDeliveryFormat,
			func(input *CreateInput) { input.DeliveryFormat = "unknown" },
			func(input *UpdateInput) { input.DeliveryFormat = "unknown" }},
		{"retry policy", ValidationInvalidRetryPolicy, ValidationFieldRetryMaxAttempts,
			func(input *CreateInput) { input.Retry.MaxAttempts = -1 },
			func(input *UpdateInput) { input.Retry.MaxAttempts = -1 }},
		{"destination query", ValidationInvalidDestinationQuery, ValidationFieldURL,
			func(input *CreateInput) { input.URL = "https://example.test/hook?" },
			func(input *UpdateInput) { input.URL = "https://example.test/hook?" }},
	}
	service := &Service{config: Config{Production: true}}
	actor := Actor{UserID: uuid.New(), IdentityKey: "session:test", RequestID: "request-222"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			createInput := CreateInput{OrganizationID: uuid.New(), URL: "https://example.test/hook",
				EventTypes: []string{"issue_comment.created"}}
			test.mutateCreate(&createInput)
			_, createErr := service.Create(t.Context(), actor, authz.Subject{}, createInput)
			assertValidationError(t, createErr, test.reason, test.field)

			updateInput := UpdateInput{ExpectedVersion: 1, URL: "https://example.test/hook", Active: true,
				EventTypes: []string{"issue_comment.created"}}
			test.mutateUpdate(&updateInput)
			_, updateErr := service.Update(t.Context(), actor, authz.Subject{}, uuid.New(), uuid.New(), updateInput)
			assertValidationError(t, updateErr, test.reason, test.field)
		})
	}
}

func assertValidationError(t *testing.T, err error, reason ValidationReason, field ValidationField) {
	t.Helper()
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Reason != reason || validation.Field != field {
		t.Fatalf("error=%v validation=%+v want reason=%s field=%s", err, validation, reason, field)
	}
}

func TestGitHubEventTypesAreValidatedBeforeDerivation(t *testing.T) {
	policy := ContentPolicy{IssueActions: []string{"opened"}, CommentActions: []string{},
		IssueKinds: []string{"ordinary"}, ActorClasses: []string{"human"}}
	_, _, _, _, err := normalizePolicy(DeliveryFormatGitHubV3, SigningModeNone, policy,
		[]string{"issue_comment.cretaed"})
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Reason != ValidationInvalidEventType ||
		validation.Field != ValidationFieldEventTypes {
		t.Fatalf("misspelled event error=%v validation=%+v", err, validation)
	}

	_, _, _, derived, err := normalizePolicy(DeliveryFormatGitHubV3, SigningModeNone, policy,
		[]string{"issue.created"})
	if err != nil || len(derived) != 1 || derived[0] != "issue.created" {
		t.Fatalf("matching derived events=%v error=%v", derived, err)
	}
}

func TestCreateValidationKeepsScopeErrorsGeneric(t *testing.T) {
	_, err := validateCreate(CreateInput{OrganizationID: uuid.Nil}, true, false)
	var validation *ValidationError
	if !errors.Is(err, ErrInvalidInput) || errors.As(err, &validation) {
		t.Fatalf("scope validation should remain generic: %v", err)
	}
}
