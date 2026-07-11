package webhook

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type RepositoryScopes struct {
	ByRepository map[string]models.RepoScope
	ByScope      map[models.RepoScope]string
}

func (s RepositoryScopes) Repository(scope models.RepoScope) (string, bool) {
	repository, ok := s.ByScope[scope]
	return repository, ok
}

// ResolveRepositoryScopes resolves configured owner/name values through the
// origin-bound native context API. injected is reserved for hermetic tests;
// production callers pass nil and cannot derive a scope from webhook data.
func ResolveRepositoryScopes(ctx context.Context, source github.NativeContextOperations, repositories []string,
	injected map[string]models.RepoScope) (RepositoryScopes, error) {
	requested, err := normalizeRequestedRepositories(repositories)
	if err != nil {
		return RepositoryScopes{}, err
	}
	if injected != nil {
		return validateInjectedScopes(requested, injected)
	}
	if source == nil {
		return RepositoryScopes{}, errors.New("native context source is required")
	}
	current, err := source.GetNativeContext(ctx)
	if err != nil {
		return RepositoryScopes{}, fmt.Errorf("resolve runner native context: %w", err)
	}
	organizations := make(map[string][]github.NativeOrganizationContext)
	for _, organization := range current.Organizations {
		key := strings.ToLower(strings.TrimSpace(organization.Name))
		if key != "" {
			organizations[key] = append(organizations[key], organization)
		}
	}
	byRepository := make(map[string]models.RepoScope, len(requested))
	for owner, wanted := range requestedByOwner(requested) {
		matches := organizations[strings.ToLower(owner)]
		if len(matches) != 1 {
			return RepositoryScopes{}, fmt.Errorf("configured repository owner %q resolved to %d organizations", owner, len(matches))
		}
		organizationID, err := uuid.Parse(strings.TrimSpace(matches[0].ID))
		if err != nil || organizationID == uuid.Nil {
			return RepositoryScopes{}, fmt.Errorf("organization %q returned an invalid UUID", owner)
		}
		page, err := source.ListNativeContextRepositories(ctx, organizationID.String())
		if err != nil {
			return RepositoryScopes{}, fmt.Errorf("list repositories for organization %q: %w", owner, err)
		}
		available := make(map[string][]github.NativeRepositorySummary)
		for _, item := range page.Repositories {
			repository := item.Repository
			if !strings.EqualFold(strings.TrimSpace(repository.OrganizationID), organizationID.String()) {
				return RepositoryScopes{}, fmt.Errorf("repository %q returned a mismatched organization scope", repository.Name)
			}
			key := strings.ToLower(strings.TrimSpace(repository.Name))
			if key != "" {
				available[key] = append(available[key], repository)
			}
		}
		for name, configured := range wanted {
			matches := available[strings.ToLower(name)]
			if len(matches) != 1 {
				return RepositoryScopes{}, fmt.Errorf("configured repository %q resolved to %d visible repositories", configured, len(matches))
			}
			repositoryID, err := uuid.Parse(strings.TrimSpace(matches[0].ID))
			if err != nil || repositoryID == uuid.Nil {
				return RepositoryScopes{}, fmt.Errorf("configured repository %q returned an invalid UUID", configured)
			}
			byRepository[configured] = models.RepoScope{OrgID: organizationID, RepoID: repositoryID}
		}
	}
	return buildScopeIndex(byRepository)
}

func normalizeRequestedRepositories(repositories []string) (map[string]string, error) {
	if len(repositories) == 0 {
		return nil, errors.New("at least one repository is required")
	}
	result := make(map[string]string, len(repositories))
	for _, repository := range repositories {
		repository = strings.TrimSpace(repository)
		if _, err := github.ParseRepo(repository); err != nil {
			return nil, err
		}
		key := strings.ToLower(repository)
		if prior := result[key]; prior != "" {
			return nil, fmt.Errorf("duplicate configured repository %q", repository)
		}
		result[key] = repository
	}
	return result, nil
}

func requestedByOwner(requested map[string]string) map[string]map[string]string {
	result := map[string]map[string]string{}
	for _, repository := range requested {
		parts := strings.Split(repository, "/")
		owner, name := parts[0], parts[1]
		if result[owner] == nil {
			result[owner] = map[string]string{}
		}
		result[owner][name] = repository
	}
	return result
}

func validateInjectedScopes(requested map[string]string, injected map[string]models.RepoScope) (RepositoryScopes, error) {
	if len(injected) != len(requested) {
		return RepositoryScopes{}, errors.New("injected repository scopes must exactly cover configured repositories")
	}
	byRepository := make(map[string]models.RepoScope, len(requested))
	for key, repository := range requested {
		var scope models.RepoScope
		found := false
		for configured, candidate := range injected {
			if strings.EqualFold(strings.TrimSpace(configured), key) {
				scope, found = candidate, true
				break
			}
		}
		if !found {
			return RepositoryScopes{}, fmt.Errorf("injected scope missing configured repository %q", repository)
		}
		byRepository[repository] = scope
	}
	return buildScopeIndex(byRepository)
}

func buildScopeIndex(byRepository map[string]models.RepoScope) (RepositoryScopes, error) {
	byScope := make(map[models.RepoScope]string, len(byRepository))
	for repository, scope := range byRepository {
		if err := scope.Validate(); err != nil {
			return RepositoryScopes{}, fmt.Errorf("repository %q scope: %w", repository, err)
		}
		if prior := byScope[scope]; prior != "" {
			return RepositoryScopes{}, fmt.Errorf("repositories %q and %q resolved to the same UUID scope", prior, repository)
		}
		byScope[scope] = repository
	}
	if len(byScope) != len(byRepository) {
		return RepositoryScopes{}, errors.New("repository scope resolution was not one-to-one")
	}
	return RepositoryScopes{ByRepository: byRepository, ByScope: byScope}, nil
}
