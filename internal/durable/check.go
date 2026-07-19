package durable

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/reconcile/filecas"
)

const CheckResultVersion = 1

const (
	BlockUnauthorizedChange = "unauthorized_durable_change"
	BlockSubjectMissing     = "subject_target_missing"
	BlockSubjectMalformed   = "subject_target_malformed"
	BlockProjectionMismatch = "subject_projection_mismatch"
	BlockRevisionMismatch   = "revision_mismatch"
)

type CheckInput struct {
	CompileInput
	SubjectRevision     string
	SubjectFiles        map[string]BaselineFile
	ChangedDurablePaths []string
	RevisionError       string
}

type CheckResult struct {
	Version          int       `json:"version"`
	ResultDigest     string    `json:"result_digest"`
	OK               bool      `json:"ok"`
	Repository       string    `json:"repository"`
	Proposal         int       `json:"proposal"`
	BaselineRevision string    `json:"baseline_revision"`
	SubjectRevision  string    `json:"subject_revision"`
	OperationCount   int       `json:"operation_count"`
	Blockers         []Blocker `json:"blockers,omitempty"`
	Findings         []Finding `json:"findings,omitempty"`
}

type CompactCheck struct {
	Version          int              `json:"version"`
	OK               bool             `json:"ok"`
	ResultDigest     string           `json:"result_digest"`
	ResultPath       string           `json:"result_path,omitempty"`
	BaselineRevision string           `json:"baseline_revision"`
	SubjectRevision  string           `json:"subject_revision"`
	OperationCount   int              `json:"operation_count"`
	BlockerCount     int              `json:"blocker_count"`
	Blockers         []CompactBlocker `json:"blockers,omitempty"`
}

// Check independently recompiles the authorized projection and compares it
// with an explicit subject tree. It consumes no preview plan or sidecar.
func Check(input CheckInput) (CheckResult, error) {
	if !planRevision.MatchString(strings.TrimSpace(input.SubjectRevision)) {
		return CheckResult{}, errors.New("durable check requires an exact lowercase subject revision")
	}
	input.SubjectRevision = strings.TrimSpace(input.SubjectRevision)
	plan, err := CompilePlan(input.CompileInput)
	if err != nil {
		return CheckResult{}, err
	}
	result := CheckResult{Version: CheckResultVersion, Repository: plan.Repository, Proposal: plan.Proposal,
		BaselineRevision: plan.BaselineRevision, SubjectRevision: input.SubjectRevision,
		OperationCount: len(plan.Operations), Findings: append([]Finding(nil), plan.Findings...)}
	if strings.TrimSpace(input.RevisionError) != "" {
		result.Findings = append(result.Findings, Finding{Code: BlockRevisionMismatch, Message: strings.TrimSpace(input.RevisionError)})
	}
	authorized := map[string]bool{}
	capabilities := map[string]string{}
	operationIDs := map[string][]string{}
	for _, operation := range plan.Operations {
		authorized[operation.Path] = true
		operationIDs[operation.Path] = append(operationIDs[operation.Path], operation.ID)
		parts := strings.Split(operation.Path, "/")
		if len(parts) >= 3 {
			capabilities[operation.Path] = parts[len(parts)-2]
		}
	}
	for _, changedPath := range normalizedChangedPaths(input.ChangedDurablePaths) {
		if !authorized[changedPath] {
			result.Findings = append(result.Findings, Finding{Code: BlockUnauthorizedChange, Path: changedPath,
				Message: fmt.Sprintf("durable path %q changed without an authorized operation", changedPath)})
		}
	}
	if len(plan.Blockers) == 0 {
		for _, mutation := range plan.Files {
			subject := input.SubjectFiles[mutation.Path]
			if !subject.Exists {
				result.Findings = appendSubjectFindings(result.Findings, BlockSubjectMissing, mutation.Path,
					operationIDs[mutation.Path], fmt.Sprintf("authorized durable target %q is missing at the subject revision", mutation.Path))
				continue
			}
			document, parseErr := parseDurableDocument(subject.Body, capabilities[mutation.Path])
			if parseErr == nil && len(duplicateRequirementTitles(document.Blocks)) != 0 {
				parseErr = errors.New("duplicate Requirement identities")
			}
			if parseErr != nil {
				result.Findings = appendSubjectFindings(result.Findings, BlockSubjectMalformed, mutation.Path,
					operationIDs[mutation.Path], fmt.Sprintf("subject durable target %q is malformed: %v", mutation.Path, parseErr))
				continue
			}
			observedDigest := filecas.FileDigest([]byte(subject.Body))
			if observedDigest != mutation.Postimage.Digest {
				result.Findings = appendSubjectFindings(result.Findings, BlockProjectionMismatch, mutation.Path,
					operationIDs[mutation.Path], fmt.Sprintf("subject durable target %q does not equal the canonical authorized postimage", mutation.Path))
			}
		}
	}
	sort.Slice(result.Findings, func(i, j int) bool { return findingKey(result.Findings[i]) < findingKey(result.Findings[j]) })
	result.Blockers = summarizeFindings(result.Findings)
	result.OK = len(result.Blockers) == 0
	digest, err := DigestCheckResult(result)
	if err != nil {
		return CheckResult{}, err
	}
	result.ResultDigest = digest
	if err := ValidateCheckResult(result); err != nil {
		return CheckResult{}, err
	}
	return result, nil
}

func appendSubjectFindings(findings []Finding, code, path string, operationIDs []string, message string) []Finding {
	if len(operationIDs) == 0 {
		return append(findings, Finding{Code: code, Path: path, Message: message})
	}
	for _, operationID := range operationIDs {
		findings = append(findings, Finding{Code: code, OperationID: operationID, Path: path, Message: message})
	}
	return findings
}

func normalizedChangedPaths(paths []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range paths {
		value = filepath.ToSlash(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func DigestCheckResult(result CheckResult) (string, error) {
	result.ResultDigest = ""
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func CanonicalCheckResultJSON(result CheckResult) ([]byte, error) {
	if err := ValidateCheckResult(result); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func ReadCheckResult(reader io.Reader) (CheckResult, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var result CheckResult
	if err := decoder.Decode(&result); err != nil {
		return CheckResult{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return CheckResult{}, errors.New("durable check result contains multiple JSON values")
		}
		return CheckResult{}, err
	}
	if err := ValidateCheckResult(result); err != nil {
		return CheckResult{}, err
	}
	return result, nil
}

func ValidateCheckResult(result CheckResult) error {
	if result.Version != CheckResultVersion || !planRepository.MatchString(result.Repository) || result.Proposal <= 0 ||
		!planRevision.MatchString(result.BaselineRevision) || !planRevision.MatchString(result.SubjectRevision) || result.OperationCount < 0 {
		return errors.New("durable check result identity is incomplete")
	}
	if result.OK != (len(result.Blockers) == 0) {
		return errors.New("durable check result ok state differs from blockers")
	}
	if !sort.SliceIsSorted(result.Blockers, func(i, j int) bool { return result.Blockers[i].Code < result.Blockers[j].Code }) {
		return errors.New("durable check blockers are not in deterministic order")
	}
	if !sort.SliceIsSorted(result.Findings, func(i, j int) bool { return findingKey(result.Findings[i]) < findingKey(result.Findings[j]) }) {
		return errors.New("durable check findings are not in deterministic order")
	}
	for _, blocker := range result.Blockers {
		if blocker.Code == "" || len(blocker.Messages) == 0 || len(blocker.Messages) > maxBlockerMessages ||
			len(blocker.AffectedIDs) > maxAffectedIDs || blocker.TruncatedCount < 0 {
			return fmt.Errorf("durable check blocker %q is not bounded", blocker.Code)
		}
	}
	digest, err := DigestCheckResult(result)
	if err != nil {
		return err
	}
	if result.ResultDigest != digest {
		return fmt.Errorf("durable check result digest mismatch: declared=%s computed=%s", result.ResultDigest, digest)
	}
	return nil
}

func CompactCheckResult(result CheckResult, resultPath string) CompactCheck {
	compact := CompactCheck{Version: CheckResultVersion, OK: result.OK, ResultDigest: result.ResultDigest,
		ResultPath: resultPath, BaselineRevision: result.BaselineRevision, SubjectRevision: result.SubjectRevision,
		OperationCount: result.OperationCount, BlockerCount: len(result.Blockers)}
	for _, blocker := range result.Blockers {
		compact.Blockers = append(compact.Blockers, CompactBlocker{Code: blocker.Code,
			AffectedIDs: append([]string(nil), blocker.AffectedIDs...), TruncatedCount: blocker.TruncatedCount,
			DetailAction: fmt.Sprintf("issue-spec durable-spec detail --result %s --code %s", shellQuote(resultPath), shellQuote(blocker.Code))})
	}
	return compact
}
