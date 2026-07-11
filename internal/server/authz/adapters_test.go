package authz

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	apierrors "github.com/higress-group/issue-spec/internal/server/api/github/errors"
)

func TestDecisionAdaptersConcealMissingAndInvisibleIdentically(t *testing.T) {
	missing := Decision{Reason: ReasonNotFound}
	invisible := Decision{Exists: true, Reason: ReasonInvisible}
	writeCompatibility := func(decision Decision) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		apiError, ok := decision.CompatibilityError("request-7")
		if !ok {
			t.Fatal("denial did not produce compatibility error")
		}
		apierrors.WriteGitHub(response, apiError)
		return response
	}
	missingResponse := writeCompatibility(missing)
	invisibleResponse := writeCompatibility(invisible)
	if missingResponse.Code != http.StatusNotFound || !bytes.Equal(missingResponse.Body.Bytes(), invisibleResponse.Body.Bytes()) {
		t.Fatalf("missing=%d %q invisible=%d %q", missingResponse.Code, missingResponse.Body.String(), invisibleResponse.Code, invisibleResponse.Body.String())
	}
	if !errors.Is(missing.AuthorizationError(), adminservice.ErrNotFound) || !errors.Is(invisible.AuthorizationError(), adminservice.ErrNotFound) {
		t.Fatal("concealed decisions did not adapt to ErrNotFound")
	}

	visibleDenied := Decision{Exists: true, Visible: true, Reason: ReasonInsufficientPermission}
	problem, ok := visibleDenied.NativeProblem("request-8")
	if !ok || problem.Status != http.StatusForbidden || problem.Code != "forbidden" || problem.RequestID != "request-8" {
		t.Fatalf("problem = %+v, %v", problem, ok)
	}
	if !errors.Is(visibleDenied.AuthorizationError(), adminservice.ErrForbidden) {
		t.Fatal("visible denial did not adapt to ErrForbidden")
	}
}
