package webhook

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestResolveRepositoryScopesIsExactUniqueAndFullyCovered(t *testing.T) {
	orgID, repoA, repoB := uuid.New(), uuid.New(), uuid.New()
	source := &fakeNativeContext{current: github.NativeContext{Organizations: []github.NativeOrganizationContext{{ID: orgID.String(), Name: "owner"}}},
		repositories: map[string]github.NativeRepositoriesContext{orgID.String(): {Repositories: []github.NativeRepositoryContext{
			{Repository: github.NativeRepositorySummary{ID: repoA.String(), OrganizationID: orgID.String(), Name: "one"}},
			{Repository: github.NativeRepositorySummary{ID: repoB.String(), OrganizationID: orgID.String(), Name: "two"}},
		}}}}
	resolved, err := ResolveRepositoryScopes(t.Context(), source, []string{"owner/one", "owner/two"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.ByRepository) != 2 || len(resolved.ByScope) != 2 || source.currentCalls != 1 || source.repositoryCalls != 1 {
		t.Fatalf("resolved=%+v calls=%d/%d", resolved, source.currentCalls, source.repositoryCalls)
	}
	if repository, ok := resolved.Repository(models.RepoScope{OrgID: orgID, RepoID: repoB}); !ok || repository != "owner/two" {
		t.Fatalf("reverse scope lookup=%q,%v", repository, ok)
	}
}

func TestResolveRepositoryScopesFailsClosedOnMissingAmbiguousOrCrossOrg(t *testing.T) {
	orgID := uuid.New()
	base := github.NativeContext{Organizations: []github.NativeOrganizationContext{{ID: orgID.String(), Name: "owner"}}}
	for name, source := range map[string]*fakeNativeContext{
		"missing": {current: base, repositories: map[string]github.NativeRepositoriesContext{orgID.String(): {}}},
		"ambiguous": {current: base, repositories: map[string]github.NativeRepositoriesContext{orgID.String(): {Repositories: []github.NativeRepositoryContext{
			{Repository: github.NativeRepositorySummary{ID: uuid.NewString(), OrganizationID: orgID.String(), Name: "repo"}},
			{Repository: github.NativeRepositorySummary{ID: uuid.NewString(), OrganizationID: orgID.String(), Name: "REPO"}},
		}}}},
		"cross-org": {current: base, repositories: map[string]github.NativeRepositoriesContext{orgID.String(): {Repositories: []github.NativeRepositoryContext{
			{Repository: github.NativeRepositorySummary{ID: uuid.NewString(), OrganizationID: uuid.NewString(), Name: "repo"}},
		}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveRepositoryScopes(t.Context(), source, []string{"owner/repo"}, nil); err == nil {
				t.Fatal("unsafe scope resolution succeeded")
			}
		})
	}
}

func TestResolveRepositoryScopesTestInjectionRequiresExactCoverage(t *testing.T) {
	scope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	resolved, err := ResolveRepositoryScopes(t.Context(), nil, []string{"owner/repo"}, map[string]models.RepoScope{"OWNER/REPO": scope})
	if err != nil || resolved.ByRepository["owner/repo"] != scope {
		t.Fatalf("injected resolution=%+v err=%v", resolved, err)
	}
	_, err = ResolveRepositoryScopes(t.Context(), nil, []string{"owner/repo"}, map[string]models.RepoScope{
		"owner/repo": scope, "extra/repo": {OrgID: uuid.New(), RepoID: uuid.New()},
	})
	if err == nil || !strings.Contains(err.Error(), "exactly cover") {
		t.Fatalf("extra injected scope error=%v", err)
	}
}

type fakeNativeContext struct {
	current         github.NativeContext
	repositories    map[string]github.NativeRepositoriesContext
	currentCalls    int
	repositoryCalls int
}

func (f *fakeNativeContext) GetNativeContext(context.Context) (github.NativeContext, error) {
	f.currentCalls++
	return f.current, nil
}

func (f *fakeNativeContext) ListNativeContextRepositories(_ context.Context, organizationID string) (github.NativeRepositoriesContext, error) {
	f.repositoryCalls++
	return f.repositories[organizationID], nil
}
