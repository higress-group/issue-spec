package auth

import (
	"net/http"
	"testing"

	"github.com/higress-group/issue-spec/internal/capability"
)

func TestDelegatedRequestOperationScope(t *testing.T) {
	principal := Principal{Kind: CredentialDelegated, Scopes: []string{"issues:write"},
		Operations: []string{string(capability.OperationIssueRead), string(capability.OperationIssueCommentWrite)}}
	for _, test := range []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodGet, "/repos/o/r/issues/1", true},
		{http.MethodPost, "/repos/o/r/issues/1/comments", true},
		{http.MethodPatch, "/repos/o/r/issues/comments/9", true},
		{http.MethodPatch, "/repos/o/r/issues/1", false},
		{http.MethodPost, "/repos/o/r/labels", false},
		{http.MethodPost, "/repos/o/r/issues/comments/9/reactions", true},
	} {
		if got := delegatedRequestAllowed(principal, test.method, test.path); got != test.allowed {
			t.Fatalf("%s %s allowed=%t, want %t", test.method, test.path, got, test.allowed)
		}
	}
	legacy := Principal{Kind: CredentialDelegated, Scopes: []string{"issues:write"}}
	if !delegatedRequestAllowed(legacy, http.MethodPatch, "/repos/o/r/issues/1") {
		t.Fatal("short-lived legacy delegated token lost compatibility before expiry")
	}
}
