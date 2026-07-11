package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestNativeResolverPinsAuthorizedServerBinding(t *testing.T) {
	scope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	bindingID := uuid.NewString()
	resolver := NativeResolver{Bindings: fakeNativeBindings{binding: github.NativeBinding{ID: bindingID,
		ProviderKey: "github", ExternalRepositoryID: "acme/widgets", CloneURL: "https://code.example/acme/widgets.git",
		WebURL: "https://code.example/acme/widgets", DefaultBranch: "main", Version: 4, Active: true}},
		Scopes: map[string]models.RepoScope{"owner/repo": scope}}
	resolution, err := resolver.ResolveRepository(t.Context(), "OWNER/REPO")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Binding.Source != SourceServer || resolution.Binding.BindingID != bindingID ||
		resolution.Binding.IssueRepositoryKey != "owner/repo" || resolution.CloneURL != "https://code.example/acme/widgets.git" {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestNativeResolverFailsClosedWithoutAuthorizedActiveBinding(t *testing.T) {
	scope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	for _, test := range []struct {
		name     string
		resolver NativeResolver
	}{
		{name: "missing scope", resolver: NativeResolver{Bindings: fakeNativeBindings{}}},
		{name: "binding unavailable", resolver: NativeResolver{Bindings: fakeNativeBindings{err: errors.New("not found")},
			Scopes: map[string]models.RepoScope{"owner/repo": scope}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.resolver.ResolveRepository(t.Context(), "owner/repo")
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "binding") {
				t.Fatalf("fail-closed error=%v", err)
			}
		})
	}
}

type fakeNativeBindings struct {
	binding github.NativeBinding
	err     error
}

func (f fakeNativeBindings) GetNativeActiveBinding(context.Context, models.RepoScope) (github.NativeBinding, error) {
	return f.binding, f.err
}
