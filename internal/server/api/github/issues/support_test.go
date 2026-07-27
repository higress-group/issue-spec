package issues

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/server/projection/artifacts"
	"github.com/higress-group/issue-spec/internal/server/store"
)

func TestWriteErrorMapsTypedProjectionConflict(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/repos/acme/widgets/issues/2/comments", nil)
	request.Header.Set(requestIDHeader, "request-typed-conflict")
	response := httptest.NewRecorder()

	WriteError(response, request, &artifacts.TypedCommentConflictError{
		ID: "QUESTION-1001", SuggestedID: "QUESTION-2001",
	})

	if response.Code != http.StatusConflict ||
		response.Header().Get(requestIDHeader) != "request-typed-conflict" ||
		!strings.Contains(response.Body.String(), "retry with QUESTION-2001") ||
		!strings.Contains(response.Body.String(), `"code":"typed_id_already_exists"`) {
		t.Fatalf("projection conflict status=%d headers=%v body=%s",
			response.Code, response.Header(), response.Body.String())
	}
}

func TestWriteErrorMapsProjectionConflictWithoutSuggestion(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/repos/acme/widgets/issues/2/comments", nil)
	response := httptest.NewRecorder()

	WriteError(response, request, store.ErrProjectionConflict)

	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "Typed comment ID already exists in this repository") {
		t.Fatalf("projection conflict status=%d body=%s", response.Code, response.Body.String())
	}
}
