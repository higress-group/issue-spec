package store

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestRepoStoreRejectsInvalidScopeBeforeQuery(t *testing.T) {
	tests := []struct {
		name  string
		scope RepoScope
		cause error
	}{
		{name: "missing organization", scope: RepoScope{RepoID: uuid.New()}, cause: models.ErrOrganizationScopeRequired},
		{name: "missing repository", scope: RepoScope{OrgID: uuid.New()}, cause: models.ErrRepositoryScopeRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (RepoStore{scope: tt.scope}).AllocateIssueNumber(t.Context())
			if !errors.Is(err, ErrInvalidScope) {
				t.Fatalf("error = %v, want ErrInvalidScope", err)
			}
			if !errors.Is(err, tt.cause) {
				t.Fatalf("error = %v, want underlying %v", err, tt.cause)
			}
		})
	}
}

func TestOrgStoreRepoPreservesStoreAndTransactionContext(t *testing.T) {
	root := &Store{}
	orgID := uuid.New()
	repoID := uuid.New()
	org := OrgStore{root: root, scope: OrgScope{OrgID: orgID}, inTx: true}
	repo := org.Repo(repoID)
	if repo.root != root {
		t.Fatal("OrgStore.Repo did not preserve the root store")
	}
	if !repo.inTx {
		t.Fatal("OrgStore.Repo did not preserve transaction context")
	}
	if repo.scope != (RepoScope{OrgID: orgID, RepoID: repoID}) {
		t.Fatalf("scope = %#v", repo.scope)
	}
}

func TestCanonicalJSONUsesSemanticObjectOrder(t *testing.T) {
	left, err := canonicalJSON([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := canonicalJSON([]byte("{\n\"a\":1,\"b\":2}"))
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) {
		t.Fatalf("canonical forms differ: %s != %s", left, right)
	}
}

func TestCanonicalJSONRejectsInvalidInput(t *testing.T) {
	if _, err := canonicalJSON([]byte(`{"unterminated"`)); err == nil {
		t.Fatal("canonicalJSON accepted invalid JSON")
	}
}
