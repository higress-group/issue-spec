package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCollaboratorPermission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/o/r/collaborators/alice/permission" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"permission":"write"}`))
	}))
	defer server.Close()

	c := NewClientWithBaseURL("github.com", server.URL, "token", server.Client())
	perm, err := c.CollaboratorPermission(context.Background(), "o/r", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if perm != "write" {
		t.Fatalf("perm = %q", perm)
	}
}
