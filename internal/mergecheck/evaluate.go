package mergecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/codereview"
)

// Evaluate is a pure function. It performs no reads, writes, time lookups, or
// local check execution and never treats its diagnostic digest as authority.
func Evaluate(input Input) Decision {
	decision := Decision{SchemaVersion: SchemaVersion, ExpectedHead: input.Subject.Revision, Blockers: []Blocker{},
		Summary: Summary{RequiredChecks: len(input.Required), RequiredApprovals: input.Review.Policy.RequiredApprovalCount}}
	decision.SnapshotDigest = inputDigest(input)

	if err := input.Scope.Validate(); err != nil {
		decision.Blockers = append(decision.Blockers, blocker(BlockerScopeInvalid, "scope", err.Error()))
	}
	if err := input.Subject.Reference.Validate(); err != nil || !opaque(input.Subject.Revision, 512) {
		decision.Blockers = append(decision.Blockers, blocker(BlockerSubjectInvalid, "subject", "code subject identity is incomplete"))
	}
	if input.Subject.State != codereview.ChangeOpen {
		decision.Blockers = append(decision.Blockers, blocker(BlockerChangeNotOpen, "subject", fmt.Sprintf("change state is %q", input.Subject.State)))
	}

	evaluateChecks(input, &decision)
	evaluateReview(input, &decision)
	for _, observation := range input.ProviderGate {
		if !opaque(observation.Code, 128) || len(observation.Diagnostics) > 4096 || strings.ContainsRune(observation.Diagnostics, 0) {
			decision.Blockers = append(decision.Blockers, blocker(BlockerProviderPolicy, "provider", "provider policy observation is invalid"))
			continue
		}
		if !observation.Satisfied {
			decision.Blockers = append(decision.Blockers, blocker(BlockerProviderPolicy, observation.Code, observation.Diagnostics))
		}
	}
	sortBlockers(decision.Blockers)
	decision.Ready = len(decision.Blockers) == 0
	return decision
}

func evaluateChecks(input Input, decision *Decision) {
	required := make(map[string]codereview.CheckIdentity, len(input.Required))
	for _, check := range input.Required {
		key := checkKey(check)
		if check.Validate() != nil || check.Provider != input.Subject.Reference.ProviderKey || required[key].Key != "" {
			decision.Blockers = append(decision.Blockers, blocker(BlockerCheckConfiguration, check.Key, "required check identity is invalid or duplicate"))
			continue
		}
		required[key] = check
	}
	seen := make(map[string]bool, len(input.Checks))
	for _, conclusion := range input.Checks {
		key := checkKey(conclusion.Identity)
		_, requested := required[key]
		if conclusion.Validate() != nil || conclusion.SubjectRevision != input.Subject.Revision || seen[key] || !requested {
			decision.Blockers = append(decision.Blockers, blocker(BlockerCheckConfiguration, conclusion.Identity.Key, "current conclusion is invalid, duplicate, wrong-head, or unrequested"))
			continue
		}
		seen[key] = true
		if conclusion.Conclusion == codereview.CheckSuccess {
			decision.Summary.SuccessfulChecks++
			continue
		}
		diagnostic := fmt.Sprintf("current attempt %s concluded %s", conclusion.CurrentAttemptID, conclusion.Conclusion)
		if conclusion.Diagnostics != "" {
			diagnostic += ": " + conclusion.Diagnostics
		}
		decision.Blockers = append(decision.Blockers, Blocker{Code: BlockerCheckNotSuccessful,
			Subject: conclusion.Identity.Key, Diagnostics: diagnostic, URL: conclusion.CanonicalURL})
	}
	for key, check := range required {
		if !seen[key] {
			decision.Blockers = append(decision.Blockers, blocker(BlockerCheckMissing, check.Key, "provider omitted the current conclusion"))
		}
	}
}

func evaluateReview(input Input, decision *Decision) {
	if err := input.Review.Validate(input.Subject.Revision); err != nil {
		decision.Blockers = append(decision.Blockers, blocker(BlockerReviewAuthorityInvalid, "review", err.Error()))
		return
	}
	authors := map[string]bool{}
	for _, author := range input.Review.Authors {
		authors[principalKey(author.CanonicalPrincipal)] = true
	}
	approved := map[string]bool{}
	independent := map[string]bool{}
	for _, current := range input.Review.Decisions {
		principal := principalKey(current.Reviewer.CanonicalPrincipal)
		switch current.Verdict {
		case codereview.ReviewApproved:
			approved[principal] = true
			if !authors[principal] {
				independent[principal] = true
			}
		case codereview.ReviewChangesRequested:
			decision.Blockers = append(decision.Blockers, blocker(BlockerReviewChangesRequested, current.ID, "current reviewer requests changes"))
		}
	}
	decision.Summary.CurrentApprovals = len(approved)
	decision.Summary.IndependentApprovals = len(independent)
	if len(approved) < input.Review.Policy.RequiredApprovalCount {
		decision.Blockers = append(decision.Blockers, blocker(BlockerReviewApprovalCount, "review",
			fmt.Sprintf("have %d current approvals; require %d", len(approved), input.Review.Policy.RequiredApprovalCount)))
	}
	if len(independent) == 0 {
		decision.Blockers = append(decision.Blockers, blocker(BlockerReviewIndependence, "review", "no current approval is independent from the exact-subject author set"))
	}
	if input.Review.Policy.CodeOwnerApprovalRequired && !input.Review.CodeOwnerSatisfied {
		decision.Blockers = append(decision.Blockers, blocker(BlockerReviewCodeOwner, "review", "effective ownership approval is unsatisfied"))
	}
	if input.Review.Policy.ConversationResolutionRequired && len(input.Review.UnresolvedConversations) > 0 {
		for _, conversation := range input.Review.UnresolvedConversations {
			decision.Blockers = append(decision.Blockers, blocker(BlockerReviewConversation, conversation, "conversation is unresolved"))
		}
	}
	for _, finding := range input.Review.Findings {
		if finding.Severity == codereview.FindingP2 {
			decision.Summary.VisibleP2Findings++
		}
		if finding.State == codereview.FindingOpen && (finding.Severity == codereview.FindingP0 || finding.Severity == codereview.FindingP1) {
			decision.Blockers = append(decision.Blockers, Blocker{Code: BlockerReviewFinding, Subject: finding.ID,
				Diagnostics: fmt.Sprintf("open %s finding", finding.Severity), URL: finding.CanonicalURL})
		}
	}
}

func blocker(code BlockerCode, subject, diagnostics string) Blocker {
	if strings.TrimSpace(diagnostics) == "" {
		diagnostics = string(code)
	}
	return Blocker{Code: code, Subject: subject, Diagnostics: diagnostics}
}

func opaque(value string, limit int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > limit {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func checkKey(check codereview.CheckIdentity) string {
	return check.Provider + "\x00" + check.Key + "\x00" + check.Owner
}

func principalKey(principal codereview.PrincipalIdentity) string {
	return principal.Realm + "\x00" + principal.StableID
}

func inputDigest(input Input) string {
	normalized := input
	normalized.Required = append([]codereview.CheckIdentity(nil), input.Required...)
	normalized.Checks = append([]codereview.CheckConclusion(nil), input.Checks...)
	normalized.Review.Authors = append([]codereview.ActorIdentity(nil), input.Review.Authors...)
	normalized.Review.Decisions = append([]codereview.ReviewDecision(nil), input.Review.Decisions...)
	normalized.Review.Findings = append([]codereview.ReviewFinding(nil), input.Review.Findings...)
	normalized.Review.UnresolvedConversations = append([]string(nil), input.Review.UnresolvedConversations...)
	normalized.ProviderGate = append([]ProviderPolicyObservation(nil), input.ProviderGate...)
	sort.Slice(normalized.Required, func(i, j int) bool { return checkKey(normalized.Required[i]) < checkKey(normalized.Required[j]) })
	sort.Slice(normalized.Checks, func(i, j int) bool {
		return checkKey(normalized.Checks[i].Identity) < checkKey(normalized.Checks[j].Identity)
	})
	sort.Slice(normalized.Review.Authors, func(i, j int) bool {
		return normalized.Review.Authors[i].Provider+"\x00"+normalized.Review.Authors[i].StableID < normalized.Review.Authors[j].Provider+"\x00"+normalized.Review.Authors[j].StableID
	})
	sort.Slice(normalized.Review.Decisions, func(i, j int) bool { return normalized.Review.Decisions[i].ID < normalized.Review.Decisions[j].ID })
	sort.Slice(normalized.Review.Findings, func(i, j int) bool { return normalized.Review.Findings[i].ID < normalized.Review.Findings[j].ID })
	sort.Strings(normalized.Review.UnresolvedConversations)
	sort.Slice(normalized.ProviderGate, func(i, j int) bool { return normalized.ProviderGate[i].Code < normalized.ProviderGate[j].Code })
	raw, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
