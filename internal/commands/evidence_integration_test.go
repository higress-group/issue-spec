package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/workflow"
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
		{name: "wrong provider", edit: func(s *codereview.Snapshot) { s.Reference.ProviderKey = "other.example" }, want: "snapshot provider"},
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
			native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
				SubjectRevision: "head-abc", Policy: coreevidence.NativePolicy{Requirements: []coreevidence.NativeRequirement{
					{Kind: codereview.EvidenceCheck, Freshness: time.Hour},
				}}, Provider: bridge, IssueID: uuid.New(), OrgID: uuid.New(), RepoID: uuid.New()}}
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			app.profileName = "staging"
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
			app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) {
				return &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
					Reference: reference, SubjectRevision: "head-abc", CapturedAt: now}}, nil
			}
			result, hosted, err := app.externalGate(t.Context(), "github.com", "realm-token", "acme/widgets", 9,
				"code_change", test.expected, coreevidence.GateVerify)
			if !hosted {
				t.Fatal("self-hosted profile was not selected")
			}
			if test.want == "" {
				if err != nil || !result.Evaluation.Passed || result.Consumption.SubjectRevision != "head-abc" ||
					result.Consumption.ReferenceVersion != 7 || native.syncs != 1 {
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

func TestExternalGatePreservesGitHubMode(t *testing.T) {
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	profile := auth.Profile{Name: auth.DefaultProfileName, Kind: auth.ProfileKindGitHub, Hostname: "github.com",
		APIURL: "https://api.github.com", WebURL: "https://github.com"}
	result, selfHosted, err := app.externalGateWithProfile(t.Context(), profile, "", "acme/widgets", 9,
		"code_change", "", coreevidence.GateVerify, ".", "verify")
	if err != nil || selfHosted || result.Consumption.ProviderKey != "" {
		t.Fatalf("GitHub external gate result=%+v self_hosted=%t err=%v", result, selfHosted, err)
	}
}

func TestExternalGateSynchronizesPersistsReloadsThenEvaluates(t *testing.T) {
	clearCommandAuthEnv(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "issue-spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "issue-spec", "config.yaml"), []byte(`external_code:
  provider_key: code.example
  evidence:
    sync_before: [verify, runner]
    required_checks: [unit]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	profile := auth.Profile{Name: "sync", Kind: auth.ProfileKindHosted, APIURL: "https://issues.example/api/v3",
		NativeAPIURL: "https://issues.example/api/v1", WebURL: "https://issues.example", ServerInstanceID: "instance-sync"}
	if err := auth.SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	var calls []string
	providerFacts := &commandEvidenceProvider{label: "operator", calls: &calls, snapshot: codereview.Snapshot{
		ProtocolVersion: codereview.ProtocolVersion, Reference: reference, SubjectRevision: "head-abc", CapturedAt: now,
		Facts: []codereview.ProviderFact{{ID: "check-fact", ExternalID: "unit", Kind: codereview.EvidenceCheck,
			State: "passed", SubjectRevision: "head-abc", Name: "unit", ObservedAt: now.Add(-time.Minute),
			PayloadDigest: strings.Repeat("a", 64)}}}}
	ledger := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
		SubjectRevision: "head-abc", CapturedAt: now, Records: []codereview.EvidenceRecord{
			testEvidenceRecord("review-ledger", codereview.EvidenceReview, "resolved", "head-abc", now),
			testEvidenceRecord("check-ledger", codereview.EvidenceCheck, "passed", "head-abc", now),
		}}
	ledger.Records[1].Name = "unit"
	ledgerProvider := &commandEvidenceProvider{label: "ledger", calls: &calls, snapshot: ledger}
	native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7, SubjectRevision: "head-abc",
		Provider: ledgerProvider, IssueID: uuid.New(), OrgID: uuid.New(), RepoID: uuid.New()}, calls: &calls}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.profileName = profile.Name
	app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
	app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) { return providerFacts, nil }
	result, hosted, err := app.externalGate(t.Context(), "github.com", "realm-token", "acme/widgets", 9,
		"code_change", "head-abc", coreevidence.GateVerify)
	if err != nil || !hosted || !result.Evaluation.Passed || native.syncs != 1 || len(native.snapshot.Facts) != 1 {
		t.Fatalf("externalGate() result=%+v hosted=%t syncs=%d snapshot=%+v err=%v", result, hosted, native.syncs, native.snapshot, err)
	}
	if strings.Join(calls, ",") != "operator,persist,ledger" || result.Consumption.ReferenceVersion != 7 ||
		strings.Join(result.Consumption.EvidenceIDs, ",") != "check-ledger,review-ledger" {
		t.Fatalf("calls=%v consumption=%+v", calls, result.Consumption)
	}
	if len(result.Consumption.Bindings) != 1 || result.Consumption.Bindings[0].EvidenceID != "review-ledger" ||
		result.Consumption.Bindings[0].ProcessID != "PROCESS-001" || result.Consumption.Bindings[0].SpecID != "SPEC-001" ||
		!result.Consumption.Bindings[0].Trusted || result.Consumption.Bindings[0].SubjectRevision != "head-abc" {
		t.Fatalf("authoritative selected bindings=%+v", result.Consumption.Bindings)
	}
	credential := filepath.Join(root, "runner-token")
	if err := os.WriteFile(credential, []byte("realm-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	runnerIdentity, err := (&runnerEvidencePreGate{app: app, profile: profile}).BeforeDispatch(t.Context(), jobs.EvidencePreGateRequest{
		Repo: "acme/widgets", IssueNumber: 9, WorkflowRoot: root, CredentialFile: credential,
	})
	if err != nil || runnerIdentity.Skipped || runnerIdentity.ProviderKey != result.Consumption.ProviderKey ||
		runnerIdentity.ExternalRepository != result.Consumption.ExternalRepository || runnerIdentity.ChangeID != result.Consumption.ChangeID ||
		runnerIdentity.SubjectRevision != result.Consumption.SubjectRevision ||
		strings.Join(runnerIdentity.EvidenceIDs, ",") != strings.Join(result.Consumption.EvidenceIDs, ",") {
		t.Fatalf("runner identity=%+v interactive=%+v err=%v", runnerIdentity, result.Consumption, err)
	}
	if strings.Join(calls, ",") != "operator,persist,ledger,operator,persist,ledger" {
		t.Fatalf("interactive/runner orchestration calls=%v", calls)
	}
	calls = nil
	native.syncErr = errors.New("writer authorization denied")
	if _, _, err := app.externalGate(t.Context(), "github.com", "realm-token", "acme/widgets", 9,
		"code_change", "head-abc", coreevidence.GateVerify); err == nil || !strings.Contains(err.Error(), "persist external provider facts") {
		t.Fatalf("persistence failure = %v", err)
	}
	if strings.Join(calls, ",") != "operator,persist" {
		t.Fatalf("ledger was evaluated after persistence failure: %v", calls)
	}
}

func TestExternalReviewGateAlwaysSynchronizesBeforeEvaluation(t *testing.T) {
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	var calls []string
	provider := &commandEvidenceProvider{label: "operator", calls: &calls, snapshot: codereview.Snapshot{
		ProtocolVersion: codereview.ProtocolVersion, Reference: reference, SubjectRevision: "head-abc", CapturedAt: now}}
	ledger := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
		SubjectRevision: "head-abc", CapturedAt: now, Records: []codereview.EvidenceRecord{
			testEvidenceRecord("review-ledger", codereview.EvidenceReview, "resolved", "head-abc", now),
		}}
	native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
		SubjectRevision: "head-abc", Provider: &commandEvidenceProvider{label: "ledger", calls: &calls, snapshot: ledger}}, calls: &calls}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
	app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) { return provider, nil }

	for i := 0; i < 2; i++ {
		result, hosted, err := app.externalGateWithProfile(t.Context(), auth.Profile{Kind: auth.ProfileKindHosted}, "token",
			"acme/widgets", 9, "code_change", "head-abc", coreevidence.GateReview, t.TempDir(), "review")
		if err != nil || !hosted || !result.Evaluation.Passed || result.Consumption.ReferenceVersion != 7 {
			t.Fatalf("run %d result=%+v hosted=%t err=%v", i+1, result, hosted, err)
		}
	}
	if native.syncs != 2 || native.resolveCalls != 4 || strings.Join(calls, ",") !=
		"operator,persist,ledger,operator,persist,ledger" {
		t.Fatalf("syncs=%d resolve_calls=%d calls=%v", native.syncs, native.resolveCalls, calls)
	}
}

func TestExternalVerifyStageSynchronizesWithoutPolicyAndOtherStagesRemainOptIn(t *testing.T) {
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	providerSnapshot := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
		Reference: reference, SubjectRevision: "head-abc", CapturedAt: now}
	check := testEvidenceRecord("check-ledger", codereview.EvidenceCheck, "passed", "head-abc", now)
	check.Name = "unit"
	ledgerSnapshot := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
		Reference: reference, SubjectRevision: "head-abc", CapturedAt: now, Records: []codereview.EvidenceRecord{check}}
	for _, test := range []struct {
		stage     string
		wantSyncs int
		wantCalls string
	}{
		{stage: "verify", wantSyncs: 2, wantCalls: "operator,persist,ledger,operator,persist,ledger"},
		{stage: "runner", wantCalls: "ledger,ledger"},
		{stage: "status", wantCalls: "ledger,ledger"},
	} {
		t.Run(test.stage, func(t *testing.T) {
			var calls []string
			provider := &commandEvidenceProvider{label: "operator", calls: &calls, snapshot: providerSnapshot}
			native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
				SubjectRevision: "head-abc", Provider: &commandEvidenceProvider{label: "ledger", calls: &calls, snapshot: ledgerSnapshot}}, calls: &calls}
			app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
			app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) { return provider, nil }
			for i := 0; i < 2; i++ {
				result, hosted, err := app.externalGateWithProfile(t.Context(), auth.Profile{Kind: auth.ProfileKindHosted}, "token",
					"acme/widgets", 9, "code_change", "head-abc", coreevidence.GateVerify, t.TempDir(), test.stage)
				if err != nil || !hosted || !result.Evaluation.Passed {
					t.Fatalf("run %d result=%+v hosted=%t err=%v", i+1, result, hosted, err)
				}
			}
			if native.syncs != test.wantSyncs || strings.Join(calls, ",") != test.wantCalls {
				t.Fatalf("syncs=%d calls=%v", native.syncs, calls)
			}
		})
	}
}

func TestExternalVerifyRevisionMismatchDoesNotPersistProviderFacts(t *testing.T) {
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	for _, message := range []string{
		"revision_mismatch: requested revision is no longer current",
		"revision_mismatch: HEAD moved while collecting facts",
	} {
		t.Run(message, func(t *testing.T) {
			var calls []string
			native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
				SubjectRevision: "head-abc", Provider: &commandEvidenceProvider{label: "ledger", calls: &calls}}, calls: &calls}
			app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
			app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) {
				return &commandEvidenceProvider{label: "operator", calls: &calls, snapshotErr: errors.New(message)}, nil
			}
			_, hosted, err := app.externalGateWithProfile(t.Context(), auth.Profile{Kind: auth.ProfileKindHosted}, "token",
				"acme/widgets", 9, "code_change", "head-abc", coreevidence.GateVerify, t.TempDir(), "verify")
			if err == nil || !hosted || !strings.Contains(err.Error(), "revision_mismatch") || native.syncs != 0 ||
				strings.Join(calls, ",") != "operator" {
				t.Fatalf("hosted=%t err=%v syncs=%d calls=%v", hosted, err, native.syncs, calls)
			}
		})
	}
}

func TestExternalReviewAndVerifyProjectCompletionPolicyAndAllowZeroFindings(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "issue-spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "issue-spec", "config.yaml"), []byte(`external_code:
  provider_key: code.example
  evidence:
    required: [review, check]
    freshness:
      review: 2h
      check: 3h
`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	check := testEvidenceRecord("check-ledger", codereview.EvidenceCheck, "passed", "head-abc", now)
	check.Name = "unit"
	provider := &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
		Reference: reference, SubjectRevision: "head-abc", CapturedAt: now}}
	for _, test := range []struct {
		name    string
		gate    coreevidence.Gate
		records []codereview.EvidenceRecord
	}{
		{name: "review", gate: coreevidence.GateReview, records: []codereview.EvidenceRecord{check}},
		{name: "verify", gate: coreevidence.GateVerify, records: []codereview.EvidenceRecord{check}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
				Reference: reference, SubjectRevision: "head-abc", CapturedAt: now, Records: test.records}}
			native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
				SubjectRevision: "head-abc", Policy: coreevidence.NativePolicy{Requirements: []coreevidence.NativeRequirement{
					{Kind: codereview.EvidenceReview, Freshness: time.Hour},
				}}, Provider: ledger}}
			app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
			app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) { return provider, nil }
			result, hosted, err := app.externalGateWithProfile(t.Context(), auth.Profile{Kind: auth.ProfileKindHosted}, "token",
				"acme/widgets", 9, "code_change", "head-abc", test.gate, root, string(test.gate))
			if err != nil || !hosted || !result.Evaluation.Passed || !result.ReviewCompletionPolicy.Required ||
				result.ReviewCompletionPolicy.Freshness != time.Hour {
				t.Fatalf("result=%+v hosted=%t err=%v", result, hosted, err)
			}
		})
	}
}

func TestProjectReviewCompletionPolicyPreservesOtherEvidencePolicy(t *testing.T) {
	config := &workflow.ExternalCodeConfig{Evidence: workflow.EvidencePolicyConfig{
		Required: []string{"review", "check"}, Freshness: map[string]string{"review": "2h", "check": "3h"},
	}}
	policy, err := mergedEvidencePolicy(config, coreevidence.NativePolicy{Requirements: []coreevidence.NativeRequirement{
		{Kind: codereview.EvidenceReview, Freshness: time.Hour},
	}})
	if err != nil {
		t.Fatal(err)
	}
	completion := projectReviewCompletionPolicy(&policy)
	if !completion.Required || completion.Freshness != time.Hour ||
		strings.Join(evidenceKindStrings(policy.RequiredKinds), ",") != "check" ||
		policy.Freshness[codereview.EvidenceReview] != 0 || policy.Freshness[codereview.EvidenceCheck] != 3*time.Hour {
		t.Fatalf("completion=%+v provider_policy=%+v", completion, policy)
	}
}

func evidenceKindStrings(values []codereview.EvidenceKind) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func TestExternalReviewGateRejectsInexactProviderSnapshotBeforePersistence(t *testing.T) {
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	base := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
		SubjectRevision: "head-abc", CapturedAt: now}
	tests := map[string]func(*codereview.Snapshot){
		"provider":   func(snapshot *codereview.Snapshot) { snapshot.Reference.ProviderKey = "other.example" },
		"repository": func(snapshot *codereview.Snapshot) { snapshot.Reference.ExternalRepository = "other/widgets" },
		"change":     func(snapshot *codereview.Snapshot) { snapshot.Reference.ChangeID = "change-99" },
		"revision":   func(snapshot *codereview.Snapshot) { snapshot.SubjectRevision = "head-old" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			snapshot := base
			mutate(&snapshot)
			native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
				SubjectRevision: "head-abc", Provider: &commandEvidenceProvider{}}}
			app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
			app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) {
				return &commandEvidenceProvider{snapshot: snapshot}, nil
			}
			_, hosted, err := app.externalGateWithProfile(t.Context(), auth.Profile{Kind: auth.ProfileKindHosted}, "token",
				"acme/widgets", 9, "code_change", "head-abc", coreevidence.GateReview, t.TempDir(), "review")
			if err == nil || !hosted || !strings.Contains(err.Error(), "snapshot "+name) || native.syncs != 0 || native.resolveCalls != 1 {
				t.Fatalf("error=%v hosted=%t syncs=%d resolve_calls=%d", err, hosted, native.syncs, native.resolveCalls)
			}
		})
	}
}

func TestExternalReviewGateRejectsReferenceMovementBeforeLedgerEvaluation(t *testing.T) {
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	initial := coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7, SubjectRevision: "head-abc",
		Provider: &commandEvidenceProvider{label: "ledger"}}
	provider := &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
		Reference: reference, SubjectRevision: "head-abc", CapturedAt: now}}
	tests := map[string]func(*coreevidence.NativeTarget){
		"provider":   func(target *coreevidence.NativeTarget) { target.Reference.ProviderKey = "other.example" },
		"repository": func(target *coreevidence.NativeTarget) { target.Reference.ExternalRepository = "other/widgets" },
		"change":     func(target *coreevidence.NativeTarget) { target.Reference.ChangeID = "change-99" },
		"version":    func(target *coreevidence.NativeTarget) { target.ReferenceVersion++ },
		"revision":   func(target *coreevidence.NativeTarget) { target.SubjectRevision = "head-next" },
	}
	for name, move := range tests {
		t.Run(name, func(t *testing.T) {
			moved := initial
			move(&moved)
			var calls []string
			initial.Provider = &commandEvidenceProvider{label: "ledger", calls: &calls}
			moved.Provider = initial.Provider
			native := &commandNativeEvidence{targets: []coreevidence.NativeTarget{initial, moved}, calls: &calls}
			app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
			app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) { return provider, nil }
			_, hosted, err := app.externalGateWithProfile(t.Context(), auth.Profile{Kind: auth.ProfileKindHosted}, "token",
				"acme/widgets", 9, "code_change", "head-abc", coreevidence.GateReview, t.TempDir(), "review")
			if err == nil || !hosted || !strings.Contains(err.Error(), "reference "+name+" moved") ||
				strings.Join(calls, ",") != "persist" {
				t.Fatalf("error=%v hosted=%t calls=%v", err, hosted, calls)
			}
		})
	}
}

func TestExternalReviewGateRejectsMissingOrIncapableProviderBeforePersistence(t *testing.T) {
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	for _, test := range []struct {
		name     string
		resolver func(context.Context, auth.Profile, string) (codereview.Provider, error)
		want     string
	}{
		{name: "missing", resolver: func(context.Context, auth.Profile, string) (codereview.Provider, error) {
			return nil, codereview.ErrProviderNotFound
		}, want: "not registered by the operator"},
		{name: "incapable", resolver: func(context.Context, auth.Profile, string) (codereview.Provider, error) {
			return &commandEvidenceProvider{capabilities: []codereview.Capability{codereview.CapabilityChangeComment}}, nil
		}, want: "evidence.snapshot"},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
				SubjectRevision: "head-abc", Provider: &commandEvidenceProvider{snapshot: codereview.Snapshot{CapturedAt: now}}}}
			app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
			app.lookupOperatorProvider = test.resolver
			_, _, err := app.externalGateWithProfile(t.Context(), auth.Profile{Kind: auth.ProfileKindHosted}, "token",
				"acme/widgets", 9, "code_change", "head-abc", coreevidence.GateReview, t.TempDir(), "review")
			if err == nil || !strings.Contains(err.Error(), test.want) || native.syncs != 0 {
				t.Fatalf("error=%v syncs=%d", err, native.syncs)
			}
		})
	}
}

func TestAuthoritativeExternalEvidenceBindingsFailClosed(t *testing.T) {
	now := time.Now().UTC()
	valid := testEvidenceRecord("review-1", codereview.EvidenceReview, "resolved", "head-abc", now)
	consumption := externalEvidenceConsumption{SubjectRevision: "head-abc", EvidenceIDs: []string{"review-1"}}
	tests := map[string]func(*codereview.Snapshot, *externalEvidenceConsumption){
		"unknown selected id": func(_ *codereview.Snapshot, c *externalEvidenceConsumption) { c.EvidenceIDs = []string{"missing"} },
		"untrusted selected":  func(s *codereview.Snapshot, _ *externalEvidenceConsumption) { s.Records[0].Trusted = false },
		"wrong revision": func(s *codereview.Snapshot, _ *externalEvidenceConsumption) {
			s.Records[0].SubjectRevision = "head-old"
		},
		"missing process": func(s *codereview.Snapshot, _ *externalEvidenceConsumption) { s.Records[0].ProcessID = "" },
		"missing spec":    func(s *codereview.Snapshot, _ *externalEvidenceConsumption) { s.Records[0].SpecID = "" },
		"provider facts only": func(s *codereview.Snapshot, _ *externalEvidenceConsumption) {
			s.Records = nil
			s.Facts = []codereview.ProviderFact{{ID: "review-1", Kind: codereview.EvidenceReview, SubjectRevision: "head-abc", ProcessID: "PROCESS-001", SpecID: "SPEC-001"}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			snapshot := codereview.Snapshot{SubjectRevision: "head-abc", Records: []codereview.EvidenceRecord{valid}}
			candidate := consumption
			mutate(&snapshot, &candidate)
			if bindings, err := authoritativeExternalEvidenceBindings(snapshot, candidate); err == nil || len(bindings) != 0 {
				t.Fatalf("bindings=%+v err=%v", bindings, err)
			}
		})
	}
}

func TestAuthoritativeExternalEvidenceBindingsIgnoreUnselectedAndSortDedup(t *testing.T) {
	now := time.Now().UTC()
	first := testEvidenceRecord("review-b", codereview.EvidenceReview, "resolved", "head-abc", now)
	first.ProcessID, first.SpecID = "PROCESS-002", "SPEC-002"
	second := testEvidenceRecord("review-a", codereview.EvidenceReview, "resolved", "head-abc", now)
	second.ProcessID, second.SpecID = "PROCESS-001", "SPEC-001"
	unselected := testEvidenceRecord("ignored", codereview.EvidenceReview, "resolved", "head-old", now)
	unselected.Trusted = false
	snapshot := codereview.Snapshot{Records: []codereview.EvidenceRecord{first, unselected, second}}
	consumption := externalEvidenceConsumption{SubjectRevision: "head-abc", EvidenceIDs: []string{"review-b", "review-a", "review-a"}}
	bindings, err := authoritativeExternalEvidenceBindings(snapshot, consumption)
	if err != nil || len(bindings) != 2 || bindings[0].ProcessID != "PROCESS-001" || bindings[1].ProcessID != "PROCESS-002" {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	duplicated := append(bindings, bindings[0])
	if normalized := normalizeExternalEvidenceBindings(duplicated); len(normalized) != 2 || normalized[0] != bindings[0] {
		t.Fatalf("normalized=%+v", normalized)
	}
}

func TestResolveOperatorProviderUsesProfileRegistry(t *testing.T) {
	clearCommandAuthEnv(t)
	t.Setenv(codereview.OperatorProvidersFileEnv, "")
	profile := auth.Profile{OperatorRegistryFile: writeEvidenceOperatorRegistry(t, "profile.example")}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	provider, err := app.resolveOperatorProvider(t.Context(), profile, "profile.example")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := provider.Capabilities(t.Context())
	if err != nil || !capabilities.Has(codereview.CapabilityEvidenceSnapshot) {
		t.Fatalf("capabilities=%+v err=%v", capabilities, err)
	}
}

func TestResolveOperatorProviderEnvironmentPrecedesProfileRegistry(t *testing.T) {
	clearCommandAuthEnv(t)
	envRegistry := writeEvidenceOperatorRegistry(t, "env.example")
	t.Setenv(codereview.OperatorProvidersFileEnv, envRegistry)
	profile := auth.Profile{OperatorRegistryFile: filepath.Join(t.TempDir(), "missing-profile-registry.json")}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	provider, err := app.resolveOperatorProvider(t.Context(), profile, "env.example")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := provider.Capabilities(t.Context())
	if err != nil || !capabilities.Has(codereview.CapabilityEvidenceSnapshot) {
		t.Fatalf("capabilities=%+v err=%v", capabilities, err)
	}
}

func TestResolveOperatorProviderFailsClosedForInvalidProfileRegistry(t *testing.T) {
	clearCommandAuthEnv(t)
	t.Setenv(codereview.OperatorProvidersFileEnv, "")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	profile := auth.Profile{OperatorRegistryFile: filepath.Join(t.TempDir(), "missing-profile-registry.json")}
	if _, err := app.resolveOperatorProvider(t.Context(), profile, "profile.example"); err == nil ||
		!strings.Contains(err.Error(), "private regular file") {
		t.Fatalf("missing profile registry error=%v", err)
	}
}

func TestResolveOperatorProviderRetainsHermeticSeamWithoutRegistry(t *testing.T) {
	clearCommandAuthEnv(t)
	t.Setenv(codereview.OperatorProvidersFileEnv, "")
	want := &commandEvidenceProvider{}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) { return want, nil }

	got, err := app.resolveOperatorProvider(t.Context(), auth.Profile{}, "test.example")
	if err != nil || got != want {
		t.Fatalf("provider=%T err=%v", got, err)
	}
}

func TestExternalMutationTargetUsesProfileAwareResolverAndRequiresMutationCapability(t *testing.T) {
	clearCommandAuthEnv(t)
	t.Chdir(t.TempDir())
	profile := auth.Profile{Name: "mutation-profile", Kind: auth.ProfileKindHosted,
		APIURL: "https://issues.example/api/v3", NativeAPIURL: "https://issues.example/api/v1",
		WebURL: "https://issues.example", ServerInstanceID: "mutation-profile-instance",
		OperatorRegistryFile: "/operator/profile/providers.json"}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
		SubjectRevision: "head-abc"}}
	provider := &commandEvidenceProvider{capabilities: []codereview.Capability{codereview.CapabilityChangeComment}}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.profileName = profile.Name
	app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
	app.lookupOperatorProvider = func(_ context.Context, gotProfile auth.Profile, key string) (codereview.Provider, error) {
		if gotProfile.Name != profile.Name || gotProfile.OperatorRegistryFile != profile.OperatorRegistryFile || key != reference.ProviderKey {
			t.Fatalf("profile=%+v key=%q", gotProfile, key)
		}
		return provider, nil
	}
	target, mutation, _, hosted, err := app.externalMutationTarget(t.Context(), "github.com", "token", "acme/widgets", 9,
		"code_change", "head-abc", codereview.CapabilityChangeComment)
	if err != nil || !hosted || mutation != provider || target.ReferenceVersion != 7 {
		t.Fatalf("target=%+v provider=%T hosted=%t err=%v", target, mutation, hosted, err)
	}

	provider.capabilities = []codereview.Capability{codereview.CapabilityEvidenceSnapshot}
	if _, _, _, _, err := app.externalMutationTarget(t.Context(), "github.com", "token", "acme/widgets", 9,
		"code_change", "head-abc", codereview.CapabilityChangeComment); err == nil ||
		!strings.Contains(err.Error(), string(codereview.CapabilityChangeComment)) || provider.mutationCalls != 0 {
		t.Fatalf("incapable mutation error=%v calls=%d", err, provider.mutationCalls)
	}

	app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) {
		return commandReadOnlyProvider{capabilities: []codereview.Capability{codereview.CapabilityChangeComment}}, nil
	}
	if _, _, _, _, err := app.externalMutationTarget(t.Context(), "github.com", "token", "acme/widgets", 9,
		"code_change", "head-abc", codereview.CapabilityChangeComment); err == nil || !strings.Contains(err.Error(), "does not implement mutations") {
		t.Fatalf("non-mutation provider error=%v", err)
	}
}

func writeEvidenceOperatorRegistry(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "providers.json")
	raw, err := json.Marshal(map[string]any{"version": 1, "providers": map[string]any{
		key: map[string]any{
			"path":             os.Args[0],
			"args":             []string{"-test.run=^TestOperatorBridgeCLIHelper$"},
			"environment":      []string{"ISSUE_SPEC_OPERATOR_BRIDGE_HELPER=1"},
			"timeout":          "10s",
			"max_output_bytes": 1 << 20,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCommandNativeEvidenceClientUsesExactReferenceCAS(t *testing.T) {
	orgID, repoID, issueID, referenceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	basePath := fmt.Sprintf("/api/v1/orgs/%s/repos/%s/issues/%s", orgID, repoID, issueID)
	posted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "sync-test")
		if r.Header.Get("Authorization") != "Bearer realm-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == basePath+"/references":
			_ = json.NewEncoder(w).Encode(map[string]any{"references": []any{map[string]any{
				"id": referenceID, "issue_id": issueID, "provider_key": reference.ProviderKey, "relation_kind": "code_change",
				"external_repository_id": reference.ExternalRepository, "external_id": reference.ChangeID,
				"canonical_url": "https://code.example/changes/42", "lifecycle_state": "active", "visibility": "repository",
				"metadata": map[string]string{"head_revision": "head-abc"}, "representation_version": 7,
				"created_at": now, "updated_at": now,
			}}})
		case r.Method == http.MethodPost && r.URL.Path == basePath+"/evidence/snapshots":
			var body struct {
				ReferenceID              uuid.UUID           `json:"reference_id"`
				ExpectedReferenceVersion int64               `json:"expected_reference_version"`
				Snapshot                 codereview.Snapshot `json:"snapshot"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.ReferenceID != referenceID || body.ExpectedReferenceVersion != 7 || len(body.Snapshot.Facts) != 1 || body.Snapshot.Records != nil {
				t.Errorf("snapshot body = %+v", body)
			}
			posted = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"reference_id": referenceID, "reference_version": 7,
				"subject_revision": "head-abc", "evidence": []any{map[string]any{"id": uuid.New()}}, "created": 1, "replayed": 0})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 404})
		}
	}))
	defer server.Close()
	profile := auth.Profile{Name: "sync-http", Kind: auth.ProfileKindHosted, APIURL: server.URL + "/api/v3",
		NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL, ServerInstanceID: "instance-sync"}
	api, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.NativeAPIURL, Token: "realm-token"})
	if err != nil {
		t.Fatal(err)
	}
	client := &commandNativeEvidenceClient{api: api}
	snapshot := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
		SubjectRevision: "head-abc", CapturedAt: now, Facts: []codereview.ProviderFact{{ID: "check-fact", ExternalID: "unit",
			Kind: codereview.EvidenceCheck, State: "passed", SubjectRevision: "head-abc", Name: "unit", ObservedAt: now,
			PayloadDigest: strings.Repeat("a", 64)}}}
	target := coreevidence.NativeTarget{Reference: reference, SubjectRevision: "head-abc", OrgID: orgID, RepoID: repoID, IssueID: issueID}
	if err := client.SynchronizeSnapshot(t.Context(), target, snapshot); err != nil {
		t.Fatal(err)
	}
	if !posted {
		t.Fatal("snapshot was not persisted")
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
	if _, _, err := stampConsumedEvidence(body, externalEvidenceConsumption{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code",
		ChangeID: "change-42", SubjectRevision: "head-abc", EvidenceIDs: []string{"review-1"}}); err == nil {
		t.Fatal("revision-only consumption without structured binding was accepted")
	}
	consumption := externalEvidenceConsumption{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code",
		ChangeID: "change-42", ReferenceVersion: 7, SubjectRevision: "head-abc", EvidenceIDs: []string{"z", "a"}, Bindings: []externalEvidenceBinding{
			{ProcessID: "PROCESS-002", SpecID: "SPEC-002", EvidenceID: "z", Kind: codereview.EvidenceReview, SubjectRevision: "head-abc", Trusted: true, Source: "native-authoritative-ledger"},
			{ProcessID: "PROCESS-001", SpecID: "SPEC-001", EvidenceID: "a", Kind: codereview.EvidenceReview, SubjectRevision: "head-abc", Trusted: true, Source: "native-authoritative-ledger"},
		}}
	first, changed, err := stampConsumedEvidence(body, consumption)
	if err != nil || !changed || !strings.Contains(first, `"reference_version":7`) || !strings.Contains(first, `"evidence_ids":["a","z"]`) ||
		!strings.Contains(first, `"process_id":"PROCESS-001"`) || strings.Index(first, "PROCESS-001") > strings.Index(first, "PROCESS-002") {
		t.Fatalf("first stamp changed=%v err=%v body=%q", changed, err, first)
	}
	second, changed, err := stampConsumedEvidence(first, consumption)
	if err != nil || changed || second != first || strings.Count(second, consumedEvidenceStart) != 1 {
		t.Fatalf("second stamp changed=%v err=%v body=%q", changed, err, second)
	}
	malformed := map[string]string{
		"duplicate complete block": first + "\n" + first,
		"duplicate start":          first + "\n" + consumedEvidenceStart,
		"duplicate end":            first + "\n" + consumedEvidenceEnd,
		"interleaved":              body + "\n" + consumedEvidenceEnd + "\n" + consumedEvidenceStart,
	}
	for name, candidate := range malformed {
		t.Run(name, func(t *testing.T) {
			if _, _, err := stampConsumedEvidence(candidate, consumption); err == nil {
				t.Fatal("malformed consumed evidence markers were accepted")
			}
		})
	}
}

func TestExternalReviewSyncPreservesCanonicalFindingLinkage(t *testing.T) {
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	review := testEvidenceRecord("review-1", codereview.EvidenceReview, "resolved", "head-abc", now)
	review.FindingID, review.ProcessID, review.SpecID = "FINDING-030", "PROCESS-020", "SPEC-010"
	review.CanonicalURL = "https://code.example/reviews/30"
	gate := externalGateResult{Target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7, SubjectRevision: "head-abc"},
		Snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
			SubjectRevision: "head-abc", CapturedAt: now, Records: []codereview.EvidenceRecord{review}},
		Evaluation: coreevidence.Result{Passed: true, EvidenceIDs: []string{"review-1"}},
		Consumption: externalEvidenceConsumption{ProviderKey: reference.ProviderKey,
			ExternalRepository: reference.ExternalRepository, ChangeID: reference.ChangeID,
			ReferenceVersion: 7, SubjectRevision: "head-abc", EvidenceIDs: []string{"review-1"}, Bindings: []externalEvidenceBinding{{
				ProcessID: "PROCESS-020", SpecID: "SPEC-010", EvidenceID: "review-1", Kind: codereview.EvidenceReview,
				SubjectRevision: "head-abc", Trusted: true, Source: "native-authoritative-ledger",
			}}}}
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
	const authoritativeBinding = `"bindings":[{"process_id":"PROCESS-020","spec_id":"SPEC-010","evidence_id":"review-1","kind":"review","subject_revision":"head-abc","trusted":true,"source":"native-authoritative-ledger"}]`
	if !strings.Contains(body, authoritativeBinding) {
		t.Fatalf("canonical REVIEW missing authoritative consumed-evidence binding %q:\n%s", authoritativeBinding, body)
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
	snapshot        codereview.Snapshot
	snapshotErr     error
	capabilities    []codereview.Capability
	capabilitiesErr error
	mutation        codereview.MutationResult
	mutationCalls   int
	mutateErr       error
	label           string
	calls           *[]string
}

type commandReadOnlyProvider struct {
	capabilities []codereview.Capability
}

func (p commandReadOnlyProvider) Capabilities(context.Context) (codereview.Capabilities, error) {
	return codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion, Values: p.capabilities}, nil
}

func (commandReadOnlyProvider) Snapshot(context.Context, codereview.SnapshotRequest) (codereview.Snapshot, error) {
	return codereview.Snapshot{}, errors.New("snapshot should not be called for a mutation preflight")
}

func (p *commandEvidenceProvider) Capabilities(context.Context) (codereview.Capabilities, error) {
	if p.capabilitiesErr != nil {
		return codereview.Capabilities{}, p.capabilitiesErr
	}
	values := p.capabilities
	if values == nil {
		values = []codereview.Capability{codereview.CapabilityEvidenceSnapshot}
	}
	return codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion, Values: values}, nil
}

func (p *commandEvidenceProvider) Snapshot(context.Context, codereview.SnapshotRequest) (codereview.Snapshot, error) {
	if p.calls != nil {
		*p.calls = append(*p.calls, p.label)
	}
	return p.snapshot, p.snapshotErr
}

func (p *commandEvidenceProvider) Mutate(context.Context, codereview.MutationRequest) (codereview.MutationResult, error) {
	p.mutationCalls++
	return p.mutation, p.mutateErr
}

type commandNativeEvidence struct {
	target       coreevidence.NativeTarget
	targets      []coreevidence.NativeTarget
	resolveCalls int
	resolveErr   error
	upserts      int
	syncs        int
	snapshot     codereview.Snapshot
	syncErr      error
	calls        *[]string
}

func (n *commandNativeEvidence) ResolveTarget(context.Context, string, int, string) (coreevidence.NativeTarget, error) {
	n.resolveCalls++
	if n.resolveErr != nil {
		return coreevidence.NativeTarget{}, n.resolveErr
	}
	if len(n.targets) > 0 {
		index := n.resolveCalls - 1
		if index >= len(n.targets) {
			index = len(n.targets) - 1
		}
		return n.targets[index], nil
	}
	return n.target, nil
}

func (n *commandNativeEvidence) UpsertArchiveReference(context.Context, coreevidence.NativeTarget, codereview.Reference, string, string, string) error {
	n.upserts++
	return nil
}

func (n *commandNativeEvidence) SynchronizeSnapshot(_ context.Context, _ coreevidence.NativeTarget, snapshot codereview.Snapshot) error {
	n.syncs++
	n.snapshot = snapshot
	if n.calls != nil {
		*n.calls = append(*n.calls, "persist")
	}
	return n.syncErr
}
