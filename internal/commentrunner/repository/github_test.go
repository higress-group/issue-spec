package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
)

func TestGitHubResolverReturnsCompleteLiveOperatorSnapshot(t *testing.T) {
	metadata := &fakeGitHubMetadata{result: github.RepositoryResult{Repository: validGitHubRepository()}}
	resolver := GitHubResolver{Metadata: metadata}

	resolution, err := resolver.ResolveRepository(t.Context(), "ACME/Widgets")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.calls != 1 || metadata.repositories[0] != "acme/widgets" {
		t.Fatalf("metadata lookups = %v", metadata.repositories)
	}
	if resolution.Repo != "acme/widgets" || resolution.CloneURL != "https://github.com/acme/widgets.git" ||
		resolution.DefaultBranch != "main" || resolution.Ref != "main" || resolution.Binding.Source != SourceOperator ||
		resolution.Binding.BindingID != "github-repository:7301" || resolution.Binding.Version != 1 ||
		resolution.Binding.ProviderKey != "github" || resolution.Binding.ExternalRepositoryID != "7301" ||
		resolution.Binding.CloneURL != resolution.CloneURL || resolution.Binding.WebURL != "https://github.com/acme/widgets" ||
		resolution.Binding.DefaultBranch != "main" {
		t.Fatalf("resolution = %+v", resolution)
	}

	metadata.result.Repository.DefaultBranch = "trunk"
	current, err := resolver.ResolveRepository(t.Context(), "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.calls != 2 || current.DefaultBranch != "trunk" || DiagnosticCode(ValidatePinned(resolution.Binding, current.Binding)) != DiagnosticBindingDrift {
		t.Fatalf("live resolution did not expose drift: first=%+v current=%+v calls=%d", resolution, current, metadata.calls)
	}
}

func TestGitHubResolverFailsClosedForUntrustedOrIncompleteMetadata(t *testing.T) {
	apiErr := errors.New("authenticated lookup failed")
	tests := []struct {
		name    string
		mutate  func(*github.Repository)
		apiErr  error
		wantErr string
	}{
		{name: "api error", apiErr: apiErr, wantErr: "authenticated lookup failed"},
		{name: "missing full identity", mutate: func(repo *github.Repository) { repo.FullName = "" }, wantErr: "full identity"},
		{name: "identity mismatch", mutate: func(repo *github.Repository) { repo.FullName = "attacker/other" }, wantErr: "does not match"},
		{name: "invalid id", mutate: func(repo *github.Repository) { repo.ID = 0 }, wantErr: "positive stable repository id"},
		{name: "missing default branch", mutate: func(repo *github.Repository) { repo.DefaultBranch = "" }, wantErr: "snapshot is incomplete"},
		{name: "credentialed clone url", mutate: func(repo *github.Repository) { repo.CloneURL = "https://secret@github.com/acme/widgets.git" }, wantErr: "safe hierarchical coordinate"},
		{name: "unsafe clone scheme", mutate: func(repo *github.Repository) { repo.CloneURL = "http://github.com/acme/widgets.git" }, wantErr: "clone URL scheme"},
		{name: "unsafe web scheme", mutate: func(repo *github.Repository) { repo.HTMLURL = "http://github.com/acme/widgets" }, wantErr: "web URL scheme"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := validGitHubRepository()
			if tc.mutate != nil {
				tc.mutate(&repo)
			}
			metadata := &fakeGitHubMetadata{result: github.RepositoryResult{Repository: repo}, err: tc.apiErr}
			_, err := (GitHubResolver{Metadata: metadata}).ResolveRepository(t.Context(), "acme/widgets")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestGitHubResolverRequiresMetadataCapability(t *testing.T) {
	_, err := (GitHubResolver{}).ResolveRepository(t.Context(), "acme/widgets")
	if err == nil || !strings.Contains(err.Error(), "metadata operations are required") {
		t.Fatalf("error = %v", err)
	}
}

type fakeGitHubMetadata struct {
	result       github.RepositoryResult
	err          error
	calls        int
	repositories []string
}

func (f *fakeGitHubMetadata) GetRepository(_ context.Context, repository string) (github.RepositoryResult, error) {
	f.calls++
	f.repositories = append(f.repositories, repository)
	return f.result, f.err
}

func validGitHubRepository() github.Repository {
	return github.Repository{ID: 7301, FullName: "Acme/Widgets", CloneURL: "https://github.com/acme/widgets.git",
		HTMLURL: "https://github.com/acme/widgets", DefaultBranch: "main"}
}
