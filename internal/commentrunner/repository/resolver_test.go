package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/bindings"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestResolverPriorityServerAuthorizationAndNoFallback(t *testing.T) {
	key := "repo:Acme/Widgets"
	operator, err := NewStaticOperatorMappings([]OperatorMapping{{IssueRepositoryKey: key, Snapshot: testSnapshot("operator-1", 4, "https://operator.example/acme/widgets.git")}})
	if err != nil {
		t.Fatal(err)
	}
	scope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	reader := &fakeBindingReader{binding: bindings.Binding{ID: uuid.New(), Scope: scope, ProviderKey: "github",
		ExternalRepositoryID: "server/widgets", CloneURL: "https://server.example/widgets.git",
		WebURL: "https://server.example/widgets", DefaultBranch: "main", Version: 9, Active: true}}
	principal := serverauth.Principal{User: serverauth.User{ID: uuid.New()}}
	server, err := NewServerSource(reader, authz.Authenticated(principal), []ServerRepository{{IssueRepositoryKey: key, Scope: scope}})
	if err != nil {
		t.Fatal(err)
	}
	resolver := Resolver{Operator: operator, Server: server}
	resolved, err := resolver.ResolveRepository(t.Context(), key)
	if err != nil || resolved.Binding.Source != SourceOperator || resolved.Binding.BindingID != "operator-1" || reader.calls != 0 {
		t.Fatalf("operator priority result=%+v calls=%d err=%v", resolved, reader.calls, err)
	}

	resolved, err = (Resolver{Server: server}).ResolveRepository(t.Context(), key)
	if err != nil || resolved.Binding.Source != SourceServer || resolved.Binding.BindingID != reader.binding.ID.String() ||
		reader.scope != scope || reader.subject.Principal == nil || reader.subject.Principal.User.ID != principal.User.ID {
		t.Fatalf("server result=%+v scope=%+v subject=%+v err=%v", resolved, reader.scope, reader.subject, err)
	}
	pinned := resolved.Binding
	reader.binding.ID = uuid.New()
	reader.binding.Version++
	reader.binding.CloneURL = "https://server.example/widgets-v2.git"
	current, err := (Resolver{Server: server}).ResolveRepository(t.Context(), key)
	if err != nil || DiagnosticCode(ValidatePinned(pinned, current.Binding)) != DiagnosticBindingDrift {
		t.Fatalf("server binding replacement did not drift: current=%+v err=%v", current, err)
	}
	reader.binding.Active = false
	if _, err := (Resolver{Server: server}).ResolveRepository(t.Context(), key); !errors.Is(err, ErrNoBinding) {
		t.Fatalf("inactive server binding resolved: %v", err)
	}
	reader.binding.Active = true

	reader.err = adminservice.ErrNotFound
	if _, err := (Resolver{Server: server}).ResolveRepository(t.Context(), key); !errors.Is(err, ErrNoBinding) || DiagnosticCode(err) != DiagnosticNoBinding {
		t.Fatalf("invisible server resolution error=%v", err)
	}
	if _, err := (Resolver{}).ResolveRepository(t.Context(), "github.com/acme/widgets"); !errors.Is(err, ErrNoBinding) {
		t.Fatalf("host/slug unexpectedly derived a clone source: %v", err)
	}
}

func TestOperatorMappingsConflictValidationAndDrift(t *testing.T) {
	mapping := OperatorMapping{IssueRepositoryKey: "o/r", Snapshot: testSnapshot("mapping-1", 1, "https://code.example/o/r.git")}
	if _, err := NewStaticOperatorMappings([]OperatorMapping{mapping, mapping}); err == nil {
		t.Fatal("duplicate operator mappings were accepted")
	}
	mutable, err := NewMutableOperatorMappings([]OperatorMapping{mapping})
	if err != nil {
		t.Fatal(err)
	}
	resolver := Resolver{Operator: mutable}
	first, err := resolver.ResolveRepository(t.Context(), "O/R")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePinned(first.Binding, first.Binding); err != nil {
		t.Fatal(err)
	}
	replacement := mapping
	replacement.Snapshot = testSnapshot("mapping-2", 2, "https://code.example/o/r-v2.git")
	if err := mutable.Replace([]OperatorMapping{replacement}); err != nil {
		t.Fatal(err)
	}
	current, err := resolver.ResolveRepository(t.Context(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePinned(first.Binding, current.Binding); DiagnosticCode(err) != DiagnosticBindingDrift || strings.Contains(err.Error(), current.Binding.CloneURL) {
		t.Fatalf("drift error=%v", err)
	}
	if err := ValidatePinned(state.RepositoryBindingSnapshot{}, current.Binding); DiagnosticCode(err) != DiagnosticLegacyState {
		t.Fatalf("legacy error=%v", err)
	}
}

func TestRepositoryURLAttackVectorsAndNumericPorts(t *testing.T) {
	for _, raw := range []string{
		"https://user@example.test/repo.git",
		"ssh://git@example.test/repo.git",
		"https://example.test/repo.git?token=x",
		"https://example.test/repo.git#main",
		"file:///tmp/repo",
		"git://example.test/repo",
		"git@example.test:repo.git",
		"ftp://example.test/repo",
		"https:opaque",
		"https://example.test/",
		"https://example.test:postgres/repo.git",
		"https://example.test:0/repo.git",
		"https://example.test:65536/repo.git",
		"https://example.test/repo\\evil.git",
	} {
		if _, err := ValidateCloneURL(raw); err == nil {
			t.Errorf("ValidateCloneURL(%q) succeeded", raw)
		}
	}
	for _, raw := range []string{"https://example.test/repo.git", "ssh://example.test:22/repo.git", "https://example.test:65535/repo.git"} {
		if _, err := ValidateCloneURL(raw); err != nil {
			t.Errorf("ValidateCloneURL(%q) error=%v", raw, err)
		}
	}
	if _, err := ValidateWebURL("ssh://example.test/repo"); err == nil {
		t.Fatal("ValidateWebURL accepted ssh")
	}
}

func TestServerMappingRejectsDuplicateAndCrossTenantIdentity(t *testing.T) {
	reader := &fakeBindingReader{}
	first := ServerRepository{IssueRepositoryKey: "one", Scope: models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}}
	second := first
	second.Scope = models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	if _, err := NewServerSource(reader, authz.Anonymous(), []ServerRepository{first, second}); err == nil {
		t.Fatal("duplicate server repository keys were accepted")
	}
	source, err := NewServerSource(reader, authz.Anonymous(), []ServerRepository{first})
	if err != nil {
		t.Fatal(err)
	}
	reader.err = adminservice.ErrNotFound
	if _, found, err := source.lookup(context.Background(), "one"); err != nil || found {
		t.Fatalf("tenant-invisible binding found=%t err=%v", found, err)
	}
}

func testSnapshot(id string, version int64, cloneURL string) Snapshot {
	return Snapshot{BindingID: id, Version: version, ProviderKey: "github", ExternalRepositoryID: "acme/widgets",
		CloneURL: cloneURL, WebURL: strings.TrimSuffix(cloneURL, ".git"), DefaultBranch: "main"}
}

type fakeBindingReader struct {
	binding bindings.Binding
	err     error
	calls   int
	scope   models.RepoScope
	subject authz.Subject
}

func (f *fakeBindingReader) ActiveBinding(_ context.Context, subject authz.Subject, scope models.RepoScope) (bindings.Binding, error) {
	f.calls++
	f.scope, f.subject = scope, subject
	return f.binding, f.err
}
