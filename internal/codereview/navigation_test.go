package codereview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestResolveNavigationChange(t *testing.T) {
	t.Parallel()
	reference := Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	request := SnapshotRequest{Reference: reference, SubjectRevision: "head-abc"}

	provider := navigationTestProvider(request)
	provider.snapshot.Facts[0].ExternalID = "provider-fact-id-is-not-authority"
	change, err := ResolveNavigationChange(t.Context(), provider, request)
	if err != nil {
		t.Fatal(err)
	}
	if change != (NavigationChange{Reference: reference, SubjectRevision: request.SubjectRevision,
		CanonicalURL: "https://code.example/acme/widgets/change/42"}) {
		t.Fatalf("navigation change = %+v", change)
	}
	if provider.snapshotCalls != 1 {
		t.Fatalf("snapshot calls = %d, want 1", provider.snapshotCalls)
	}
}

func TestResolveNavigationChangeRequiresSnapshotCapabilityBeforeFetch(t *testing.T) {
	t.Parallel()
	reference := Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	request := SnapshotRequest{Reference: reference, SubjectRevision: "head-abc"}
	provider := navigationTestProvider(request)
	provider.capabilities.Values = nil

	_, err := ResolveNavigationChange(t.Context(), provider, request)
	if !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("error = %v, want capability missing", err)
	}
	if provider.snapshotCalls != 0 {
		t.Fatalf("snapshot calls = %d, want 0", provider.snapshotCalls)
	}
}

func TestResolveNavigationChangeRejectsInvalidProviderOutput(t *testing.T) {
	t.Parallel()
	reference := Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	request := SnapshotRequest{Reference: reference, SubjectRevision: "head-abc"}

	tests := []struct {
		name string
		edit func(*Snapshot)
		want string
	}{
		{
			name: "identity mismatch",
			edit: func(snapshot *Snapshot) { snapshot.Reference.ChangeID = "43" },
			want: "reference does not match authority",
		},
		{
			name: "moved head",
			edit: func(snapshot *Snapshot) {
				snapshot.SubjectRevision = "head-new"
				snapshot.Facts[0].SubjectRevision = "head-new"
			},
			want: "subject revision does not match expected revision",
		},
		{
			name: "change fact at different revision",
			edit: func(snapshot *Snapshot) { snapshot.Facts[0].SubjectRevision = "head-new" },
			want: "provider fact revision does not match snapshot",
		},
		{
			name: "missing change fact",
			edit: func(snapshot *Snapshot) { snapshot.Facts = nil },
			want: "exactly one exact-revision change fact",
		},
		{
			name: "multiple change facts",
			edit: func(snapshot *Snapshot) {
				second := snapshot.Facts[0]
				second.ID = "change-2"
				second.ExternalID = "change-2"
				snapshot.Facts = append(snapshot.Facts, second)
			},
			want: "exactly one exact-revision change fact",
		},
		{
			name: "missing canonical URL",
			edit: func(snapshot *Snapshot) { snapshot.Facts[0].CanonicalURL = "" },
			want: "canonical URL is missing or unsafe",
		},
		{
			name: "unsafe canonical URL",
			edit: func(snapshot *Snapshot) {
				snapshot.Facts[0].CanonicalURL = "https://code.example/acme/widgets/change/42?access_token=secret"
			},
			want: "provider fact canonical URL is unsafe",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider := navigationTestProvider(request)
			test.edit(&provider.snapshot)
			_, err := ResolveNavigationChange(t.Context(), provider, request)
			if !errors.Is(err, ErrInvalidProviderData) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want invalid provider data containing %q", err, test.want)
			}
		})
	}
}

type navigationProvider struct {
	capabilities  Capabilities
	snapshot      Snapshot
	snapshotCalls int
}

func navigationTestProvider(request SnapshotRequest) *navigationProvider {
	return &navigationProvider{
		capabilities: Capabilities{ProtocolVersion: ProtocolVersion, Values: []Capability{CapabilityEvidenceSnapshot}},
		snapshot: Snapshot{
			ProtocolVersion: ProtocolVersion,
			Reference:       request.Reference,
			SubjectRevision: request.SubjectRevision,
			CapturedAt:      time.Date(2026, 7, 17, 1, 2, 3, 0, time.UTC),
			Facts: []ProviderFact{{
				ID:              "change-1",
				Kind:            EvidenceChange,
				ExternalID:      "42",
				State:           "open",
				SubjectRevision: request.SubjectRevision,
				ObservedAt:      time.Date(2026, 7, 17, 1, 2, 2, 0, time.UTC),
				CanonicalURL:    "https://code.example/acme/widgets/change/42",
				PayloadDigest:   strings.Repeat("a", 64),
			}},
		},
	}
}

func (p *navigationProvider) Capabilities(context.Context) (Capabilities, error) {
	return p.capabilities, nil
}

func (p *navigationProvider) Snapshot(context.Context, SnapshotRequest) (Snapshot, error) {
	p.snapshotCalls++
	return p.snapshot, nil
}
