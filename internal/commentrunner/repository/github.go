package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/github"
)

const githubRepositoryBindingVersion int64 = 1

// GitHubResolver derives a complete operator snapshot only from an
// authenticated repository metadata lookup for the explicitly configured
// issue repository key. It performs a live lookup for every call so dispatcher
// resume validation observes metadata drift.
type GitHubResolver struct {
	Metadata github.RepositoryMetadataOperations
}

func (r GitHubResolver) ResolveRepository(ctx context.Context, issueRepositoryKey string) (Resolution, error) {
	key, err := NormalizeKey(issueRepositoryKey)
	if err != nil {
		return Resolution{}, err
	}
	if r.Metadata == nil {
		return Resolution{}, errors.New("GitHub repository metadata operations are required")
	}
	result, err := r.Metadata.GetRepository(ctx, key)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve authenticated GitHub repository metadata for %s: %w", key, err)
	}
	repositoryKey, err := NormalizeKey(result.Repository.FullName)
	if err != nil {
		return Resolution{}, errors.New("authenticated GitHub repository metadata omitted a valid full identity")
	}
	if repositoryKey != key {
		return Resolution{}, fmt.Errorf("authenticated GitHub repository identity %q does not match configured repository %q", repositoryKey, key)
	}
	if result.Repository.ID <= 0 {
		return Resolution{}, errors.New("authenticated GitHub repository metadata omitted a positive stable repository id")
	}
	repositoryID := strconv.FormatInt(result.Repository.ID, 10)
	return normalizeSnapshot(SourceOperator, key, Snapshot{
		BindingID:            "github-repository:" + repositoryID,
		Version:              githubRepositoryBindingVersion,
		ProviderKey:          "github",
		ExternalRepositoryID: repositoryID,
		CloneURL:             result.Repository.CloneURL,
		WebURL:               result.Repository.HTMLURL,
		DefaultBranch:        strings.TrimSpace(result.Repository.DefaultBranch),
	})
}
