package commands

import (
	"context"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
)

func (a *app) resolveOperatorProvider(ctx context.Context, profile auth.Profile, key string) (codereview.Provider, error) {
	if a.lookupOperatorProvider == nil {
		return nil, codereview.ErrProviderNotFound
	}
	return a.lookupOperatorProvider(ctx, profile, key)
}

func defaultResolveOperatorProvider(_ context.Context, profile auth.Profile, key string) (codereview.Provider, error) {
	registry, _, err := codereview.LoadOperatorRegistry(profile.OperatorRegistryFile)
	if err != nil {
		return nil, err
	}
	return registry.Lookup(key)
}
