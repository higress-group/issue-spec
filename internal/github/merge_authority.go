package github

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/codereview"
)

var ErrAtomicExternalAuthorityUnsupported = errors.New("github merge backend cannot atomically validate external authority generation")

// NativeMergeAuthorityBackend is deliberately narrower than Client and
// GitHubCodeBackend. Implementations must bind the complete policy/review/check
// generation to the returned token and validate it in MergeWithAuthority.
// The ordinary REST expected-SHA merge endpoint does not satisfy this contract.
type NativeMergeAuthorityBackend interface {
	CollectMergeAuthority(context.Context, codereview.MergeSnapshotRequest) (codereview.MergeSnapshot, error)
	MergeWithAuthority(context.Context, codereview.ConditionalMergeRequest) (codereview.ConditionalMergeResult, error)
}

type MergeAuthorityProviderOptions struct {
	ProviderKey                       string
	ProviderBuildIdentity             string
	AtomicExternalAuthorityGeneration bool
}

// MergeAuthorityProvider adapts a provider-native GitHub implementation to
// the same strict contract used by command bridges. It does not retrofit
// read-then-merge REST clients with authority they cannot atomically enforce.
type MergeAuthorityProvider struct {
	options MergeAuthorityProviderOptions
	backend NativeMergeAuthorityBackend
}

func NewMergeAuthorityProvider(options MergeAuthorityProviderOptions, backend NativeMergeAuthorityBackend) (*MergeAuthorityProvider, error) {
	options.ProviderKey = strings.TrimSpace(options.ProviderKey)
	options.ProviderBuildIdentity = strings.TrimSpace(options.ProviderBuildIdentity)
	capabilities := codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion,
		SemanticGeneration: codereview.MergeAuthorityGeneration, ProviderBuildIdentity: options.ProviderBuildIdentity,
		Values: codereview.RequiredMergeAuthorityCapabilities()}
	if codereview.ValidateProviderKey(options.ProviderKey) != nil || capabilities.Validate() != nil || backend == nil {
		return nil, errors.New("github merge authority requires a provider key, immutable build, and atomic backend")
	}
	return &MergeAuthorityProvider{options: options, backend: backend}, nil
}

func (p *MergeAuthorityProvider) Capabilities(context.Context) (codereview.Capabilities, error) {
	return codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion,
		SemanticGeneration: codereview.MergeAuthorityGeneration, ProviderBuildIdentity: p.options.ProviderBuildIdentity,
		Values: codereview.RequiredMergeAuthorityCapabilities()}, nil
}

func (p *MergeAuthorityProvider) MergeSnapshot(ctx context.Context, request codereview.MergeSnapshotRequest) (codereview.MergeSnapshot, error) {
	if err := request.Validate(); err != nil || request.Reference.ProviderKey != p.options.ProviderKey {
		return codereview.MergeSnapshot{}, fmt.Errorf("%w: github authority request provider mismatch", codereview.ErrInvalidProviderData)
	}
	snapshot, err := p.backend.CollectMergeAuthority(ctx, request)
	if err != nil {
		return codereview.MergeSnapshot{}, err
	}
	if err := codereview.ValidateMergeSnapshot(snapshot, request); err != nil {
		return codereview.MergeSnapshot{}, err
	}
	if snapshot.ProviderBuildIdentity != p.options.ProviderBuildIdentity {
		return codereview.MergeSnapshot{}, fmt.Errorf("%w: github authority build mismatch", codereview.ErrInvalidProviderData)
	}
	if snapshot.ExternalAuthorityGeneration != "" && !p.options.AtomicExternalAuthorityGeneration {
		return codereview.MergeSnapshot{}, ErrAtomicExternalAuthorityUnsupported
	}
	return snapshot, nil
}

func (p *MergeAuthorityProvider) MergeChange(ctx context.Context, request codereview.ConditionalMergeRequest) (codereview.ConditionalMergeResult, error) {
	if err := request.Validate(); err != nil || request.Reference.ProviderKey != p.options.ProviderKey {
		return codereview.ConditionalMergeResult{}, fmt.Errorf("%w: github conditional merge provider mismatch", codereview.ErrInvalidProviderData)
	}
	if request.ExternalAuthorityGeneration != "" && !p.options.AtomicExternalAuthorityGeneration {
		return codereview.ConditionalMergeResult{}, ErrAtomicExternalAuthorityUnsupported
	}
	result, err := p.backend.MergeWithAuthority(ctx, request)
	if err != nil {
		return codereview.ConditionalMergeResult{}, err
	}
	if err := codereview.ValidateConditionalMergeResult(result, request); err != nil {
		return codereview.ConditionalMergeResult{}, err
	}
	return result, nil
}

var _ codereview.MergeAuthorityProvider = (*MergeAuthorityProvider)(nil)
