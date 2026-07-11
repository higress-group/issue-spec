package evidence

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
)

func TestNativeProviderResolvesExactReferenceAndBuildsSnapshot(t *testing.T) {
	fixture := newNativeFixture(t)
	provider, err := NewNativeProvider(fixture.profile, "realm-token")
	if err != nil {
		t.Fatal(err)
	}
	provider.now = func() time.Time { return fixture.now }
	target, err := provider.ResolveTarget(t.Context(), "acme/widgets", 9, "code_change")
	if err != nil {
		t.Fatal(err)
	}
	if target.Reference != (codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}) ||
		target.SubjectRevision != "head-abc" || target.BaseRevision != "base-123" || len(target.Policy.Requirements) != 1 {
		t.Fatalf("target = %+v", target)
	}
	snapshot, err := codereview.FetchSnapshot(t.Context(), target.Provider, codereview.SnapshotRequest{
		Reference: target.Reference, SubjectRevision: target.SubjectRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundCheck := false
	for _, record := range snapshot.Records {
		if record.Kind == codereview.EvidenceCheck && record.Name == "unit" && record.Trusted {
			foundCheck = true
		}
	}
	if len(snapshot.Records) != 2 || snapshot.Records[0].ID > snapshot.Records[1].ID || !foundCheck {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	result := Evaluate(snapshot, Policy{RequiredChecks: []string{"unit"}, Freshness: map[codereview.EvidenceKind]time.Duration{}},
		Target{Gate: GateVerify, Reference: target.Reference, SubjectRevision: target.SubjectRevision, Now: fixture.now})
	if !result.Passed || len(result.EvidenceIDs) != 2 {
		t.Fatalf("evaluation = %+v", result)
	}
}

func TestNativeProviderRejectsUnsafeOrAmbiguousServerData(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*nativeFixture)
		want   string
	}{
		{name: "no-store required", mutate: func(f *nativeFixture) { f.noStore = false }, want: "not marked no-store"},
		{name: "ambiguous reference", mutate: func(f *nativeFixture) { f.duplicateReference = true }, want: "exactly one active code_change"},
		{name: "strict metadata", mutate: func(f *nativeFixture) { f.referenceMetadata = `{"head_revision":"head-abc","approved":true}` }, want: "only head_revision"},
		{name: "approved payload", mutate: func(f *nativeFixture) { f.approvedPayload = true }, want: "untrusted approval"},
		{name: "wrong change", mutate: func(f *nativeFixture) { f.evidenceChangeID = "other-change" }, want: "change identity"},
		{name: "malformed node id", mutate: func(f *nativeFixture) {
			f.nodeID = base64.RawStdEncoding.EncodeToString([]byte("User:" + f.issueID.String()))
		}, want: "issue node_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNativeFixture(t)
			test.mutate(fixture)
			provider, err := NewNativeProvider(fixture.profile, "realm-token")
			if err != nil {
				t.Fatal(err)
			}
			target, err := provider.ResolveTarget(t.Context(), "acme/widgets", 9, "code_change")
			if err == nil && (test.name == "approved payload" || test.name == "wrong change") {
				_, err = codereview.FetchSnapshot(t.Context(), target.Provider, codereview.SnapshotRequest{Reference: target.Reference, SubjectRevision: target.SubjectRevision})
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestNativeProviderUpsertsArchiveReferenceAfterExternalMutation(t *testing.T) {
	fixture := newNativeFixture(t)
	provider, _ := NewNativeProvider(fixture.profile, "realm-token")
	target, err := provider.ResolveTarget(t.Context(), "acme/widgets", 9, "code_change")
	if err != nil {
		t.Fatal(err)
	}
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "archive-7"}
	if err := provider.UpsertArchiveReference(t.Context(), target, reference, "https://code.example/archive/7", "archive-head", "archive-base"); err != nil {
		t.Fatal(err)
	}
	if !fixture.upserted || fixture.upsertBody["relation_kind"] != "archive_change" {
		t.Fatalf("upsert body = %+v", fixture.upsertBody)
	}
	if err := provider.UpsertArchiveReference(t.Context(), target, reference, "https://code.example/archive/7", "archive-head", "archive-base"); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if fixture.upsertCount != 1 {
		t.Fatalf("archive reference writes = %d, want 1", fixture.upsertCount)
	}
}

func TestNativeProviderBoundsHungRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	profile := auth.Profile{Name: "hung-native", Kind: auth.ProfileKindHosted, APIURL: server.URL + "/api/v3",
		NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL, ServerInstanceID: "instance-hung"}
	provider, err := NewNativeProvider(profile, "realm-token")
	if err != nil {
		t.Fatal(err)
	}
	provider.client.Timeout = 30 * time.Millisecond
	started := time.Now()
	_, err = provider.ResolveTarget(t.Context(), "acme/widgets", 9, "code_change")
	if err == nil || !strings.Contains(err.Error(), ErrNativeEvidence.Error()) {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("hung request took %s", elapsed)
	}
}

type nativeFixture struct {
	t                  *testing.T
	server             *httptest.Server
	profile            auth.Profile
	now                time.Time
	orgID, repoID      uuid.UUID
	issueID            uuid.UUID
	nodeID             string
	noStore            bool
	duplicateReference bool
	referenceMetadata  string
	approvedPayload    bool
	evidenceChangeID   string
	upserted           bool
	upsertCount        int
	archiveReference   map[string]any
	upsertBody         map[string]any
}

func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	f := &nativeFixture{t: t, now: time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC), orgID: uuid.New(), repoID: uuid.New(),
		issueID: uuid.New(), noStore: true, referenceMetadata: `{"head_revision":"head-abc","base_revision":"base-123"}`,
		evidenceChangeID: "change-42"}
	f.nodeID = base64.RawStdEncoding.EncodeToString([]byte("Issue:" + f.issueID.String()))
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	f.profile = auth.Profile{Name: "test-native", Kind: auth.ProfileKindHosted, APIURL: f.server.URL + "/api/v3",
		NativeAPIURL: f.server.URL + "/api/v1", WebURL: f.server.URL, ServerInstanceID: "instance-test"}
	return f
}

func (f *nativeFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer realm-token" {
		f.t.Errorf("authorization = %q", r.Header.Get("Authorization"))
	}
	if f.noStore {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", "native-test")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/context":
		writeNativeJSON(w, map[string]any{"user": map[string]any{}, "credential": map[string]any{}, "allowed_actions": []string{},
			"organizations": []any{map[string]any{"id": f.orgID, "name": "acme", "display_name": "Acme", "effective_permission": "maintain", "container_only": false, "allowed_actions": []string{"organization.read"}}}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/context/orgs/"+f.orgID.String()+"/repos":
		writeNativeJSON(w, map[string]any{"repositories": []any{map[string]any{"repository": map[string]any{"id": f.repoID,
			"organization_id": f.orgID, "name": "widgets", "display_name": "Widgets", "visibility": "private", "contribution_policy": "members"},
			"effective_permission": "maintain", "allowed_actions": []string{"read"}}}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v3/repos/acme/widgets/issues/9":
		writeNativeJSON(w, map[string]any{"node_id": f.nodeID})
	case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/api/v1/orgs/%s/repos/%s/evidence/policy", f.orgID, f.repoID):
		writeNativeJSON(w, map[string]any{"representation_version": 1, "requirements": []any{map[string]any{
			"evidence_type": "check", "freshness": int64(time.Hour), "representation_version": 1}}, "created_at": f.now, "updated_at": f.now})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/references"):
		references := []any{f.reference("change-42", "code_change", f.referenceMetadata)}
		if f.archiveReference != nil {
			references = append(references, f.archiveReference)
		}
		if f.duplicateReference {
			references = append(references, f.reference("change-43", "code_change", f.referenceMetadata))
		}
		writeNativeJSON(w, map[string]any{"references": references})
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/references"):
		if err := json.NewDecoder(r.Body).Decode(&f.upsertBody); err != nil {
			f.t.Fatal(err)
		}
		f.upserted = true
		f.upsertCount++
		metadata, _ := json.Marshal(f.upsertBody["metadata"])
		f.archiveReference = f.reference("archive-7", "archive_change", string(metadata))
		writeNativeJSON(w, f.archiveReference)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/evidence"):
		payloadReview := map[string]any{"schema_version": "issue-spec.evidence/v1", "change_id": f.evidenceChangeID, "severity": "P2", "summary": "reviewed"}
		if f.approvedPayload {
			payloadReview["approved"] = true
		}
		writeNativeJSON(w, map[string]any{"evidence": []any{
			f.evidence("review", "resolved", "finding-1", payloadReview),
			f.evidence("check", "passed", "check-1", map[string]any{"schema_version": "issue-spec.evidence/v1", "change_id": f.evidenceChangeID, "name": "unit"}),
		}})
	default:
		w.WriteHeader(http.StatusNotFound)
		writeNativeJSON(w, map[string]any{"status": 404})
	}
}

func (f *nativeFixture) reference(change, relation, metadata string) map[string]any {
	var raw any
	_ = json.Unmarshal([]byte(metadata), &raw)
	return map[string]any{"id": uuid.New(), "issue_id": f.issueID, "provider_key": "code.example", "relation_kind": relation,
		"external_repository_id": "acme/widgets-code", "external_id": change, "canonical_url": "https://code.example/changes/" + change,
		"lifecycle_state": "active", "visibility": "repository", "metadata": raw, "representation_version": 1, "created_at": f.now, "updated_at": f.now}
}

func (f *nativeFixture) evidence(kind, state, externalID string, payload map[string]any) map[string]any {
	raw, _ := json.Marshal(payload)
	digest := sha256.Sum256(raw)
	return map[string]any{"id": uuid.New(), "issue_id": f.issueID, "provider_key": "code.example",
		"external_repository_id": "acme/widgets-code", "evidence_type": kind, "external_id": externalID,
		"ingest_key": kind + "-1", "normalized_state": state, "subject_revision": "head-abc", "observed_at": f.now.Add(-time.Minute),
		"payload_hash": digest[:], "payload": payload, "provenance": map[string]any{}, "writer_user_id": uuid.New(),
		"writer_identity_key": "bridge:test", "visibility": "repository", "created_at": f.now.Add(-time.Minute)}
}

func writeNativeJSON(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }
