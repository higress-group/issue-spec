package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/bindings"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type OperatorSource interface {
	LookupOperatorBinding(context.Context, string) (Snapshot, bool, error)
}

type ServerBindingReader interface {
	ActiveBinding(context.Context, authz.Subject, models.RepoScope) (bindings.Binding, error)
}

type Snapshot struct {
	BindingID            string
	Version              int64
	ProviderKey          string
	ExternalRepositoryID string
	CloneURL             string
	WebURL               string
	DefaultBranch        string
}

type OperatorMapping struct {
	IssueRepositoryKey string
	Snapshot           Snapshot
}

type StaticOperatorMappings struct {
	items map[string]Snapshot
}

func NewStaticOperatorMappings(mappings []OperatorMapping) (*StaticOperatorMappings, error) {
	result := &StaticOperatorMappings{items: make(map[string]Snapshot, len(mappings))}
	for _, mapping := range mappings {
		key, err := NormalizeKey(mapping.IssueRepositoryKey)
		if err != nil {
			return nil, err
		}
		if _, exists := result.items[key]; exists {
			return nil, fmt.Errorf("duplicate operator repository mapping %q", key)
		}
		if _, err := normalizeSnapshot(SourceOperator, key, mapping.Snapshot); err != nil {
			return nil, fmt.Errorf("operator repository mapping %q: %w", key, err)
		}
		result.items[key] = mapping.Snapshot
	}
	return result, nil
}

func (m *StaticOperatorMappings) LookupOperatorBinding(_ context.Context, key string) (Snapshot, bool, error) {
	if m == nil {
		return Snapshot{}, false, nil
	}
	key, err := NormalizeKey(key)
	if err != nil {
		return Snapshot{}, false, err
	}
	item, found := m.items[key]
	return item, found, nil
}

type ServerRepository struct {
	IssueRepositoryKey string
	Scope              models.RepoScope
}

type ServerSource struct {
	reader  ServerBindingReader
	subject authz.Subject
	scopes  map[string]models.RepoScope
}

func NewServerSource(reader ServerBindingReader, subject authz.Subject, repositories []ServerRepository) (*ServerSource, error) {
	if reader == nil {
		return nil, errors.New("server binding reader is required")
	}
	result := &ServerSource{reader: reader, subject: subject, scopes: make(map[string]models.RepoScope, len(repositories))}
	for _, repository := range repositories {
		key, err := NormalizeKey(repository.IssueRepositoryKey)
		if err != nil {
			return nil, err
		}
		if err := repository.Scope.Validate(); err != nil {
			return nil, err
		}
		if _, exists := result.scopes[key]; exists {
			return nil, fmt.Errorf("duplicate server repository mapping %q", key)
		}
		result.scopes[key] = repository.Scope
	}
	return result, nil
}

func (s *ServerSource) lookup(ctx context.Context, key string) (Snapshot, bool, error) {
	if s == nil {
		return Snapshot{}, false, nil
	}
	scope, found := s.scopes[key]
	if !found {
		return Snapshot{}, false, nil
	}
	binding, err := s.reader.ActiveBinding(ctx, s.subject, scope)
	if errors.Is(err, adminservice.ErrNotFound) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	if !binding.Active {
		return Snapshot{}, false, nil
	}
	return Snapshot{BindingID: binding.ID.String(), Version: binding.Version, ProviderKey: binding.ProviderKey,
		ExternalRepositoryID: binding.ExternalRepositoryID, CloneURL: binding.CloneURL, WebURL: binding.WebURL,
		DefaultBranch: binding.DefaultBranch}, true, nil
}

type Resolver struct {
	Operator OperatorSource
	Server   *ServerSource
}

// ResolveRepository enforces the only valid priority order: operator mapping,
// authorized active server binding, then fail closed.
func (r Resolver) ResolveRepository(ctx context.Context, issueRepositoryKey string) (Resolution, error) {
	key, err := NormalizeKey(issueRepositoryKey)
	if err != nil {
		return Resolution{}, err
	}
	if r.Operator != nil {
		snapshot, found, err := r.Operator.LookupOperatorBinding(ctx, key)
		if err != nil {
			return Resolution{}, err
		}
		if found {
			return normalizeSnapshot(SourceOperator, key, snapshot)
		}
	}
	if r.Server != nil {
		snapshot, found, err := r.Server.lookup(ctx, key)
		if err != nil {
			return Resolution{}, err
		}
		if found {
			return normalizeSnapshot(SourceServer, key, snapshot)
		}
	}
	return Resolution{}, NoBindingError()
}

func normalizeSnapshot(source, key string, input Snapshot) (Resolution, error) {
	input.BindingID = strings.TrimSpace(input.BindingID)
	input.ProviderKey = strings.TrimSpace(input.ProviderKey)
	input.ExternalRepositoryID = strings.TrimSpace(input.ExternalRepositoryID)
	input.DefaultBranch = strings.TrimSpace(input.DefaultBranch)
	if input.BindingID == "" || input.Version <= 0 || input.ProviderKey == "" ||
		input.ExternalRepositoryID == "" || input.DefaultBranch == "" {
		return Resolution{}, errors.New("repository binding snapshot is incomplete")
	}
	cloneURL, err := ValidateCloneURL(input.CloneURL)
	if err != nil {
		return Resolution{}, err
	}
	webURL, err := ValidateWebURL(input.WebURL)
	if err != nil {
		return Resolution{}, err
	}
	binding := stateSnapshot(source, key, input, cloneURL, webURL)
	return Resolution{Repo: key, CloneURL: cloneURL, DefaultBranch: input.DefaultBranch, Ref: input.DefaultBranch,
		Binding: binding, Diagnostic: "repository_binding_source=" + source}, nil
}

func stateSnapshot(source, key string, input Snapshot, cloneURL, webURL string) state.RepositoryBindingSnapshot {
	return state.RepositoryBindingSnapshot{Source: source, IssueRepositoryKey: key, BindingID: input.BindingID,
		Version: input.Version, ProviderKey: input.ProviderKey, ExternalRepositoryID: input.ExternalRepositoryID,
		CloneURL: cloneURL, WebURL: webURL, DefaultBranch: input.DefaultBranch}
}

// MutableOperatorMappings is intentionally small and primarily useful for a
// long-running process whose trusted operator configuration is reloaded. Each
// replacement is validated atomically; conflicting mappings never become live.
type MutableOperatorMappings struct {
	mu      sync.RWMutex
	current *StaticOperatorMappings
}

func NewMutableOperatorMappings(mappings []OperatorMapping) (*MutableOperatorMappings, error) {
	current, err := NewStaticOperatorMappings(mappings)
	if err != nil {
		return nil, err
	}
	return &MutableOperatorMappings{current: current}, nil
}

func (m *MutableOperatorMappings) Replace(mappings []OperatorMapping) error {
	next, err := NewStaticOperatorMappings(mappings)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.current = next
	m.mu.Unlock()
	return nil
}

func (m *MutableOperatorMappings) LookupOperatorBinding(ctx context.Context, key string) (Snapshot, bool, error) {
	if m == nil {
		return Snapshot{}, false, nil
	}
	m.mu.RLock()
	current := m.current
	m.mu.RUnlock()
	return current.LookupOperatorBinding(ctx, key)
}
