package repository

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

const RunnerTriggerAction = "runner.trigger"

var ErrRepositoryIneligible = errors.New("repository is not eligible for organization runner")

type RegistryEntry struct {
	Repository string
	Scope      models.RepoScope
	Binding    github.NativeBinding
}

type Registry interface {
	ResolveScope(context.Context, models.RepoScope) (RegistryEntry, error)
	ResolveRepository(context.Context, string) (RegistryEntry, error)
}

func IsRepositoryIneligible(err error) bool { return errors.Is(err, ErrRepositoryIneligible) }

type StaticRegistry struct {
	Bindings github.NativeBindingOperations
	Scopes   map[string]models.RepoScope
}

func (r *StaticRegistry) ResolveScope(ctx context.Context, scope models.RepoScope) (RegistryEntry, error) {
	if err := scope.Validate(); err != nil {
		return RegistryEntry{}, ineligible("invalid repository scope")
	}
	for repository, candidate := range r.Scopes {
		if candidate == scope {
			return r.resolveBinding(ctx, repository, scope)
		}
	}
	return RegistryEntry{}, ineligible("repository scope is not configured")
}

func (r *StaticRegistry) ResolveRepository(ctx context.Context, requested string) (RegistryEntry, error) {
	key, err := NormalizeKey(requested)
	if err != nil {
		return RegistryEntry{}, err
	}
	for repository, scope := range r.Scopes {
		if strings.EqualFold(strings.TrimSpace(repository), key) {
			return r.resolveBinding(ctx, repository, scope)
		}
	}
	return RegistryEntry{}, ineligible("repository is not configured")
}

func (r *StaticRegistry) resolveBinding(ctx context.Context, repository string, scope models.RepoScope) (RegistryEntry, error) {
	if r == nil || r.Bindings == nil {
		return RegistryEntry{}, errors.New("native binding operations are required")
	}
	binding, err := r.Bindings.GetNativeActiveBinding(ctx, scope)
	if err != nil {
		if deterministicAuthorityFailure(err) {
			return RegistryEntry{}, ineligible("active Source Binding is unavailable")
		}
		return RegistryEntry{}, fmt.Errorf("resolve native source binding for %s: %w", repository, err)
	}
	return RegistryEntry{Repository: strings.TrimSpace(repository), Scope: scope, Binding: binding}, nil
}

type OrganizationRegistry struct {
	Context          github.NativeContextOperations
	Bindings         github.NativeBindingOperations
	OrganizationID   uuid.UUID
	OrganizationName string

	mu           sync.Mutex
	byScope      map[models.RepoScope]string
	byRepository map[string]models.RepoScope
}

func NewOrganizationRegistry(contextSource github.NativeContextOperations, bindings github.NativeBindingOperations,
	organizationID uuid.UUID, organizationName string) (*OrganizationRegistry, error) {
	organizationName = strings.TrimSpace(organizationName)
	if contextSource == nil || bindings == nil || organizationID == uuid.Nil || organizationName == "" ||
		strings.ContainsAny(organizationName, "/\\\r\n\t") {
		return nil, errors.New("organization repository registry requires native context, bindings, UUID and name")
	}
	return &OrganizationRegistry{Context: contextSource, Bindings: bindings, OrganizationID: organizationID,
		OrganizationName: organizationName, byScope: map[models.RepoScope]string{},
		byRepository: map[string]models.RepoScope{}}, nil
}

func (r *OrganizationRegistry) ResolveScope(ctx context.Context, scope models.RepoScope) (RegistryEntry, error) {
	if r == nil || scope.Validate() != nil || scope.OrgID != r.OrganizationID {
		return RegistryEntry{}, ineligible("repository is outside the configured organization")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resolveLocked(ctx, scope, "")
}

func (r *OrganizationRegistry) ResolveRepository(ctx context.Context, requested string) (RegistryEntry, error) {
	key, err := NormalizeKey(requested)
	if err != nil {
		return RegistryEntry{}, err
	}
	owner, name, ok := strings.Cut(key, "/")
	if !ok || !strings.EqualFold(owner, r.OrganizationName) {
		return RegistryEntry{}, ineligible("repository is outside the configured organization")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	scope := r.byRepository[strings.ToLower(key)]
	return r.resolveLocked(ctx, scope, name)
}

func (r *OrganizationRegistry) resolveLocked(ctx context.Context, hintedScope models.RepoScope,
	hintedName string) (RegistryEntry, error) {
	page, err := r.Context.ListNativeContextRepositories(ctx, r.OrganizationID.String())
	if err != nil {
		if deterministicAuthorityFailure(err) {
			return RegistryEntry{}, ineligible("repository access is unavailable")
		}
		return RegistryEntry{}, fmt.Errorf("refresh organization repository access: %w", err)
	}
	var matches []github.NativeRepositoryContext
	for _, candidate := range page.Repositories {
		repository := candidate.Repository
		repositoryID, parseErr := uuid.Parse(strings.TrimSpace(repository.ID))
		organizationID, organizationErr := uuid.Parse(strings.TrimSpace(repository.OrganizationID))
		if parseErr != nil || organizationErr != nil || organizationID != r.OrganizationID {
			continue
		}
		if hintedScope.Validate() == nil && repositoryID != hintedScope.RepoID {
			continue
		}
		if hintedName != "" && !strings.EqualFold(strings.TrimSpace(repository.Name), hintedName) {
			continue
		}
		matches = append(matches, candidate)
	}
	if len(matches) != 1 || !includesAction(matches[0].AllowedActions, RunnerTriggerAction) {
		return RegistryEntry{}, ineligible("repository is missing, invisible, ambiguous, or not runner-triggerable")
	}
	repository := matches[0].Repository
	repositoryID, _ := uuid.Parse(strings.TrimSpace(repository.ID))
	scope := models.RepoScope{OrgID: r.OrganizationID, RepoID: repositoryID}
	canonical := r.OrganizationName + "/" + strings.TrimSpace(repository.Name)
	key := strings.ToLower(canonical)
	if prior := r.byScope[scope]; prior != "" && !strings.EqualFold(prior, canonical) {
		return RegistryEntry{}, ineligible("repository UUID identity changed")
	}
	if prior := r.byRepository[key]; prior.Validate() == nil && prior != scope {
		return RegistryEntry{}, ineligible("repository name identity changed")
	}
	binding, err := r.Bindings.GetNativeActiveBinding(ctx, scope)
	if err != nil {
		if deterministicAuthorityFailure(err) {
			return RegistryEntry{}, ineligible("active Source Binding is unavailable")
		}
		return RegistryEntry{}, fmt.Errorf("refresh active Source Binding for %s: %w", canonical, err)
	}
	r.byScope[scope], r.byRepository[key] = canonical, scope
	return RegistryEntry{Repository: canonical, Scope: scope, Binding: binding}, nil
}

func includesAction(actions []string, expected string) bool {
	for _, action := range actions {
		if strings.EqualFold(strings.TrimSpace(action), expected) {
			return true
		}
	}
	return false
}

func deterministicAuthorityFailure(err error) bool {
	var apiError *github.APIError
	return errors.As(err, &apiError) && (apiError.StatusCode == http.StatusUnauthorized ||
		apiError.StatusCode == http.StatusForbidden || apiError.StatusCode == http.StatusNotFound)
}

func ineligible(detail string) error {
	return fmt.Errorf("%w: %s", ErrRepositoryIneligible, detail)
}

var _ Registry = (*StaticRegistry)(nil)
var _ Registry = (*OrganizationRegistry)(nil)
