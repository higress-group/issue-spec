package codereview

import (
	"fmt"
	"sort"
	"strings"
)

// Registration is supplied only by trusted process/operator configuration.
// Repository workflow files receive a Registry but cannot add or replace an
// entry and therefore cannot select an executable, arguments, or credentials.
type Registration struct {
	Key      string
	Provider Provider
}

type Registry struct {
	providers map[string]Provider
	keys      []string
}

func NewRegistry(registrations []Registration) (Registry, error) {
	providers := make(map[string]Provider, len(registrations))
	keys := make([]string, 0, len(registrations))
	for _, registration := range registrations {
		key := strings.TrimSpace(registration.Key)
		if !validKey(key) || registration.Provider == nil {
			return Registry{}, fmt.Errorf("invalid operator code provider registration %q", key)
		}
		if _, exists := providers[key]; exists {
			return Registry{}, fmt.Errorf("duplicate operator code provider registration %q", key)
		}
		providers[key] = registration.Provider
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return Registry{providers: providers, keys: keys}, nil
}

func (r Registry) Lookup(key string) (Provider, error) {
	key = strings.TrimSpace(key)
	provider, ok := r.providers[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotFound, key)
	}
	return provider, nil
}

func (r Registry) Keys() []string {
	return append([]string(nil), r.keys...)
}
