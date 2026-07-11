package commands

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestExternalVerifyGateUsesAuthoritativeNativeTarget(t *testing.T) {
	clearCommandAuthEnv(t)
	profile := auth.Profile{Name: "staging", Kind: auth.ProfileKindHosted, APIURL: "https://issues.example/api/v3",
		NativeAPIURL: "https://issues.example/api/v1", WebURL: "https://issues.example", ServerInstanceID: "instance-staging"}
	if err := auth.SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	baseSnapshot := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
		SubjectRevision: "head-abc", CapturedAt: now, Records: []codereview.EvidenceRecord{
			testEvidenceRecord("review-1", codereview.EvidenceReview, "resolved", "head-abc", now),
			testEvidenceRecord("check-1", codereview.EvidenceCheck, "passed", "head-abc", now),
		}}
	baseSnapshot.Records[0].Severity = "P2"
	baseSnapshot.Records[1].Name = "unit"

	for _, test := range []struct {
		name     string
		expected string
		edit     func(*codereview.Snapshot)
		want     string
	}{
		{name: "authoritative ref without optional flag"},
		{name: "wrong command revision", expected: "other-head", want: "revision mismatch"},
		{name: "missing check", edit: func(s *codereview.Snapshot) { s.Records = s.Records[:1] }, want: "missing_evidence"},
		{name: "stale check", edit: func(s *codereview.Snapshot) { s.Records[1].ObservedAt = now.Add(-2 * time.Hour) }, want: "stale_evidence"},
		{name: "untrusted check", edit: func(s *codereview.Snapshot) { s.Records[1].Trusted = false }, want: "untrusted_evidence"},
		{name: "wrong provider", edit: func(s *codereview.Snapshot) { s.Reference.ProviderKey = "other.example" }, want: "reference_mismatch"},
		{name: "wrong record revision", edit: func(s *codereview.Snapshot) { s.Records[1].SubjectRevision = "other-head" }, want: "record_revision_mismatch"},
		{name: "pending check", edit: func(s *codereview.Snapshot) { s.Records[1].State = "pending" }, want: "required_check_pending"},
		{name: "failed check", edit: func(s *codereview.Snapshot) { s.Records[1].State = "failed" }, want: "required_check_failed"},
		{name: "open p1", edit: func(s *codereview.Snapshot) { s.Records[0].State, s.Records[0].Severity = "open", "P1" }, want: "blocking_review"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := baseSnapshot
			snapshot.Records = append([]codereview.EvidenceRecord(nil), baseSnapshot.Records...)
			if test.edit != nil {
				test.edit(&snapshot)
			}
			bridge := &commandEvidenceProvider{snapshot: snapshot}
			native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference,
				SubjectRevision: "head-abc", Policy: coreevidence.NativePolicy{Requirements: []coreevidence.NativeRequirement{
					{Kind: codereview.EvidenceCheck, Freshness: time.Hour},
				}}, Provider: bridge, IssueID: uuid.New(), OrgID: uuid.New(), RepoID: uuid.New()}}
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			app.profileName = "staging"
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
			result, hosted, err := app.externalGate(t.Context(), "github.com", "realm-token", "acme/widgets", 9,
				"code_change", test.expected, coreevidence.GateVerify)
			if !hosted {
				t.Fatal("self-hosted profile was not selected")
			}
			if test.want == "" {
				if err != nil || !result.Evaluation.Passed || result.Consumption.SubjectRevision != "head-abc" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestConsumedEvidenceStampAndRevisionBindingAreExactAndIdempotent(t *testing.T) {
	body := "<!-- issue-spec:type=VERIFY id=VERIFY-100 status=done -->\n### Revision\n\n`head-abc`\n"
	artifact := model.Artifact{Comment: model.TypedComment{Type: "VERIFY", ID: "VERIFY-100", Status: "done", Body: body}}
	if _, err := exactRevisionBoundVerify([]model.Artifact{artifact}, "head-abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := exactRevisionBoundVerify([]model.Artifact{artifact}, "head-ab"); err == nil {
		t.Fatal("prefix revision unexpectedly accepted")
	}
	consumption := externalEvidenceConsumption{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code",
		ChangeID: "change-42", SubjectRevision: "head-abc", EvidenceIDs: []string{"z", "a"}}
	first, changed, err := stampConsumedEvidence(body, consumption)
	if err != nil || !changed || !strings.Contains(first, `"evidence_ids":["a","z"]`) {
		t.Fatalf("first stamp changed=%v err=%v body=%q", changed, err, first)
	}
	second, changed, err := stampConsumedEvidence(first, consumption)
	if err != nil || changed || second != first || strings.Count(second, consumedEvidenceStart) != 1 {
		t.Fatalf("second stamp changed=%v err=%v body=%q", changed, err, second)
	}
}

func TestExternalReviewSyncPreservesCanonicalFindingLinkage(t *testing.T) {
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	review := testEvidenceRecord("review-1", codereview.EvidenceReview, "resolved", "head-abc", now)
	review.FindingID, review.ProcessID, review.SpecID = "FINDING-030", "PROCESS-020", "SPEC-010"
	review.CanonicalURL = "https://code.example/reviews/30"
	gate := externalGateResult{Target: coreevidence.NativeTarget{Reference: reference, SubjectRevision: "head-abc"},
		Snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
			SubjectRevision: "head-abc", CapturedAt: now, Records: []codereview.EvidenceRecord{review}},
		Evaluation: coreevidence.Result{Passed: true, EvidenceIDs: []string{"review-1"}},
		Consumption: externalEvidenceConsumption{ProviderKey: reference.ProviderKey,
			ExternalRepository: reference.ExternalRepository, ChangeID: reference.ChangeID,
			SubjectRevision: "head-abc", EvidenceIDs: []string{"review-1"}}}
	body, err := renderExternalReviewSyncComment("REVIEW-101", "Review Agent", writerSession{}, "external review", gate)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"finding_id": "FINDING-030"`, `"process_id": "PROCESS-020"`,
		`"spec_id": "SPEC-010"`, `"evidence_id": "review-1"`, "https://code.example/reviews/30"} {
		if !strings.Contains(body, required) {
			t.Fatalf("canonical REVIEW missing %q:\n%s", required, body)
		}
	}
}

func TestExternalArchiveMutationFailureNeverWritesReference(t *testing.T) {
	target := coreevidence.NativeTarget{Reference: codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "implementation-1"}}
	native := &commandNativeEvidence{}
	provider := &commandEvidenceProvider{capabilities: []codereview.Capability{codereview.CapabilityChangeCreate}, mutateErr: errors.New("provider unavailable")}
	request := codereview.MutationRequest{Kind: codereview.MutationCreateChange,
		Reference:    codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code"},
		HeadRevision: "archive-head", BaseRevision: "archive-base"}
	if _, err := createExternalArchiveChange(t.Context(), provider, native, target, request); err == nil {
		t.Fatal("mutation failure unexpectedly succeeded")
	}
	if native.upserts != 0 {
		t.Fatalf("archive reference writes = %d", native.upserts)
	}
	provider.mutateErr = nil
	provider.mutation = codereview.MutationResult{Reference: codereview.Reference{ProviderKey: "code.example",
		ExternalRepository: "acme/widgets-code", ChangeID: "archive-7"}, CanonicalURL: "https://code.example/archive/7"}
	if _, err := createExternalArchiveChange(t.Context(), provider, native, target, request); err != nil {
		t.Fatal(err)
	}
	if native.upserts != 1 {
		t.Fatalf("archive reference writes = %d, want 1", native.upserts)
	}
}

func testEvidenceRecord(id string, kind codereview.EvidenceKind, state, revision string, now time.Time) codereview.EvidenceRecord {
	record := codereview.EvidenceRecord{ID: id, Kind: kind, State: state, SubjectRevision: revision,
		ObservedAt: now.Add(-time.Minute), Trusted: true, WriterIdentity: "bridge:test", PayloadDigest: "sha256:test"}
	if kind == codereview.EvidenceReview {
		record.Severity, record.FindingID, record.ProcessID, record.SpecID = "P2", "FINDING-001", "PROCESS-001", "SPEC-001"
	}
	return record
}

type commandEvidenceProvider struct {
	snapshot     codereview.Snapshot
	capabilities []codereview.Capability
	mutation     codereview.MutationResult
	mutateErr    error
}

func (p *commandEvidenceProvider) Capabilities(context.Context) (codereview.Capabilities, error) {
	values := p.capabilities
	if values == nil {
		values = []codereview.Capability{codereview.CapabilityEvidenceSnapshot}
	}
	return codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion, Values: values}, nil
}

func (p *commandEvidenceProvider) Snapshot(context.Context, codereview.SnapshotRequest) (codereview.Snapshot, error) {
	return p.snapshot, nil
}

func (p *commandEvidenceProvider) Mutate(context.Context, codereview.MutationRequest) (codereview.MutationResult, error) {
	return p.mutation, p.mutateErr
}

type commandNativeEvidence struct {
	target  coreevidence.NativeTarget
	upserts int
}

func (n *commandNativeEvidence) ResolveTarget(context.Context, string, int, string) (coreevidence.NativeTarget, error) {
	return n.target, nil
}

func (n *commandNativeEvidence) UpsertArchiveReference(context.Context, coreevidence.NativeTarget, codereview.Reference, string, string, string) error {
	n.upserts++
	return nil
}
