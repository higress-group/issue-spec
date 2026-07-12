package repos

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
)

func TestCollaboratorCollectionSerializesEmptyArray(t *testing.T) {
	recorder := httptest.NewRecorder()
	adminapi.WriteJSON(recorder, http.StatusOK, map[string]any{
		"collaborators": collaboratorCollection(nil),
	})

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if got := string(payload["collaborators"]); got != "[]" {
		t.Fatalf("collaborators JSON = %s, want []", got)
	}
}
