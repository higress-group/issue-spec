package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/model"
)

const (
	externalReviewCompletionStart = "<!-- issue-spec:external-review-completion version=1 -->"
	externalReviewCompletionEnd   = "<!-- /issue-spec:external-review-completion -->"
)

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
