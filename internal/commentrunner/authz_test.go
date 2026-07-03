package commentrunner

import (
	"context"
	"errors"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
)

func TestAuthorizeCandidateAllowsCurrentAuthenticatedUserByDefault(t *testing.T) {
	backend := &fakePermissionBackend{
		user:        github.User{Login: "runner"},
		permissions: map[string]string{"o/r|runner": "write"},
	}
	candidate := CommandCandidate{Repo: "o/r", Commenter: "runner", Verb: VerbNew}

	result := AuthorizeCandidate(context.Background(), backend, candidate, DefaultAuthorizationPolicy())
	if !result.Allowed || result.Reason != AuthReasonAllowed || result.Permission != "write" {
		t.Fatalf("authorization = %+v, want allowed", result)
	}

	result = AuthorizeCandidate(context.Background(), backend, candidate, AuthorizationPolicy{})
	if !result.Allowed || result.Reason != AuthReasonAllowed || result.Permission != "write" {
		t.Fatalf("zero policy authorization = %+v, want allowed", result)
	}
}

func TestAuthorizeCandidateAllowsConfiguredUsersWithWriteEquivalentPermission(t *testing.T) {
	backend := &fakePermissionBackend{
		user: github.User{Login: "runner"},
		permissions: map[string]string{
			"o/r|alice":  "admin",
			"o/r|bob":    "maintain",
			"o/r|carol":  "write",
			"o/r|reader": "read",
		},
	}
	policy := AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice", "bob", "carol"}}
	for _, commenter := range []string{"alice", "bob", "carol"} {
		result := AuthorizeCandidate(context.Background(), backend, CommandCandidate{Repo: "o/r", Commenter: commenter}, policy)
		if !result.Allowed {
			t.Fatalf("%s authorization = %+v, want allowed", commenter, result)
		}
	}
}

func TestAuthorizeCandidateDeniesPolicyAndPermissionFailures(t *testing.T) {
	tests := []struct {
		name       string
		backend    *fakePermissionBackend
		policy     AuthorizationPolicy
		candidate  CommandCandidate
		wantReason AuthorizationReason
	}{
		{
			name:       "runner identity mismatch",
			backend:    &fakePermissionBackend{user: github.User{Login: "other"}},
			policy:     AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice"}},
			candidate:  CommandCandidate{Repo: "o/r", Commenter: "alice"},
			wantReason: AuthReasonRunnerIdentityMismatch,
		},
		{
			name:       "commenter not configured",
			backend:    &fakePermissionBackend{user: github.User{Login: "runner"}},
			policy:     AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice"}},
			candidate:  CommandCandidate{Repo: "o/r", Commenter: "mallory"},
			wantReason: AuthReasonCommenterNotAllowed,
		},
		{
			name:       "read permission insufficient",
			backend:    &fakePermissionBackend{user: github.User{Login: "runner"}, permissions: map[string]string{"o/r|alice": "read"}},
			policy:     AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice"}},
			candidate:  CommandCandidate{Repo: "o/r", Commenter: "alice"},
			wantReason: AuthReasonInsufficientPermission,
		},
		{
			name:       "unknown permission insufficient",
			backend:    &fakePermissionBackend{user: github.User{Login: "runner"}, permissions: map[string]string{"o/r|alice": "triage"}},
			policy:     AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice"}},
			candidate:  CommandCandidate{Repo: "o/r", Commenter: "alice"},
			wantReason: AuthReasonInsufficientPermission,
		},
		{
			name:       "permission lookup failure",
			backend:    &fakePermissionBackend{user: github.User{Login: "runner"}, permissionErr: errors.New("boom")},
			policy:     AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice"}},
			candidate:  CommandCandidate{Repo: "o/r", Commenter: "alice"},
			wantReason: AuthReasonPermissionLookupFailed,
		},
		{
			name:       "runner lookup failure",
			backend:    &fakePermissionBackend{userErr: errors.New("boom")},
			policy:     AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice"}},
			candidate:  CommandCandidate{Repo: "o/r", Commenter: "alice"},
			wantReason: AuthReasonRunnerIdentityLookupFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AuthorizeCandidate(context.Background(), tt.backend, tt.candidate, tt.policy)
			if result.Allowed || result.Reason != tt.wantReason {
				t.Fatalf("authorization = %+v, want denied %s", result, tt.wantReason)
			}
		})
	}
}

func TestAuthorizeCandidateForRepoUsesSessionBoundRepository(t *testing.T) {
	backend := &fakePermissionBackend{
		user:        github.User{Login: "runner"},
		permissions: map[string]string{"session/repo|alice": "write"},
	}
	candidate := CommandCandidate{Repo: "trigger/repo", Commenter: "alice", Verb: VerbResume, PublicSessionID: "sess-123"}
	policy := AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice"}}

	result := AuthorizeCandidateForRepo(context.Background(), backend, candidate, "session/repo", policy)
	if !result.Allowed {
		t.Fatalf("authorization = %+v, want allowed", result)
	}
	if backend.lastRepo != "session/repo" {
		t.Fatalf("permission checked repo %q, want session/repo", backend.lastRepo)
	}
}

type fakePermissionBackend struct {
	user          github.User
	userErr       error
	permissions   map[string]string
	permissionErr error
	lastRepo      string
}

func (f *fakePermissionBackend) GetUser(context.Context) (github.User, []string, error) {
	if f.userErr != nil {
		return github.User{}, nil, f.userErr
	}
	return f.user, nil, nil
}

func (f *fakePermissionBackend) GetCollaboratorPermission(_ context.Context, repo, username string) (github.CollaboratorPermissionResult, error) {
	f.lastRepo = repo
	if f.permissionErr != nil {
		return github.CollaboratorPermissionResult{}, f.permissionErr
	}
	permission := github.CollaboratorPermission{Permission: f.permissions[repo+"|"+username]}
	return github.CollaboratorPermissionResult{Permission: permission}, nil
}
