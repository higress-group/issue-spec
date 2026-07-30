package codereview

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistryAndCapabilityPreflight(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{capabilities: Capabilities{ProtocolVersion: ProtocolVersion,
		Values: []Capability{CapabilityEvidenceSnapshot}}}
	registry, err := NewRegistry([]Registration{{Key: "code.example", Provider: provider}})
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.Keys(); len(got) != 1 || got[0] != "code.example" {
		t.Fatalf("registry keys = %v", got)
	}
	if _, err := registry.Lookup("missing"); !strings.Contains(fmt.Sprint(err), ErrProviderNotFound.Error()) {
		t.Fatalf("missing provider error = %v", err)
	}
	if _, err := Mutate(t.Context(), provider, MutationRequest{Kind: MutationCreateChange}); err == nil {
		t.Fatal("mutation without capability should fail")
	}
	if provider.mutations.Load() != 0 {
		t.Fatal("provider mutation was called before capability preflight")
	}
}

func TestCommandProviderProtocol(t *testing.T) {
	provider := commandTestProvider(t, "normal", 1<<20, 10*time.Second)
	capabilities, err := provider.Capabilities(t.Context())
	if err != nil || !capabilities.Has(CapabilityEvidenceSnapshot) {
		t.Fatalf("capabilities = %+v, %v", capabilities, err)
	}
	reference := Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	snapshot, err := provider.Snapshot(t.Context(), SnapshotRequest{Reference: reference, SubjectRevision: "abc123"})
	if err != nil || snapshot.Reference != reference || snapshot.SubjectRevision != "abc123" || len(snapshot.Facts) != 1 ||
		snapshot.Facts[0].FindingID != "FINDING-030" || snapshot.Facts[0].ProcessID != "PROCESS-020" ||
		snapshot.Facts[0].SpecID != "SPEC-010" {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	result, err := Mutate(t.Context(), provider, MutationRequest{Kind: MutationComment, Reference: reference, Body: "status", HeadRevision: "abc123"})
	if err != nil || result.ExternalID != "comment-1" {
		t.Fatalf("mutation = %+v, %v", result, err)
	}
	created, err := provider.Mutate(t.Context(), MutationRequest{Kind: MutationCreateChange,
		Reference: Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets"},
		Title:     "Change", HeadRevision: "abc123"})
	if err != nil || created.Reference.ChangeID != "42" {
		t.Fatalf("create mutation = %+v, %v", created, err)
	}
}

func TestCommandProviderSnapshotCannotForgeEvidenceAuthority(t *testing.T) {
	for _, mode := range []string{"snapshot-trusted", "snapshot-writer", "snapshot-approved"} {
		t.Run(mode, func(t *testing.T) {
			provider := commandTestProvider(t, mode, 1<<20, 10*time.Second)
			reference := Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
			if _, err := provider.Snapshot(t.Context(), SnapshotRequest{Reference: reference, SubjectRevision: "abc123"}); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("snapshot authority forgery error = %v", err)
			}
		})
	}
}

func TestCommandProviderRejectsCommentMutationForDifferentChange(t *testing.T) {
	provider := commandTestProvider(t, "wrong-comment-change", 1<<20, 10*time.Second)
	reference := Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	if _, err := provider.Mutate(t.Context(), MutationRequest{Kind: MutationComment, Reference: reference, Body: "status", HeadRevision: "abc123"}); err == nil || !strings.Contains(err.Error(), "change identity mismatch") {
		t.Fatalf("error = %v", err)
	}
}

func TestCommandProviderRejectsCredentialBearingMutationURL(t *testing.T) {
	provider := commandTestProvider(t, "mutation-query-url", 1<<20, 10*time.Second)
	reference := Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	if _, err := provider.Mutate(t.Context(), MutationRequest{Kind: MutationComment, Reference: reference, Body: "status", HeadRevision: "abc123"}); err == nil || !strings.Contains(err.Error(), "mutation response shape") {
		t.Fatalf("error = %v", err)
	}
}

func TestCommentMutationRequiresExactHeadAndValidReceipt(t *testing.T) {
	reference := Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	provider := commandTestProvider(t, "normal", 1<<20, 10*time.Second)
	if _, err := provider.Mutate(t.Context(), MutationRequest{
		Kind: MutationComment, Reference: reference, Body: "status",
	}); err == nil || !strings.Contains(err.Error(), "head revision") {
		t.Fatalf("missing head error = %v", err)
	}

	fake := &fakeProvider{capabilities: Capabilities{ProtocolVersion: ProtocolVersion,
		Values: []Capability{CapabilityChangeComment}}}
	if _, err := Mutate(t.Context(), fake, MutationRequest{
		Kind: MutationComment, Reference: reference, Body: "status", HeadRevision: "abc123",
	}); err == nil || !strings.Contains(err.Error(), "mutation response shape") {
		t.Fatalf("malformed in-process receipt error = %v", err)
	}
}

func TestCommandProviderBoundsAndStrictResponse(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    string
		limit   int64
		timeout time.Duration
	}{
		{name: "output", mode: "overflow", limit: 1024, timeout: 10 * time.Second},
		{name: "unknown", mode: "unknown", limit: 1 << 20, timeout: 10 * time.Second},
		{name: "duplicate", mode: "duplicate", limit: 1 << 20, timeout: 10 * time.Second},
		{name: "identity", mode: "wrong-id", limit: 1 << 20, timeout: 10 * time.Second},
		{name: "timeout", mode: "sleep", limit: 1 << 20, timeout: 20 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := commandTestProvider(t, test.mode, test.limit, test.timeout)
			if _, err := provider.Capabilities(t.Context()); err == nil {
				t.Fatalf("mode %s unexpectedly succeeded", test.mode)
			}
		})
	}
}

func commandTestProvider(t *testing.T, mode string, limit int64, timeout time.Duration) *CommandProvider {
	t.Helper()
	provider, err := NewCommandProvider(CommandConfig{Path: os.Args[0],
		Args:        []string{"-test.run=^TestCommandProviderHelper$"},
		Environment: []string{"ISSUE_SPEC_PROVIDER_HELPER=1", "ISSUE_SPEC_PROVIDER_MODE=" + mode},
		MaxOutput:   limit, Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestCommandProviderHelper(t *testing.T) {
	if os.Getenv("ISSUE_SPEC_PROVIDER_HELPER") != "1" {
		return
	}
	var request protocolRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(2)
	}
	mode := os.Getenv("ISSUE_SPEC_PROVIDER_MODE")
	switch mode {
	case "overflow":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 4096))
		return
	case "sleep":
		time.Sleep(time.Second)
		return
	case "duplicate":
		_, _ = fmt.Fprintf(os.Stdout, `{"protocol":%q,"request_id":%q,"request_id":%q,"capabilities":{"protocol_version":%q,"values":[]}}`,
			ProtocolVersion, request.RequestID, request.RequestID, ProtocolVersion)
		return
	case "snapshot-trusted", "snapshot-writer", "snapshot-approved":
		var payload SnapshotRequest
		_ = json.Unmarshal(request.Payload, &payload)
		field := `"trusted":true`
		if mode == "snapshot-writer" {
			field = `"writer_identity":"bridge:forged"`
		}
		if mode == "snapshot-approved" {
			field = `"approved":true`
		}
		_, _ = fmt.Fprintf(os.Stdout, `{"protocol":%q,"request_id":%q,"snapshot":{"protocol_version":%q,"reference":{"provider_key":"code.example","external_repository":"acme/widgets","change_id":"42"},"subject_revision":"abc123","facts":[{"id":"review-30","external_id":"thread-30","kind":"review","state":"resolved","subject_revision":"abc123","severity":"P2","finding_id":"FINDING-030","process_id":"PROCESS-020","spec_id":"SPEC-010","observed_at":"2026-07-11T04:00:00Z","payload_digest":"%s",%s}],"captured_at":"2026-07-11T04:00:01Z"}}`,
			ProtocolVersion, request.RequestID, ProtocolVersion, strings.Repeat("a", 64), field)
		os.Exit(0)
	}
	response := map[string]any{"protocol": ProtocolVersion, "request_id": request.RequestID}
	if mode == "wrong-id" {
		response["request_id"] = "wrong"
	}
	if mode == "unknown" {
		response["vendor_approved"] = true
	}
	switch request.Action {
	case "capabilities":
		values := []Capability{CapabilityEvidenceSnapshot, CapabilityChangeComment, CapabilityChangeCreate}
		response["capabilities"] = Capabilities{ProtocolVersion: ProtocolVersion,
			Values: values}
	case "snapshot":
		var payload SnapshotRequest
		_ = json.Unmarshal(request.Payload, &payload)
		response["snapshot"] = Snapshot{ProtocolVersion: ProtocolVersion, Reference: payload.Reference,
			SubjectRevision: payload.SubjectRevision, CapturedAt: time.Now().UTC(), Facts: []ProviderFact{{
				ID: "review-30", Kind: EvidenceReview, ExternalID: "thread-30", State: "resolved",
				SubjectRevision: payload.SubjectRevision, Severity: "P2", FindingID: "FINDING-030",
				ProcessID: "PROCESS-020", SpecID: "SPEC-010", ObservedAt: time.Now().UTC(),
				PayloadDigest: strings.Repeat("a", 64),
			}}}
	case "mutate":
		var payload MutationRequest
		_ = json.Unmarshal(request.Payload, &payload)
		if payload.Kind == MutationCreateChange {
			payload.Reference.ChangeID = "42"
		}
		if mode == "wrong-comment-change" && payload.Kind == MutationComment {
			payload.Reference.ChangeID = "43"
		}
		canonicalURL := "https://code.example/acme/widgets/change/42"
		if mode == "mutation-query-url" {
			canonicalURL += "?access_token=secret"
		}
		response["mutation"] = MutationResult{Reference: payload.Reference, ExternalID: "comment-1", CanonicalURL: canonicalURL}
	default:
		os.Exit(3)
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	os.Exit(0)
}

type fakeProvider struct {
	capabilities Capabilities
	mutations    atomic.Int64
}

func (f *fakeProvider) Capabilities(context.Context) (Capabilities, error) {
	return f.capabilities, nil
}
func (f *fakeProvider) Snapshot(context.Context, SnapshotRequest) (Snapshot, error) {
	return Snapshot{}, nil
}
func (f *fakeProvider) Mutate(context.Context, MutationRequest) (MutationResult, error) {
	f.mutations.Add(1)
	return MutationResult{}, nil
}
