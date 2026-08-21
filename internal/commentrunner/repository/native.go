package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type NativeResolver struct {
	Registry Registry
	// Bindings and Scopes retain source compatibility for callers constructing
	// the explicit-repository resolver directly.
	Bindings github.NativeBindingOperations
	Scopes   map[string]models.RepoScope
}

func (r NativeResolver) ResolveRepository(ctx context.Context, issueRepositoryKey string) (Resolution, error) {
	key, err := NormalizeKey(issueRepositoryKey)
	if err != nil {
		return Resolution{}, err
	}
	registry := r.Registry
	if registry == nil {
		if r.Bindings == nil {
			return Resolution{}, errors.New("native binding operations are required")
		}
		registry = &StaticRegistry{Bindings: r.Bindings, Scopes: r.Scopes}
	}
	entry, err := registry.ResolveRepository(ctx, key)
	if err != nil {
		if IsRepositoryIneligible(err) {
			return Resolution{}, NoBindingError()
		}
		return Resolution{}, fmt.Errorf("resolve native source binding for %s: %w", key, err)
	}
	resolution, err := normalizeSnapshot(SourceServer, entry.Repository, Snapshot{BindingID: entry.Binding.ID,
		Version: entry.Binding.Version, ProviderKey: entry.Binding.ProviderKey,
		ExternalRepositoryID: entry.Binding.ExternalRepositoryID, CloneURL: entry.Binding.CloneURL,
		WebURL: entry.Binding.WebURL, DefaultBranch: entry.Binding.DefaultBranch})
	if err != nil {
		return Resolution{}, err
	}
	resolution.Scope = entry.Scope
	return resolution, nil
}
