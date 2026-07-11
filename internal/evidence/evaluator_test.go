package evidence

import (
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/codereview"
)

func TestVerifyEvidencePassesExactTrustedRevision(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)
	snapshot, target := validSnapshot(now)
	result := Evaluate(snapshot, Policy{RequiredChecks: []string{"unit", "dco"},
		Freshness: map[codereview.EvidenceKind]time.Duration{codereview.EvidenceCheck: time.Hour}}, target)
	if !result.Passed || len(result.Failures) != 0 || len(result.EvidenceIDs) != 3 {
		t.Fatalf("result = %+v", result)
	}
}

func TestVerifyEvidenceFailsClosedMatrix(t *testing.T) {
	now := time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		edit func(*codereview.Snapshot, *Target)
		code string
	}{
		{name: "missing", edit: func(s *codereview.Snapshot, _ *Target) { s.Records = s.Records[:1] }, code: "missing_evidence"},
		{name: "stale", edit: func(s *codereview.Snapshot, _ *Target) { s.Records[1].ObservedAt = now.Add(-2 * time.Hour) }, code: "stale_evidence"},
		{name: "untrusted", edit: func(s *codereview.Snapshot, _ *Target) { s.Records[1].Trusted = false }, code: "untrusted_evidence"},
		{name: "wrong revision", edit: func(s *codereview.Snapshot, _ *Target) { s.Records[1].SubjectRevision = "other" }, code: "record_revision_mismatch"},
		{name: "pending", edit: func(s *codereview.Snapshot, _ *Target) { s.Records[1].State = "pending" }, code: "required_check_pending"},
		{name: "failed", edit: func(s *codereview.Snapshot, _ *Target) { s.Records[1].State = "failed" }, code: "required_check_failed"},
		{name: "blocking review", edit: func(s *codereview.Snapshot, _ *Target) {
			s.Records[0].State, s.Records[0].Severity = "open", "P1"
		}, code: "blocking_review"},
		{name: "missing review linkage", edit: func(s *codereview.Snapshot, _ *Target) {
			s.Records[0].FindingID = ""
		}, code: "malformed_review_linkage"},
		{name: "invalid review state", edit: func(s *codereview.Snapshot, _ *Target) {
			s.Records[0].State = "approved"
		}, code: "malformed_review_linkage"},
		{name: "wrong provider", edit: func(s *codereview.Snapshot, _ *Target) { s.Reference.ProviderKey = "other" }, code: "reference_mismatch"},
		{name: "broken supersession", edit: func(s *codereview.Snapshot, _ *Target) { s.Records[1].SupersedesID = "missing" }, code: "invalid_supersession"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot, target := validSnapshot(now)
			test.edit(&snapshot, &target)
			result := Evaluate(snapshot, Policy{RequiredChecks: []string{"unit", "dco"},
				Freshness: map[codereview.EvidenceKind]time.Duration{codereview.EvidenceCheck: time.Hour}}, target)
			if result.Passed || !hasFailure(result, test.code) || len(result.EvidenceIDs) != 0 {
				t.Fatalf("result = %+v, want %s", result, test.code)
			}
		})
	}
}

func TestMergeAndArchiveRequireMergedEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)
	for _, gate := range []Gate{GateMerge, GateArchive} {
		kind := codereview.EvidenceMerge
		if gate == GateArchive {
			kind = codereview.EvidenceArchive
		}
		reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
		record := validRecord("merged", kind, "abc123", now)
		record.State = "open"
		snapshot := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
			SubjectRevision: "abc123", CapturedAt: now, Records: []codereview.EvidenceRecord{record}}
		result := Evaluate(snapshot, Policy{}, Target{Gate: gate, Reference: reference, SubjectRevision: "abc123", Now: now})
		if result.Passed || !hasFailure(result, "not_merged") {
			t.Fatalf("%s open result = %+v", gate, result)
		}
		record.State, record.MergeRevision = "merged", "merge456"
		snapshot.Records = []codereview.EvidenceRecord{record}
		if result = Evaluate(snapshot, Policy{}, Target{Gate: gate, Reference: reference, SubjectRevision: "abc123", Now: now}); !result.Passed {
			t.Fatalf("%s merged result = %+v", gate, result)
		}
	}
}

func validSnapshot(now time.Time) (codereview.Snapshot, Target) {
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	review := validRecord("review", codereview.EvidenceReview, "abc123", now)
	review.State, review.Severity = "resolved", "P2"
	review.FindingID, review.ProcessID, review.SpecID = "FINDING-001", "PROCESS-001", "SPEC-001"
	unit := validRecord("unit", codereview.EvidenceCheck, "abc123", now)
	unit.Name, unit.State = "unit", "passed"
	dco := validRecord("dco", codereview.EvidenceCheck, "abc123", now)
	dco.Name, dco.State = "dco", "passed"
	return codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
			SubjectRevision: "abc123", CapturedAt: now, Records: []codereview.EvidenceRecord{review, unit, dco}},
		Target{Gate: GateVerify, Reference: reference, SubjectRevision: "abc123", Now: now}
}

func validRecord(id string, kind codereview.EvidenceKind, revision string, now time.Time) codereview.EvidenceRecord {
	return codereview.EvidenceRecord{ID: id, Kind: kind, State: "passed", SubjectRevision: revision,
		ObservedAt: now.Add(-time.Minute), Trusted: true, WriterIdentity: "bridge-1", PayloadDigest: "sha256:abc"}
}

func hasFailure(result Result, code string) bool {
	for _, failure := range result.Failures {
		if failure.Code == code {
			return true
		}
	}
	return false
}
