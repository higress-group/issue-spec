package mergecheck

import (
	"testing"

	"github.com/higress-group/issue-spec/internal/codereview"
)

func TestEvaluateVariesNecessaryFactsOneAtATime(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Input)
		code BlockerCode
	}{
		{"scope", func(in *Input) { in.Scope.SimpleIssue = intp(2) }, BlockerScopeInvalid},
		{"head", func(in *Input) { in.Checks[0].SubjectRevision = "other" }, BlockerCheckConfiguration},
		{"check key", func(in *Input) { in.Checks[0].Identity.Key = "app:7/context:other" }, BlockerCheckConfiguration},
		{"check owner", func(in *Input) { in.Checks[0].Identity.Owner = "app:8" }, BlockerCheckConfiguration},
		{"check conclusion", func(in *Input) { in.Checks[0].Conclusion = codereview.CheckFailure }, BlockerCheckNotSuccessful},
		{"approval count", func(in *Input) { in.Review.Policy.RequiredApprovalCount = 2 }, BlockerReviewApprovalCount},
		{"codeowner", func(in *Input) { in.Review.Policy.CodeOwnerApprovalRequired = true }, BlockerReviewCodeOwner},
		{"stale", func(in *Input) { in.Review.Decisions[0].Verdict = codereview.ReviewStale }, BlockerReviewApprovalCount},
		{"dismissed", func(in *Input) { in.Review.Decisions[0].Verdict = codereview.ReviewDismissed }, BlockerReviewApprovalCount},
		{"changes requested", func(in *Input) { in.Review.Decisions[0].Verdict = codereview.ReviewChangesRequested }, BlockerReviewChangesRequested},
		{"self authored", func(in *Input) {
			in.Review.Decisions[0].Reviewer.CanonicalPrincipal = in.Review.Authors[0].CanonicalPrincipal
		}, BlockerReviewIndependence},
		{"conversation", func(in *Input) {
			in.Review.Policy.ConversationResolutionRequired = true
			in.Review.UnresolvedConversations = []string{"thread:1"}
		}, BlockerReviewConversation},
		{"finding", func(in *Input) {
			in.Review.Findings = []codereview.ReviewFinding{openFinding(in.Review.Decisions[0].Reviewer)}
		}, BlockerReviewFinding},
		{"provider policy", func(in *Input) {
			in.ProviderGate = []ProviderPolicyObservation{{Code: "ruleset", Satisfied: false, Diagnostics: "drift"}}
		}, BlockerProviderPolicy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := readyInput()
			test.edit(&input)
			decision := Evaluate(input)
			if decision.Ready || !hasBlocker(decision, test.code) {
				t.Fatalf("Evaluate() = %+v, want blocker %s", decision, test.code)
			}
		})
	}
}

func TestEvaluateReadyDigestIsDeterministicAndP2DoesNotBlock(t *testing.T) {
	input := readyInput()
	finding := openFinding(input.Review.Decisions[0].Reviewer)
	finding.ID, finding.Severity = "finding:p2", codereview.FindingP2
	input.Review.Findings = []codereview.ReviewFinding{finding}
	first := Evaluate(input)
	input.Required[0], input.Checks[0] = input.Required[0], input.Checks[0]
	second := Evaluate(input)
	if !first.Ready || first.ExpectedHead != "head:2" || first.SnapshotDigest == "" ||
		first.SnapshotDigest != second.SnapshotDigest || first.Summary.VisibleP2Findings != 1 {
		t.Fatalf("ready decisions = %+v / %+v", first, second)
	}
}

func TestScopeIssueNumbersAreExactAndPlanningFree(t *testing.T) {
	simple := ChangeScope{SimpleIssue: intp(4)}
	proposal := ChangeScope{ProposalIssue: intp(5), DesignIssue: intp(6), ImplementIssue: intp(7)}
	if got := simple.IssueNumbers(); len(got) != 1 || got[0] != 4 {
		t.Fatalf("simple issues = %v", got)
	}
	if got := proposal.IssueNumbers(); len(got) != 3 || got[0] != 5 || got[1] != 6 || got[2] != 7 {
		t.Fatalf("proposal issues = %v", got)
	}
}

func readyInput() Input {
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	check := codereview.CheckIdentity{Provider: "code.example", Key: "app:7/context:unit", Owner: "app:7", DisplayName: "unit"}
	author := actor("user:1", "person:1")
	reviewer := actor("user:2", "person:2")
	return Input{Scope: ChangeScope{ProposalIssue: intp(10), DesignIssue: intp(11)},
		Subject:  CodeSubject{Reference: reference, Revision: "head:2", State: codereview.ChangeOpen},
		Required: []codereview.CheckIdentity{check}, Checks: []codereview.CheckConclusion{{Identity: check,
			SubjectRevision: "head:2", CurrentAttemptID: "run:3", ConfigurationGeneration: "ruleset:2", Conclusion: codereview.CheckSuccess}},
		Review: codereview.ReviewAuthority{Mode: codereview.ReviewProviderNative, AuthorSetComplete: true,
			Authors: []codereview.ActorIdentity{author}, Policy: codereview.ReviewPolicy{RequiredApprovalCount: 1},
			Decisions: []codereview.ReviewDecision{{ID: "review:2", SubjectRevision: "head:2", Reviewer: reviewer,
				Verdict: codereview.ReviewApproved, ObservationID: "observation:2"}}, Findings: []codereview.ReviewFinding{},
			UnresolvedConversations: []string{}}}
}

func actor(source, principal string) codereview.ActorIdentity {
	return codereview.ActorIdentity{Provider: "code.example", StableID: source, Kind: codereview.ActorHuman,
		CanonicalPrincipal: codereview.PrincipalIdentity{Realm: "people.example", StableID: principal}}
}

func openFinding(owner codereview.ActorIdentity) codereview.ReviewFinding {
	return codereview.ReviewFinding{ID: "finding:p1", SubjectRevision: "head:2", Owner: owner,
		Severity: codereview.FindingP1, State: codereview.FindingOpen}
}

func intp(value int) *int { return &value }

func hasBlocker(decision Decision, code BlockerCode) bool {
	for _, blocker := range decision.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}
