package commands

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

func TestReviewReceiptBindingAndImmutableProjection(t *testing.T) {
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleReview,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	if err := validateReviewReceiptBinding(receipt, binding); err != nil {
		t.Fatal(err)
	}
	body := "canonical REVIEW\n"
	projected := acceptedReviewReceiptFrom(receipt)
	stamped, changed, err := stampAcceptedReviewReceipt(body, projected)
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	parsed, found, err := parseAcceptedReviewReceipt(stamped)
	if err != nil || !found || parsed.ReceiptDigest != receipt.ReceiptDigest || parsed.Verdict != assignment.ReviewApprove {
		t.Fatalf("parsed=%+v found=%t err=%v", parsed, found, err)
	}
	if retry, changed, err := stampAcceptedReviewReceipt(stamped, projected); err != nil || changed || retry != stamped {
		t.Fatalf("retry changed=%t err=%v", changed, err)
	}
	other := projected
	other.ReceiptID = "receipt-review-other"
	if _, _, err := stampAcceptedReviewReceipt(stamped, other); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("conflicting accepted receipt error=%v", err)
	}
}

func TestReviewReceiptBindingRejectsAuthorityDrift(t *testing.T) {
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	valid := processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleReview,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	tests := map[string]func(*assignment.Receipt, *processworkspace.AssignmentBinding){
		"generation": func(_ *assignment.Receipt, b *processworkspace.AssignmentBinding) { b.Generation++ },
		"revision":   func(_ *assignment.Receipt, b *processworkspace.AssignmentBinding) { b.SubjectRevision = "head-old" },
		"coordinator": func(r *assignment.Receipt, _ *processworkspace.AssignmentBinding) {
			r.Provenance.Writer, r.Provenance.Subject = "Coordinator", "Coordinator"
		},
		"impersonated": func(r *assignment.Receipt, _ *processworkspace.AssignmentBinding) {
			r.Provenance.Subject = "Another Reviewer"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate, binding := receipt, valid
			mutate(&candidate, &binding)
			if err := validateReviewReceiptBinding(candidate, &binding); err == nil {
				t.Fatal("authority drift was accepted")
			}
		})
	}
}

func testSealedReviewReceipt(t *testing.T, verdict assignment.ReviewVerdict, findings []assignment.Finding) assignment.Receipt {
	t.Helper()
	value := assignment.Receipt{SchemaVersion: assignment.ReceiptSchemaVersion, ID: "receipt-review-1",
		AssignmentID: "assignment-review-1", AssignmentDigest: strings.Repeat("a", 64), AssignmentGeneration: 1,
		Role: assignment.RoleReview, ResultSchemaVersion: assignment.ReceiptSchemaVersion, SubjectRevision: "head-abc",
		Provenance: assignment.Provenance{Route: assignment.RouteRoleOwned, Assurance: assignment.AssuranceSelfReported,
			Writer: "Independent Reviewer", Subject: "Independent Reviewer", Source: "review-submit"},
		Review: &assignment.ReviewResult{Verdict: verdict, Findings: findings}}
	sealed, err := assignment.SealReceipt(value)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestValidateExternalReviewCompletionControlledTime(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	target := testReviewCompletionTarget()
	tests := []struct {
		name         string
		synchronized time.Time
		policy       ReviewCompletionPolicy
		want         string
	}{
		{name: "fresh", synchronized: now.Add(-30 * time.Minute), policy: ReviewCompletionPolicy{Required: true, Freshness: time.Hour}},
		{name: "freshness boundary", synchronized: now.Add(-time.Hour), policy: ReviewCompletionPolicy{Required: true, Freshness: time.Hour}},
		{name: "expired", synchronized: now.Add(-time.Hour - time.Nanosecond), policy: ReviewCompletionPolicy{Required: true, Freshness: time.Hour}, want: "older"},
		{name: "small clock skew", synchronized: now.Add(time.Minute), policy: ReviewCompletionPolicy{Required: true}},
		{name: "future without freshness", synchronized: now.Add(time.Minute + time.Nanosecond), policy: ReviewCompletionPolicy{Required: true}, want: "future"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			completion := completionForTarget(target, test.synchronized)
			review := reviewWithCompletion(t, completion, target.SubjectRevision, "done")
			err := validateExternalReviewCompletionAt(review, target, test.policy, now)
			if test.want == "" && err != nil {
				t.Fatal(err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateExternalReviewCompletionStrictArtifactMatrix(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	target := testReviewCompletionTarget()
	valid := completionForTarget(target, now.Add(-time.Minute))
	base := reviewWithCompletion(t, valid, target.SubjectRevision, "done")
	unstamped := reviewWithoutCompletion(t, target.SubjectRevision, "done")
	ordinary := model.ParseTypedComment("generic comment edit")
	nonUTC := valid
	nonUTC.SynchronizedAt = now.In(time.FixedZone("UTC+8", 8*60*60))

	tests := []struct {
		name   string
		review model.TypedComment
		target coreevidence.NativeTarget
		policy ReviewCompletionPolicy
		want   string
	}{
		{name: "canonical", review: base, target: target, policy: ReviewCompletionPolicy{Required: true}},
		{name: "optional unstamped", review: unstamped, target: target},
		{name: "required unstamped", review: unstamped, target: target, policy: ReviewCompletionPolicy{Required: true}, want: "required"},
		{name: "generic comment edit", review: ordinary, target: target, policy: ReviewCompletionPolicy{Required: true}, want: "required"},
		{name: "not done", review: reviewWithCompletion(t, valid, target.SubjectRevision, "in-progress"), target: target,
			policy: ReviewCompletionPolicy{Required: true}, want: "done REVIEW"},
		{name: "wrong header revision", review: reviewWithCompletion(t, valid, "head-old", "done"), target: target,
			policy: ReviewCompletionPolicy{Required: true}, want: "revision"},
		{name: "non UTC", review: reviewWithCompletion(t, nonUTC, target.SubjectRevision, "done"), target: target,
			policy: ReviewCompletionPolicy{Required: true}, want: "must be UTC"},
		{name: "negative freshness", review: base, target: target, policy: ReviewCompletionPolicy{Required: true, Freshness: -time.Second}, want: "policy is invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateExternalReviewCompletionAt(test.review, test.target, test.policy, now)
			if test.want == "" && err != nil {
				t.Fatal(err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateExternalReviewCompletionRejectsWrongExactIdentity(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	target := testReviewCompletionTarget()
	base := completionForTarget(target, now)
	tests := map[string]func(*externalReviewCompletion){
		"provider":   func(value *externalReviewCompletion) { value.ProviderKey = "other.example" },
		"repository": func(value *externalReviewCompletion) { value.ExternalRepository = "other/widgets" },
		"change":     func(value *externalReviewCompletion) { value.ChangeID = "change-99" },
		"version":    func(value *externalReviewCompletion) { value.ReferenceVersion++ },
		"revision":   func(value *externalReviewCompletion) { value.SubjectRevision = "head-old" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			completion := base
			mutate(&completion)
			review := reviewWithCompletion(t, completion, target.SubjectRevision, "done")
			if err := validateExternalReviewCompletionAt(review, target, ReviewCompletionPolicy{Required: true}, now); err == nil {
				t.Fatal("wrong exact identity was accepted")
			}
		})
	}
}

func TestParseExternalReviewCompletionRejectsMalformedOrNoncanonicalBlock(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	target := testReviewCompletionTarget()
	completion := completionForTarget(target, now)
	canonical, err := json.Marshal(completion)
	if err != nil {
		t.Fatal(err)
	}
	base := reviewWithoutCompletion(t, target.SubjectRevision, "done").Body
	validBlock := externalReviewCompletionStart + "\n" + string(canonical) + "\n" + externalReviewCompletionEnd
	extra := strings.TrimSuffix(string(canonical), "}") + `,"approved":true}`
	reordered := `{"synchronized_at":"2026-07-17T08:00:00Z","provider_key":"code.example","external_repository":"acme/widgets-code","change_id":"change-42","reference_version":7,"subject_revision":"head-abc"}`
	tests := map[string]string{
		"extra field":         base + externalReviewCompletionStart + "\n" + extra + "\n" + externalReviewCompletionEnd,
		"reordered fields":    base + externalReviewCompletionStart + "\n" + reordered + "\n" + externalReviewCompletionEnd,
		"unsupported version": base + strings.Replace(validBlock, "version=1", "version=2", 1),
		"duplicate pair":      base + validBlock + "\n" + validBlock,
		"closing before open": base + externalReviewCompletionEnd + "\n" + externalReviewCompletionStart + "\n" + string(canonical) + "\n",
		"payload framing":     base + externalReviewCompletionStart + string(canonical) + externalReviewCompletionEnd,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, found, err := parseExternalReviewCompletion(body); !found || err == nil {
				t.Fatalf("found=%t err=%v", found, err)
			}
		})
	}
}

func testReviewCompletionTarget() coreevidence.NativeTarget {
	return coreevidence.NativeTarget{Reference: codereview.Reference{ProviderKey: "code.example",
		ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}, ReferenceVersion: 7, SubjectRevision: "head-abc"}
}

func completionForTarget(target coreevidence.NativeTarget, synchronized time.Time) externalReviewCompletion {
	return externalReviewCompletion{ProviderKey: target.Reference.ProviderKey,
		ExternalRepository: target.Reference.ExternalRepository, ChangeID: target.Reference.ChangeID,
		ReferenceVersion: target.ReferenceVersion, SubjectRevision: target.SubjectRevision, SynchronizedAt: synchronized}
}

func reviewWithoutCompletion(t *testing.T, revision, status string) model.TypedComment {
	t.Helper()
	body, err := model.EnsureTypedBody("REVIEW", "REVIEW-101", "## Review\n\nProvider review synchronized.", model.BodyOptions{
		Agent: "Review Agent", SubjectRevision: revision, Status: status, Scope: "external review",
	})
	if err != nil {
		t.Fatal(err)
	}
	return model.ParseTypedComment(body)
}

func reviewWithCompletion(t *testing.T, completion externalReviewCompletion, headerRevision, status string) model.TypedComment {
	t.Helper()
	review := reviewWithoutCompletion(t, headerRevision, status)
	raw, err := json.Marshal(completion)
	if err != nil {
		t.Fatal(err)
	}
	review.Body += externalReviewCompletionStart + "\n" + string(raw) + "\n" + externalReviewCompletionEnd + "\n"
	return model.ParseTypedComment(review.Body)
}
