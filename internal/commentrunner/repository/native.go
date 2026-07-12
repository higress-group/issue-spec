package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type NativeResolver struct {
	Bindings github.NativeBindingOperations
	Scopes   map[string]models.RepoScope
}

func (r NativeResolver) ResolveRepository(ctx context.Context, issueRepositoryKey string) (Resolution, error) {
	key, err := NormalizeKey(issueRepositoryKey)
	if err != nil {
		return Resolution{}, err
	}
	if r.Bindings == nil {
		return Resolution{}, errors.New("native binding operations are required")
	}
	var scope models.RepoScope
	found := false
	for configured, candidate := range r.Scopes {
		if strings.EqualFold(strings.TrimSpace(configured), key) {
			scope, found = candidate, true
			break
		}
	}
	if !found || scope.Validate() != nil {
		return Resolution{}, NoBindingError()
	}
	binding, err := r.Bindings.GetNativeActiveBinding(ctx, scope)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve native source binding for %s: %w", key, err)
	}
	return normalizeSnapshot(SourceServer, key, Snapshot{BindingID: binding.ID, Version: binding.Version,
		ProviderKey: binding.ProviderKey, ExternalRepositoryID: binding.ExternalRepositoryID, CloneURL: binding.CloneURL,
		WebURL: binding.WebURL, DefaultBranch: binding.DefaultBranch})
}
