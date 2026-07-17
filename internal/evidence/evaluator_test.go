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

func TestReviewAndVerifyDoNotImplicitlyRequireProviderReviewRecords(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	check := validRecord("unit", codereview.EvidenceCheck, "abc123", now)
	check.Name, check.State = "unit", "passed"
	for _, test := range []struct {
		name    string
		gate    Gate
		records []codereview.EvidenceRecord
	}{
		{name: "review zero findings", gate: GateReview},
		{name: "verify zero findings", gate: GateVerify, records: []codereview.EvidenceRecord{check}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
				SubjectRevision: "abc123", CapturedAt: now, Records: test.records}
			result := Evaluate(snapshot, Policy{}, Target{Gate: test.gate, Reference: reference, SubjectRevision: "abc123", Now: now})
			if !result.Passed {
				t.Fatalf("result=%+v", result)
			}
		})
	}

	snapshot := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
		SubjectRevision: "abc123", CapturedAt: now}
	result := Evaluate(snapshot, Policy{RequiredKinds: []codereview.EvidenceKind{codereview.EvidenceReview}},
		Target{Gate: GateReview, Reference: reference, SubjectRevision: "abc123", Now: now})
	if result.Passed || !hasFailure(result, "missing_evidence") {
		t.Fatalf("explicit review requirement was ignored: %+v", result)
	}
}

func TestPresentReviewRecordsRemainValidatedAndBlocking(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	snapshot, target := validSnapshot(now)
	target.Gate = GateReview
	snapshot.Records = snapshot.Records[:1]
	base := snapshot.Records[0]
	for _, test := range []struct {
		name     string
		state    string
		severity string
		edit     func(*codereview.EvidenceRecord)
		want     string
	}{
		{name: "open P0", state: "open", severity: "P0", want: "blocking_review"},
		{name: "open P1", state: "open", severity: "P1", want: "blocking_review"},
		{name: "open P2", state: "open", severity: "P2"},
		{name: "resolved P0", state: "resolved", severity: "P0"},
		{name: "dismissed P1", state: "dismissed", severity: "P1"},
		{name: "closed P1", state: "closed", severity: "P1"},
		{name: "superseded P1", state: "superseded", severity: "P1"},
		{name: "malformed linkage", state: "resolved", severity: "P2", edit: func(record *codereview.EvidenceRecord) {
			record.ProcessID = ""
		}, want: "malformed_review_linkage"},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := base
			record.State, record.Severity = test.state, test.severity
			if test.edit != nil {
				test.edit(&record)
			}
			snapshot.Records = []codereview.EvidenceRecord{record}
			result := Evaluate(snapshot, Policy{}, target)
			if test.want == "" && !result.Passed {
				t.Fatalf("result=%+v", result)
			}
			if test.want != "" && (result.Passed || !hasFailure(result, test.want)) {
				t.Fatalf("result=%+v, want %s", result, test.want)
			}
		})
	}
}

func TestReviewSupersessionUsesOnlyActiveFindingState(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	snapshot, target := validSnapshot(now)
	target.Gate = GateReview
	old := snapshot.Records[0]
	old.ID, old.State, old.Severity = "review-old", "open", "P1"
	successor := old
	successor.ID, successor.SupersedesID, successor.State = "review-current", old.ID, "resolved"
	successor.ObservedAt = old.ObservedAt.Add(30 * time.Second)
	snapshot.Records = []codereview.EvidenceRecord{old, successor}
	result := Evaluate(snapshot, Policy{}, target)
	if !result.Passed || len(result.EvidenceIDs) != 1 || result.EvidenceIDs[0] != successor.ID {
		t.Fatalf("resolved successor result=%+v", result)
	}
	successor.State = "open"
	snapshot.Records[1] = successor
	result = Evaluate(snapshot, Policy{}, target)
	if result.Passed || !hasFailure(result, "blocking_review") {
		t.Fatalf("open successor result=%+v", result)
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
