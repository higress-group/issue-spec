package models

import "encoding/json"

// SourceBindingMatch describes whether a persisted code-change relationship
// identifies the repository selected by the active credential-free source
// binding. It is deliberately provider-neutral presentation data.
type SourceBindingMatch string

const (
	SourceBindingMatched    SourceBindingMatch = "matched"
	SourceBindingMismatched SourceBindingMatch = "mismatched"
	SourceBindingUnbound    SourceBindingMatch = "unbound"
)

// CodeChangeRelationship is the caller-filtered read model shared by issue and
// change projections. Metadata is omitted below maintain permission; callers
// must never infer provider URLs or identities from issue body text.
type CodeChangeRelationship struct {
	ProviderKey          string             `json:"provider_key"`
	RelationKind         string             `json:"relation_kind"`
	ExternalRepositoryID string             `json:"external_repository_id"`
	ExternalID           string             `json:"external_id"`
	CanonicalURL         string             `json:"canonical_url"`
	Title                *string            `json:"title,omitempty"`
	LifecycleState       string             `json:"lifecycle_state"`
	SourceBindingMatch   SourceBindingMatch `json:"source_binding_match"`
	Metadata             json.RawMessage    `json:"metadata,omitempty"`
}
