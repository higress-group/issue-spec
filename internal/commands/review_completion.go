package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

const (
	externalReviewCompletionStart = "<!-- issue-spec:external-review-completion version=1 -->"
	externalReviewCompletionEnd   = "<!-- /issue-spec:external-review-completion -->"
	acceptedReviewReceiptStart    = "<!-- issue-spec:accepted-review-receipt version=1 -->"
	acceptedReviewReceiptEnd      = "<!-- /issue-spec:accepted-review-receipt -->"
)

// acceptedReviewReceipt is the compact immutable authority projected from a
// validated role-owned receipt. Detailed findings remain in their stable
// provider-native carriers and are not duplicated into this block.
type acceptedReviewReceipt struct {
	ReceiptID            string                   `json:"receipt_id"`
	ReceiptDigest        string                   `json:"receipt_digest"`
	AssignmentID         string                   `json:"assignment_id"`
	AssignmentDigest     string                   `json:"assignment_digest"`
	AssignmentGeneration uint64                   `json:"assignment_generation"`
	SubjectRevision      string                   `json:"subject_revision"`
	Verdict              assignment.ReviewVerdict `json:"verdict"`
	FindingIDs           []string                 `json:"finding_ids,omitempty"`
	Tests                []acceptedReviewTest     `json:"tests,omitempty"`
	Provenance           assignment.Provenance    `json:"provenance"`
}

type acceptedReviewTest struct {
	ID               string                   `json:"id"`
	Command          string                   `json:"command"`
	AssignedSelector *assignment.TestSelector `json:"assigned_selector,omitempty"`
	ResolvedRevision string                   `json:"resolved_revision,omitempty"`
	Outcome          assignment.TestOutcome   `json:"outcome"`
	Assurance        assignment.Assurance     `json:"assurance"`
}

func validateReviewReceiptBinding(receipt assignment.Receipt, sealed assignment.Assignment,
	binding *processworkspace.AssignmentBinding) error {
	if err := receipt.ValidateForAcceptance(); err != nil {
		return err
	}
	if receipt.Role != assignment.RoleReview || receipt.Review == nil {
		return errors.New("--result-file must contain a review receipt")
	}
	if binding == nil || binding.SchemaVersion != assignment.AssignmentSchemaVersion ||
		binding.Role != assignment.RoleReview || binding.BaseRevision != "" || binding.SubjectRevision == "" {
		return errors.New("PROCESS does not contain an authoritative review assignment binding")
	}
	if receipt.AssignmentID != binding.AssignmentID || receipt.AssignmentDigest != binding.Digest ||
		receipt.AssignmentGeneration != binding.Generation {
		return errors.New("review receipt does not match the authoritative assignment id, digest, and generation")
	}
	digest, err := assignment.AssignmentDigest(sealed)
	if err != nil {
		return fmt.Errorf("validate sealed review assignment: %w", err)
	}
	if sealed.Role != assignment.RoleReview || sealed.Review == nil || sealed.ID != binding.AssignmentID ||
		digest != binding.Digest || sealed.SubjectRevision != binding.SubjectRevision || sealed.ProcessID == "" {
		return errors.New("sealed review assignment does not exactly match the authoritative PROCESS binding")
	}
	if receipt.SubjectRevision != binding.SubjectRevision {
		return errors.New("review receipt subject revision does not match the authoritative exact snapshot")
	}
	if err := assignment.ValidateReviewReceiptCoverage(*sealed.Review, receipt); err != nil {
		return err
	}
	sealedSpecs := map[string]bool{}
	for _, scenario := range sealed.Scenarios {
		sealedSpecs[scenario.SpecID] = true
	}
	for _, finding := range receipt.Review.Findings {
		if !sealedSpecs[finding.SpecID] {
			return fmt.Errorf("review finding %s spec_id %s is not present in the sealed assignment scenarios", finding.ID, finding.SpecID)
		}
	}
	writer := strings.TrimSpace(receipt.Provenance.Writer)
	if writer == "" || !strings.EqualFold(writer, strings.TrimSpace(receipt.Provenance.Subject)) ||
		strings.EqualFold(writer, "Coordinator") {
		return errors.New("review receipt must be owned by one non-Coordinator reviewer identity")
	}
	if reviewerMatchesExactAuthors(writer, sealed.Review.Authors) {
		return errors.New("declared review receipt writer and subject must not match any normalized exact-diff author from the sealed assignment")
	}
	return nil
}

func reviewerMatchesExactAuthors(reviewer string, authors []string) bool {
	reviewer = normalizeReviewIdentity(reviewer)
	for _, author := range authors {
		if reviewer == normalizeReviewIdentity(author) {
			return true
		}
		if open, close := strings.LastIndex(author, "<"), strings.LastIndex(author, ">"); open >= 0 && close > open {
			if reviewer == normalizeReviewIdentity(author[:open]) || reviewer == normalizeReviewIdentity(author[open+1:close]) {
				return true
			}
		}
	}
	return false
}

func normalizeReviewIdentity(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func acceptedReviewReceiptFrom(receipt assignment.Receipt) acceptedReviewReceipt {
	result := acceptedReviewReceipt{ReceiptID: receipt.ID, ReceiptDigest: receipt.ReceiptDigest,
		AssignmentID: receipt.AssignmentID, AssignmentDigest: receipt.AssignmentDigest,
		AssignmentGeneration: receipt.AssignmentGeneration, SubjectRevision: receipt.SubjectRevision,
		Verdict: receipt.Review.Verdict, Provenance: receipt.Provenance}
	for _, finding := range receipt.Review.Findings {
		result.FindingIDs = append(result.FindingIDs, finding.ID)
	}
	for _, test := range receipt.Tests {
		projected := acceptedReviewTest{ID: test.ID, Command: test.Command,
			ResolvedRevision: test.ResolvedRevision, Outcome: test.Outcome, Assurance: test.Assurance}
		if test.AssignedSelector != nil {
			selector := cloneFinalTestSelector(*test.AssignedSelector)
			projected.AssignedSelector = &selector
		}
		result.Tests = append(result.Tests, projected)
	}
	sort.Strings(result.FindingIDs)
	sort.Slice(result.Tests, func(i, j int) bool { return result.Tests[i].ID < result.Tests[j].ID })
	return result
}

func stampAcceptedReviewReceipt(body string, receipt acceptedReviewReceipt) (string, bool, error) {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return "", false, err
	}
	block := acceptedReviewReceiptStart + "\n" + string(raw) + "\n" + acceptedReviewReceiptEnd
	startCount, endCount := strings.Count(body, acceptedReviewReceiptStart), strings.Count(body, acceptedReviewReceiptEnd)
	if startCount != endCount || startCount > 1 || strings.Count(body, "issue-spec:accepted-review-receipt") != startCount+endCount {
		return "", false, errors.New("existing accepted review receipt block is malformed")
	}
	if startCount == 0 {
		updated := strings.TrimRight(body, "\n") + "\n\n" + block + "\n"
		return updated, true, nil
	}
	start, end := strings.Index(body, acceptedReviewReceiptStart), strings.Index(body, acceptedReviewReceiptEnd)
	if end < start+len(acceptedReviewReceiptStart) {
		return "", false, errors.New("existing accepted review receipt block is malformed")
	}
	end += len(acceptedReviewReceiptEnd)
	if body[start:end] != block {
		return "", false, errors.New("accepted review receipt authority is immutable")
	}
	return body, false, nil
}

func parseAcceptedReviewReceipt(body string) (acceptedReviewReceipt, bool, error) {
	if !strings.Contains(body, "issue-spec:accepted-review-receipt") {
		return acceptedReviewReceipt{}, false, nil
	}
	if strings.Count(body, acceptedReviewReceiptStart) != 1 || strings.Count(body, acceptedReviewReceiptEnd) != 1 ||
		strings.Count(body, "issue-spec:accepted-review-receipt") != 2 {
		return acceptedReviewReceipt{}, true, errors.New("accepted review receipt must contain exactly one version-1 marker pair")
	}
	start, end := strings.Index(body, acceptedReviewReceiptStart), strings.Index(body, acceptedReviewReceiptEnd)
	if end <= start {
		return acceptedReviewReceipt{}, true, errors.New("accepted review receipt marker order is invalid")
	}
	rawBlock := body[start+len(acceptedReviewReceiptStart) : end]
	if len(rawBlock) < 3 || rawBlock[0] != '\n' || rawBlock[len(rawBlock)-1] != '\n' {
		return acceptedReviewReceipt{}, true, errors.New("accepted review receipt payload framing is invalid")
	}
	raw := []byte(rawBlock[1 : len(rawBlock)-1])
	var result acceptedReviewReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return acceptedReviewReceipt{}, true, fmt.Errorf("decode accepted review receipt: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return acceptedReviewReceipt{}, true, errors.New("accepted review receipt has trailing JSON")
	}
	canonical, _ := json.Marshal(result)
	if !bytes.Equal(raw, canonical) {
		return acceptedReviewReceipt{}, true, errors.New("accepted review receipt payload is not canonical JSON")
	}
	return result, true, nil
}

// ReviewCompletionPolicy projects repository and native review policy onto the
// CLI-owned REVIEW completion artifact. Provider review records remain facts:
// they do not stand in for an exact-revision synchronization completion.
type ReviewCompletionPolicy struct {
	Required  bool
	Freshness time.Duration
}

// externalReviewCompletion is deliberately limited to exact code-change
// identity plus the time at which the authoritative native ledger was read.
// Versioning lives on the enclosing marker so the JSON schema stays closed.
type externalReviewCompletion struct {
	ProviderKey        string    `json:"provider_key"`
	ExternalRepository string    `json:"external_repository"`
	ChangeID           string    `json:"change_id"`
	ReferenceVersion   int64     `json:"reference_version"`
	SubjectRevision    string    `json:"subject_revision"`
	SynchronizedAt     time.Time `json:"synchronized_at"`
}

// ValidateExternalReviewCompletion validates against the current time without
// installing a mutable package/global clock. Tests use the package-local At
// helper to inject a controlled validation instant.
func ValidateExternalReviewCompletion(review model.TypedComment, target coreevidence.NativeTarget,
	policy ReviewCompletionPolicy) error {
	return validateExternalReviewCompletionAt(review, target, policy, time.Now().UTC())
}

func validateExternalReviewCompletionAt(review model.TypedComment, target coreevidence.NativeTarget,
	policy ReviewCompletionPolicy, validationNow time.Time) error {
	if policy.Freshness < 0 {
		return errors.New("external REVIEW completion freshness policy is invalid")
	}
	completion, found, err := parseExternalReviewCompletion(review.Body)
	if err != nil {
		return err
	}
	if !found {
		if policy.Required {
			return errors.New("external REVIEW completion is required")
		}
		return nil
	}

	parsed := model.ParseTypedComment(review.Body)
	if parsed.Marker.Type != "REVIEW" || parsed.Marker.ID == "" || parsed.Type != "REVIEW" || parsed.Status != "done" ||
		!parsed.HasHead || len(parsed.Errors) != 0 {
		return errors.New("external REVIEW completion requires one canonical done REVIEW typed comment")
	}
	headerRevision, err := exactReviewSubjectRevision(review.Body)
	if err != nil {
		return err
	}
	if err := validateReviewCompletionTarget(target); err != nil {
		return err
	}
	if headerRevision != target.SubjectRevision || completion.SubjectRevision != target.SubjectRevision {
		return errors.New("external REVIEW completion revision does not match the REVIEW header and active target")
	}
	if completion.ProviderKey != target.Reference.ProviderKey ||
		completion.ExternalRepository != target.Reference.ExternalRepository ||
		completion.ChangeID != target.Reference.ChangeID || completion.ReferenceVersion != target.ReferenceVersion {
		return errors.New("external REVIEW completion identity or reference version does not match the active target")
	}
	if err := validateReviewCompletionIdentity(completion); err != nil {
		return err
	}
	if validationNow.IsZero() {
		return errors.New("external REVIEW completion validation time is invalid")
	}
	validationNow = validationNow.UTC()
	if completion.SynchronizedAt.After(validationNow.Add(time.Minute)) {
		return errors.New("external REVIEW completion synchronization time is in the future")
	}
	if policy.Freshness > 0 && validationNow.Sub(completion.SynchronizedAt) > policy.Freshness {
		return errors.New("external REVIEW completion is older than repository policy permits")
	}
	return nil
}

func parseExternalReviewCompletion(body string) (externalReviewCompletion, bool, error) {
	present := strings.Contains(body, "issue-spec:external-review-completion")
	if !present {
		return externalReviewCompletion{}, false, nil
	}
	if strings.Count(body, externalReviewCompletionStart) != 1 || strings.Count(body, externalReviewCompletionEnd) != 1 ||
		strings.Count(body, "issue-spec:external-review-completion") != 2 {
		return externalReviewCompletion{}, true, errors.New("external REVIEW completion must contain exactly one version-1 marker pair")
	}
	start := strings.Index(body, externalReviewCompletionStart)
	end := strings.Index(body, externalReviewCompletionEnd)
	if start < 0 || end <= start {
		return externalReviewCompletion{}, true, errors.New("external REVIEW completion marker order is invalid")
	}
	rawBlock := body[start+len(externalReviewCompletionStart) : end]
	if len(rawBlock) < 3 || rawBlock[0] != '\n' || rawBlock[len(rawBlock)-1] != '\n' {
		return externalReviewCompletion{}, true, errors.New("external REVIEW completion payload framing is invalid")
	}
	raw := []byte(rawBlock[1 : len(rawBlock)-1])
	var completion externalReviewCompletion
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&completion); err != nil {
		return externalReviewCompletion{}, true, fmt.Errorf("decode external REVIEW completion: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return externalReviewCompletion{}, true, errors.New("external REVIEW completion has trailing JSON")
	}
	canonical, err := json.Marshal(completion)
	if err != nil || !bytes.Equal(raw, canonical) {
		return externalReviewCompletion{}, true, errors.New("external REVIEW completion payload is not canonical JSON")
	}
	return completion, true, nil
}

func exactReviewSubjectRevision(body string) (string, error) {
	var revision string
	count := 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "Subject Revision:") {
			continue
		}
		count++
		revision = strings.TrimSpace(strings.TrimPrefix(trimmed, "Subject Revision:"))
	}
	if count != 1 || revision == "" || strings.ContainsAny(revision, " \t\r\n") {
		return "", errors.New("external REVIEW completion requires exactly one canonical Subject Revision header")
	}
	return revision, nil
}

func validateReviewCompletionTarget(target coreevidence.NativeTarget) error {
	revision := strings.TrimSpace(target.SubjectRevision)
	if err := target.Reference.Validate(); err != nil || target.ReferenceVersion <= 0 || revision == "" ||
		target.SubjectRevision != revision || strings.ContainsAny(revision, " \t\r\n") {
		return errors.New("external REVIEW completion active target is invalid")
	}
	return nil
}

func validateReviewCompletionIdentity(completion externalReviewCompletion) error {
	reference := codereview.Reference{ProviderKey: completion.ProviderKey,
		ExternalRepository: completion.ExternalRepository, ChangeID: completion.ChangeID}
	if err := reference.Validate(); err != nil || completion.ReferenceVersion <= 0 || completion.SubjectRevision == "" ||
		completion.ProviderKey != strings.TrimSpace(completion.ProviderKey) ||
		completion.ExternalRepository != strings.TrimSpace(completion.ExternalRepository) ||
		completion.ChangeID != strings.TrimSpace(completion.ChangeID) ||
		completion.SubjectRevision != strings.TrimSpace(completion.SubjectRevision) ||
		strings.ContainsAny(completion.ExternalRepository, "\t\r\n") ||
		strings.ContainsAny(completion.ChangeID, "\t\r\n") ||
		strings.ContainsAny(completion.SubjectRevision, " \t\r\n") || completion.SynchronizedAt.IsZero() {
		return errors.New("external REVIEW completion identity is invalid")
	}
	_, offset := completion.SynchronizedAt.Zone()
	if completion.SynchronizedAt.Location() != time.UTC || offset != 0 {
		return errors.New("external REVIEW completion synchronization time must be UTC")
	}
	return nil
}
