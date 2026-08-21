package repository

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestOrganizationRegistryDiscoversAndRevalidatesAuthoritativeRepository(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	source := &registryNative{page: registryPage(orgID, repoID, RunnerTriggerAction)}
	binding := registryBinding()
	registry, err := NewOrganizationRegistry(source, &registryBindings{binding: binding}, orgID, "Owner")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := registry.ResolveScope(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID})
	if err != nil || entry.Repository != "Owner/repo" || entry.Scope.RepoID != repoID || entry.Binding.ID != binding.ID {
		t.Fatalf("entry=%+v err=%v", entry, err)
	}
	entry, err = registry.ResolveRepository(t.Context(), "owner/REPO")
	if err != nil || entry.Scope.RepoID != repoID || source.calls != 2 {
		t.Fatalf("revalidated entry=%+v calls=%d err=%v", entry, source.calls, err)
	}
	source.page.Repositories[0].AllowedActions = nil
	if _, err := registry.ResolveRepository(t.Context(), "owner/repo"); !IsRepositoryIneligible(err) {
		t.Fatalf("permission drift err=%v", err)
	}
}

func TestOrganizationRegistryReconstructsByNameAfterRestartAndRejectsBindingDrift(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	bindings := &registryBindings{binding: registryBinding()}
	registry, err := NewOrganizationRegistry(&registryNative{page: registryPage(orgID, repoID, RunnerTriggerAction)},
		bindings, orgID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := registry.ResolveRepository(t.Context(), "owner/repo")
	if err != nil || entry.Scope != (models.RepoScope{OrgID: orgID, RepoID: repoID}) {
		t.Fatalf("reconstructed entry=%+v err=%v", entry, err)
	}
	bindings.err = &github.APIError{StatusCode: http.StatusNotFound}
	if _, err := registry.ResolveRepository(t.Context(), "owner/repo"); !IsRepositoryIneligible(err) {
		t.Fatalf("binding drift err=%v", err)
	}
}

func TestStaticRegistryPreservesExplicitRepositoryAllowList(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	scope := models.RepoScope{OrgID: orgID, RepoID: repoID}
	registry := &StaticRegistry{Bindings: &registryBindings{binding: registryBinding()},
		Scopes: map[string]models.RepoScope{"Owner/Repo": scope}}
	entry, err := registry.ResolveRepository(t.Context(), "owner/repo")
	if err != nil || entry.Repository != "Owner/Repo" || entry.Scope != scope {
		t.Fatalf("entry=%+v err=%v", entry, err)
	}
	if _, err := registry.ResolveRepository(t.Context(), "owner/other"); !IsRepositoryIneligible(err) {
		t.Fatalf("out-of-scope repository err=%v", err)
	}
}

func TestOrganizationRegistrySerializesConcurrentFirstDiscovery(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	registry, err := NewOrganizationRegistry(&registryNative{page: registryPage(orgID, repoID, RunnerTriggerAction)},
		&registryBindings{binding: registryBinding()}, orgID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 12
	var wg sync.WaitGroup
	results := make(chan RegistryEntry, workers)
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, err := registry.ResolveScope(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID})
			results <- entry
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for entry := range results {
		if entry.Repository != "owner/repo" || entry.Scope.RepoID != repoID {
			t.Fatalf("entry=%+v", entry)
		}
	}
}

func TestOrganizationRegistryClassifiesAuthorityFailures(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	for _, test := range []struct {
		name       string
		listErr    error
		bindingErr error
		ineligible bool
	}{
		{name: "missing binding", bindingErr: &github.APIError{StatusCode: http.StatusNotFound}, ineligible: true},
		{name: "unauthenticated list", listErr: &github.APIError{StatusCode: http.StatusUnauthorized}, ineligible: true},
		{name: "forbidden list", listErr: &github.APIError{StatusCode: http.StatusForbidden}, ineligible: true},
		{name: "server unavailable", listErr: &github.APIError{StatusCode: http.StatusServiceUnavailable}},
		{name: "network unavailable", listErr: errors.New("dial unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, _ := NewOrganizationRegistry(&registryNative{page: registryPage(orgID, repoID, RunnerTriggerAction), err: test.listErr},
				&registryBindings{binding: registryBinding(), err: test.bindingErr}, orgID, "owner")
			_, err := registry.ResolveScope(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID})
			if err == nil || IsRepositoryIneligible(err) != test.ineligible {
				t.Fatalf("err=%v ineligible=%v", err, test.ineligible)
			}
		})
	}
}

type registryNative struct {
	mu    sync.Mutex
	page  github.NativeRepositoriesContext
	err   error
	calls int
}

func (f *registryNative) GetNativeContext(context.Context) (github.NativeContext, error) {
	return github.NativeContext{}, nil
}
func (f *registryNative) ListNativeContextRepositories(context.Context, string) (github.NativeRepositoriesContext, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.page, f.err
}

type registryBindings struct {
	binding github.NativeBinding
	err     error
}

func (f *registryBindings) GetNativeActiveBinding(context.Context, models.RepoScope) (github.NativeBinding, error) {
	return f.binding, f.err
}

func registryPage(orgID, repoID uuid.UUID, actions ...string) github.NativeRepositoriesContext {
	return github.NativeRepositoriesContext{Repositories: []github.NativeRepositoryContext{{
		Repository:     github.NativeRepositorySummary{ID: repoID.String(), OrganizationID: orgID.String(), Name: "repo"},
		AllowedActions: actions,
	}}}
}

func registryBinding() github.NativeBinding {
	return github.NativeBinding{ID: uuid.NewString(), Version: 1, Active: true, ProviderKey: "github",
		ExternalRepositoryID: "1", CloneURL: "https://example.test/repo.git", WebURL: "https://example.test/repo", DefaultBranch: "main"}
}
