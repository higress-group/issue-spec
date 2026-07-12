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
	Key         string
	Provider    Provider
	Description ProviderDescription
}

type Registry struct {
	providers    map[string]Provider
	descriptions map[string]ProviderDescription
	keys         []string
}

func NewRegistry(registrations []Registration) (Registry, error) {
	providers := make(map[string]Provider, len(registrations))
	descriptions := make(map[string]ProviderDescription, len(registrations))
	keys := make([]string, 0, len(registrations))
	for _, registration := range registrations {
		key := strings.TrimSpace(registration.Key)
		if !validKey(key) || registration.Provider == nil {
			return Registry{}, fmt.Errorf("invalid operator code provider registration %q", key)
		}
		if _, exists := providers[key]; exists {
			return Registry{}, fmt.Errorf("duplicate operator code provider registration %q", key)
		}
		description, err := registration.Description.Normalized(key)
		if err != nil {
			return Registry{}, fmt.Errorf("invalid operator code provider registration %q: %w", key, err)
		}
		providers[key] = registration.Provider
		descriptions[key] = description
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return Registry{providers: providers, descriptions: descriptions, keys: keys}, nil
}

func (r Registry) Descriptions() []ProviderDescription {
	result := make([]ProviderDescription, 0, len(r.keys))
	for _, key := range r.keys {
		description := r.descriptions[key]
		description.RemoteAuthorities = append([]string(nil), description.RemoteAuthorities...)
		description.Capabilities = append([]Capability(nil), description.Capabilities...)
		description.RecommendedEvidence = append([]EvidenceKind(nil), description.RecommendedEvidence...)
		result = append(result, description)
	}
	return result
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
