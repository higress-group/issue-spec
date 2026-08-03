package model

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// ArtifactRef is the provider-neutral identity of one typed issue artifact.
// Relationship topology intentionally carries no assignment or evidence data.
type ArtifactRef struct {
	Issue     int    `json:"issue"`
	Type      string `json:"type"`
	ID        string `json:"id"`
	CommentID int64  `json:"comment_id,omitempty"`
	URL       string `json:"url"`
}

// Ref returns the exact typed identity and canonical navigation URL of an
// already-collected artifact.
func (artifact Artifact) Ref() (ArtifactRef, error) {
	ref := ArtifactRef{Issue: artifact.Issue, Type: artifact.Comment.Type, ID: artifact.Comment.ID,
		CommentID: artifact.CommentID, URL: NormalizeURL(artifact.URL)}
	if len(artifact.Comment.Errors) != 0 {
		return ArtifactRef{}, fmt.Errorf("typed artifact %s is invalid: %s", artifact.Comment.ID,
			strings.Join(artifact.Comment.Errors, "; "))
	}
	if err := ref.Validate(); err != nil {
		return ArtifactRef{}, err
	}
	return ref, nil
}

// Validate rejects incomplete or non-canonical relationship identities.
func (ref ArtifactRef) Validate() error {
	if ref.Issue <= 0 {
		return errors.New("artifact issue must be positive")
	}
	if ref.Type != strings.ToUpper(strings.TrimSpace(ref.Type)) || ref.ID != strings.TrimSpace(ref.ID) {
		return errors.New("artifact type and id must be canonical")
	}
	if err := ValidateTypedIdentity(ref.Type, ref.ID); err != nil {
		return fmt.Errorf("artifact identity: %w", err)
	}
	if ref.URL != NormalizeURL(ref.URL) || ref.URL == "" {
		return errors.New("artifact url must be canonical")
	}
	parsed, err := url.Parse(ref.URL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return errors.New("artifact url must be an absolute HTTP(S) provider identity")
	}
	return nil
}

// Key is stable across HTML/API URL aliases for the same typed artifact.
func (ref ArtifactRef) Key() string {
	return fmt.Sprintf("%d\x00%s\x00%s", ref.Issue, ref.Type, ref.ID)
}

// ArtifactProviderURLs returns the normalized HTML/API identities accepted for
// exact lookup. A duplicate alias is collapsed deterministically.
func ArtifactProviderURLs(artifact Artifact) []string {
	seen := map[string]bool{}
	var values []string
	for _, raw := range []string{artifact.URL, artifact.APIURL} {
		value := NormalizeURL(raw)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
