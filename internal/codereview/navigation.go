package codereview

import (
	"context"
	"fmt"
	"strings"
)

// NavigationChange is the provider-neutral identity needed to establish a
// navigable code_change relationship. It intentionally contains no provider
// facts: an attach-time snapshot validates navigation but does not become
// trusted evidence.
type NavigationChange struct {
	Reference       Reference `json:"reference"`
	SubjectRevision string    `json:"subject_revision"`
	CanonicalURL    string    `json:"canonical_url"`
}

// ResolveNavigationChange validates an existing code change at the exact
// authority and revision supplied by the caller. The snapshot's top-level
// Reference is authoritative; a ProviderFact ExternalID never substitutes for
// its provider, repository, or change identity.
func ResolveNavigationChange(ctx context.Context, provider Provider, request SnapshotRequest) (NavigationChange, error) {
	if err := request.Reference.Validate(); err != nil || strings.TrimSpace(request.SubjectRevision) == "" {
		return NavigationChange{}, fmt.Errorf("%w: navigation request identity is incomplete", ErrInvalidProviderData)
	}
	snapshot, err := FetchSnapshot(ctx, provider, request)
	if err != nil {
		return NavigationChange{}, err
	}
	if err := ValidateProviderSnapshot(snapshot); err != nil {
		return NavigationChange{}, err
	}
	if snapshot.Reference != request.Reference {
		return NavigationChange{}, fmt.Errorf("%w: navigation snapshot reference does not match authority", ErrInvalidProviderData)
	}
	if snapshot.SubjectRevision != request.SubjectRevision {
		return NavigationChange{}, fmt.Errorf("%w: navigation snapshot subject revision does not match expected revision", ErrInvalidProviderData)
	}

	var changeFact *ProviderFact
	for i := range snapshot.Facts {
		if snapshot.Facts[i].Kind != EvidenceChange {
			continue
		}
		if changeFact != nil {
			return NavigationChange{}, fmt.Errorf("%w: navigation snapshot must contain exactly one exact-revision change fact", ErrInvalidProviderData)
		}
		changeFact = &snapshot.Facts[i]
	}
	if changeFact == nil {
		return NavigationChange{}, fmt.Errorf("%w: navigation snapshot must contain exactly one exact-revision change fact", ErrInvalidProviderData)
	}
	if !safeCanonicalURL(changeFact.CanonicalURL) {
		return NavigationChange{}, fmt.Errorf("%w: navigation change canonical URL is missing or unsafe", ErrInvalidProviderData)
	}

	return NavigationChange{
		Reference:       snapshot.Reference,
		SubjectRevision: snapshot.SubjectRevision,
		CanonicalURL:    changeFact.CanonicalURL,
	}, nil
}
