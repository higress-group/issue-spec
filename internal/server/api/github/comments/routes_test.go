package comments

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	issueapi "github.com/higress-group/issue-spec/internal/server/api/github/issues"
)

func TestRouteSetRequiresServiceAndUsesOneUnambiguousTailPerMethod(t *testing.T) {
	if _, err := NewRouteSet(Dependencies{}); err == nil {
		t.Fatal("nil service was accepted")
	}
	set, err := NewRouteSet(Dependencies{Service: &issueapi.Service{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Routes) != 4 {
		t.Fatalf("routes = %d, want one GET/POST/PATCH/DELETE tail dispatcher", len(set.Routes))
	}
}

func typedMarkerBody(t *testing.T, version string) string {
	t.Helper()
	return `{"body":"<!-- issue-spec:type=QUESTION id=QUESTION-1004 version=` + version +
		` -->\nAgent: Coordinator\nType: QUESTION\nID: QUESTION-1004\nStatus: blocked\nScope: example-scope\n"}`
}

func TestCommentWritesRejectNonSchemaOneMarkerVersions(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/repos/acme/widgets/issues/1/comments",
			strings.NewReader(typedMarkerBody(t, "2")))
		request.SetPathValue("number", "1")
		response := httptest.NewRecorder()
		handlers{}.create(response, request)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want %d (%s)", response.Code, http.StatusUnprocessableEntity, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), "invalid_marker_version") {
			t.Fatalf("body %q does not carry the invalid_marker_version code", response.Body.String())
		}
	})
	t.Run("update", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPatch, "/repos/acme/widgets/issues/comments/7",
			strings.NewReader(typedMarkerBody(t, "3")))
		request.SetPathValue("comment", "7")
		response := httptest.NewRecorder()
		handlers{}.update(response, request)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want %d (%s)", response.Code, http.StatusUnprocessableEntity, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), "invalid_marker_version") {
			t.Fatalf("body %q does not carry the invalid_marker_version code", response.Body.String())
		}
	})
}
