package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestClientGetsCollaboratorPermission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/o/r/collaborators/alice/permission" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization header = %q", got)
		}
		json.NewEncoder(w).Encode(CollaboratorPermission{Permission: "maintain", RoleName: "maintain"})
	}))
	defer server.Close()

	client := NewClientWithBaseURL("github.com", server.URL, "token", server.Client())
	permission, err := client.GetCollaboratorPermission(context.Background(), "o/r", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if permission.Permission != "maintain" || permission.RoleName != "maintain" {
		t.Fatalf("permission = %+v", permission)
	}
}

func TestGHBackendGetsCollaboratorPermission(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stdout: []byte(`{"permission":"write","role_name":"write"}`)},
	}
	backend, err := NewGHBackend(GHBackendOptions{Host: "ghe.example.com", CLIOptions: GHCLIOptions{Runner: runner}})
	if err != nil {
		t.Fatal(err)
	}

	permission, err := backend.GetCollaboratorPermission(context.Background(), "o/r", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if permission.Permission != "write" {
		t.Fatalf("permission = %+v", permission)
	}
	wantEndpoint := "/repos/o/r/collaborators/" + url.PathEscape("alice@example.com") + "/permission"
	wantArgs := []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--hostname", "ghe.example.com", wantEndpoint}
	if !reflect.DeepEqual(runner.command.Args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", runner.command.Args, wantArgs)
	}
}
