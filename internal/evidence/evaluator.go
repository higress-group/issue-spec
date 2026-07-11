// Package evidence evaluates provider-neutral immutable evidence in core. A
// bridge supplies facts, never an "approved" boolean; review, verify, and
// archive commands consume this package to make their own fail-closed decision.
package evidence

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/codereview"
)

type Gate string

const (
	GateReview  Gate = "review"
	GateVerify  Gate = "verify"
	GateMerge   Gate = "merge"
	GateArchive Gate = "archive"
)

type Policy struct {
	RequiredKinds            []codereview.EvidenceKind
	RequiredChecks           []string
	Freshness                map[codereview.EvidenceKind]time.Duration
	BlockingReviewSeverities []string
}

type Target struct {
	Gate            Gate
	Reference       codereview.Reference
	SubjectRevision string
	Now             time.Time
}

type Failure struct {
	Code       string                  `json:"code"`
	Message    string                  `json:"message"`
	EvidenceID string                  `json:"evidence_id,omitempty"`
	Kind       codereview.EvidenceKind `json:"kind,omitempty"`
}

type Result struct {
	Passed          bool      `json:"passed"`
	Gate            Gate      `json:"gate"`
	SubjectRevision string    `json:"subject_revision"`
	EvidenceIDs     []string  `json:"evidence_ids"`
	Failures        []Failure `json:"failures"`
}

func Evaluate(snapshot codereview.Snapshot, policy Policy, target Target) Result {
	result := Result{Gate: target.Gate, SubjectRevision: strings.TrimSpace(target.SubjectRevision),
		EvidenceIDs: []string{}, Failures: []Failure{}}
	if target.Now.IsZero() {
		target.Now = time.Now().UTC()
	} else {
		target.Now = target.Now.UTC()
	}
	if err := target.Reference.Validate(); err != nil || result.SubjectRevision == "" || !validGate(target.Gate) {
		result.Failures = append(result.Failures, Failure{Code: "invalid_target", Message: "gate target is incomplete"})
		return finalize(result)
	}
	if snapshot.ProtocolVersion != codereview.ProtocolVersion {
		result.Failures = append(result.Failures, Failure{Code: "protocol_mismatch", Message: "evidence protocol version is unsupported"})
	}
	if snapshot.Reference != target.Reference {
		result.Failures = append(result.Failures, Failure{Code: "reference_mismatch", Message: "evidence belongs to a different provider, repository, or change"})
	}
	if strings.TrimSpace(snapshot.SubjectRevision) != result.SubjectRevision {
		result.Failures = append(result.Failures, Failure{Code: "snapshot_revision_mismatch", Message: "evidence snapshot is tied to a different revision"})
	}
	if snapshot.CapturedAt.IsZero() || snapshot.CapturedAt.After(target.Now.Add(time.Minute)) {
		result.Failures = append(result.Failures, Failure{Code: "invalid_capture_time", Message: "evidence snapshot has an invalid capture time"})
	}

	result.Failures = append(result.Failures, validateSupersession(snapshot.Records)...)
	active := activeRecords(snapshot.Records)
	usable := make([]codereview.EvidenceRecord, 0, len(active))
	for _, record := range active {
		failure := validateRecord(record, policy, target)
		if failure != nil {
			result.Failures = append(result.Failures, *failure)
			continue
		}
		usable = append(usable, record)
	}

	requiredKinds := append([]codereview.EvidenceKind(nil), policy.RequiredKinds...)
	switch target.Gate {
	case GateReview:
		requiredKinds = append(requiredKinds, codereview.EvidenceReview)
	case GateVerify:
		requiredKinds = append(requiredKinds, codereview.EvidenceReview, codereview.EvidenceCheck)
	case GateMerge:
		requiredKinds = append(requiredKinds, codereview.EvidenceMerge)
	case GateArchive:
		requiredKinds = append(requiredKinds, codereview.EvidenceArchive)
	}
	for _, kind := range dedupeKinds(requiredKinds) {
		if !hasKind(usable, kind) {
			result.Failures = append(result.Failures, Failure{Code: "missing_evidence", Kind: kind,
				Message: fmt.Sprintf("required %s evidence is missing", kind)})
		}
	}

	blocking := blockingSeverities(policy.BlockingReviewSeverities)
	for _, record := range usable {
		if record.Kind == codereview.EvidenceReview && blocking[strings.ToUpper(record.Severity)] &&
			!isResolvedReview(record.State) {
			result.Failures = append(result.Failures, Failure{Code: "blocking_review", Kind: record.Kind,
				EvidenceID: record.ID, Message: fmt.Sprintf("open %s review finding %s blocks the gate", strings.ToUpper(record.Severity), displayID(record))})
		}
	}

	latestChecks := latestByName(usable, codereview.EvidenceCheck)
	requiredChecks := normalizedNames(policy.RequiredChecks)
	if len(requiredChecks) == 0 {
		for name := range latestChecks {
			requiredChecks = append(requiredChecks, name)
		}
		sort.Strings(requiredChecks)
	}
	for _, required := range requiredChecks {
		record, ok := latestChecks[required]
		if !ok {
			result.Failures = append(result.Failures, Failure{Code: "missing_required_check", Kind: codereview.EvidenceCheck,
				Message: fmt.Sprintf("required check %s is missing", required)})
			continue
		}
		switch strings.ToLower(strings.TrimSpace(record.State)) {
		case "passed", "success", "successful":
		default:
			code := "required_check_pending"
			if strings.EqualFold(record.State, "failed") || strings.EqualFold(record.State, "failure") || strings.EqualFold(record.State, "cancelled") {
				code = "required_check_failed"
			}
			result.Failures = append(result.Failures, Failure{Code: code, Kind: record.Kind, EvidenceID: record.ID,
				Message: fmt.Sprintf("required check %s is %s", required, record.State)})
		}
	}

	if target.Gate == GateMerge {
		requireMerged(&result, usable, codereview.EvidenceMerge)
	}
	if target.Gate == GateArchive {
		requireMerged(&result, usable, codereview.EvidenceArchive)
	}
	if len(result.Failures) == 0 {
		for _, record := range usable {
			result.EvidenceIDs = append(result.EvidenceIDs, record.ID)
		}
	}
	return finalize(result)
}

func validateRecord(record codereview.EvidenceRecord, policy Policy, target Target) *Failure {
	failure := Failure{EvidenceID: record.ID, Kind: record.Kind}
	if strings.TrimSpace(record.ID) == "" || !validKind(record.Kind) || strings.TrimSpace(record.State) == "" ||
		strings.TrimSpace(record.SubjectRevision) == "" || record.ObservedAt.IsZero() || strings.TrimSpace(record.PayloadDigest) == "" {
		failure.Code, failure.Message = "malformed_evidence", "evidence record is missing immutable identity, state, revision, time, or digest"
		return &failure
	}
	if record.SubjectRevision != strings.TrimSpace(target.SubjectRevision) {
		failure.Code, failure.Message = "record_revision_mismatch", "evidence record is tied to a different revision"
		return &failure
	}
	if !record.Trusted || strings.TrimSpace(record.WriterIdentity) == "" {
		failure.Code, failure.Message = "untrusted_evidence", "evidence record was not produced by a trusted designated writer"
		return &failure
	}
	if record.ObservedAt.After(target.Now.Add(time.Minute)) {
		failure.Code, failure.Message = "future_evidence", "evidence observation time is in the future"
		return &failure
	}
	if record.ValidUntil != nil && !target.Now.Before(*record.ValidUntil) {
		failure.Code, failure.Message = "expired_evidence", "evidence validity window has expired"
		return &failure
	}
	if maximum := policy.Freshness[record.Kind]; maximum > 0 && target.Now.Sub(record.ObservedAt) > maximum {
		failure.Code, failure.Message = "stale_evidence", "evidence observation is older than repository policy permits"
		return &failure
	}
	return nil
}

func activeRecords(records []codereview.EvidenceRecord) []codereview.EvidenceRecord {
	superseded := make(map[string]struct{}, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if id := strings.TrimSpace(record.SupersedesID); id != "" {
			superseded[id] = struct{}{}
		}
	}
	result := make([]codereview.EvidenceRecord, 0, len(records))
	for _, record := range records {
		id := strings.TrimSpace(record.ID)
		if _, replaced := superseded[id]; replaced {
			continue
		}
		if _, duplicate := seen[id]; duplicate && id != "" {
			// Preserve the duplicate as malformed so evaluation fails closed.
			record.ID = ""
		}
		seen[id] = struct{}{}
		result = append(result, record)
	}
	return result
}

func validateSupersession(records []codereview.EvidenceRecord) []Failure {
	byID := make(map[string]codereview.EvidenceRecord, len(records))
	successors := make(map[string]int, len(records))
	failures := make([]Failure, 0)
	for _, record := range records {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			continue
		}
		if _, duplicate := byID[id]; duplicate {
			failures = append(failures, Failure{Code: "duplicate_evidence_id", Kind: record.Kind,
				EvidenceID: id, Message: "evidence snapshot contains a duplicate immutable identifier"})
			continue
		}
		byID[id] = record
	}
	for _, record := range records {
		predecessorID := strings.TrimSpace(record.SupersedesID)
		if predecessorID == "" {
			continue
		}
		failure := Failure{Code: "invalid_supersession", Kind: record.Kind, EvidenceID: record.ID,
			Message: "evidence supersession chain is incomplete or inconsistent"}
		predecessor, exists := byID[predecessorID]
		if !exists || predecessorID == strings.TrimSpace(record.ID) {
			failures = append(failures, failure)
			continue
		}
		successors[predecessorID]++
		if successors[predecessorID] > 1 || predecessor.Kind != record.Kind ||
			strings.TrimSpace(predecessor.ExternalID) != strings.TrimSpace(record.ExternalID) ||
			strings.TrimSpace(predecessor.Name) != strings.TrimSpace(record.Name) ||
			predecessor.SubjectRevision != record.SubjectRevision || !record.ObservedAt.After(predecessor.ObservedAt) {
			failures = append(failures, failure)
		}
	}
	for id := range byID {
		seen := map[string]bool{}
		for current := id; current != ""; current = strings.TrimSpace(byID[current].SupersedesID) {
			if seen[current] {
				failures = append(failures, Failure{Code: "invalid_supersession", EvidenceID: id,
					Message: "evidence supersession chain contains a cycle"})
				break
			}
			seen[current] = true
			if _, exists := byID[current]; !exists {
				break
			}
		}
	}
	return failures
}

func requireMerged(result *Result, records []codereview.EvidenceRecord, kind codereview.EvidenceKind) {
	latest := latestOfKind(records, kind)
	if latest == nil {
		return
	}
	if !strings.EqualFold(latest.State, "merged") || strings.TrimSpace(latest.MergeRevision) == "" {
		result.Failures = append(result.Failures, Failure{Code: "not_merged", Kind: kind, EvidenceID: latest.ID,
			Message: fmt.Sprintf("%s evidence does not report a merged revision", kind)})
	}
}

func latestOfKind(records []codereview.EvidenceRecord, kind codereview.EvidenceKind) *codereview.EvidenceRecord {
	var latest *codereview.EvidenceRecord
	for index := range records {
		if records[index].Kind != kind || (latest != nil && !records[index].ObservedAt.After(latest.ObservedAt)) {
			continue
		}
		latest = &records[index]
	}
	return latest
}

func latestByName(records []codereview.EvidenceRecord, kind codereview.EvidenceKind) map[string]codereview.EvidenceRecord {
	result := make(map[string]codereview.EvidenceRecord)
	for _, record := range records {
		if record.Kind != kind {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(record.Name))
		if name == "" {
			continue
		}
		if current, exists := result[name]; !exists || record.ObservedAt.After(current.ObservedAt) {
			result[name] = record
		}
	}
	return result
}

func finalize(result Result) Result {
	sort.Strings(result.EvidenceIDs)
	result.EvidenceIDs = dedupeStrings(result.EvidenceIDs)
	sort.SliceStable(result.Failures, func(i, j int) bool {
		left, right := result.Failures[i], result.Failures[j]
		return left.Code+"\x00"+string(left.Kind)+"\x00"+left.EvidenceID < right.Code+"\x00"+string(right.Kind)+"\x00"+right.EvidenceID
	})
	result.Passed = len(result.Failures) == 0
	return result
}

func validGate(gate Gate) bool {
	return gate == GateReview || gate == GateVerify || gate == GateMerge || gate == GateArchive
}

func validKind(kind codereview.EvidenceKind) bool {
	switch kind {
	case codereview.EvidenceChange, codereview.EvidenceReview, codereview.EvidenceCheck,
		codereview.EvidenceMerge, codereview.EvidenceArchive:
		return true
	default:
		return false
	}
}

func hasKind(records []codereview.EvidenceRecord, kind codereview.EvidenceKind) bool {
	for _, record := range records {
		if record.Kind == kind {
			return true
		}
	}
	return false
}

func dedupeKinds(values []codereview.EvidenceKind) []codereview.EvidenceKind {
	seen := make(map[codereview.EvidenceKind]struct{}, len(values))
	result := make([]codereview.EvidenceKind, 0, len(values))
	for _, value := range values {
		if !validKind(value) {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func blockingSeverities(values []string) map[string]bool {
	if len(values) == 0 {
		values = []string{"P0", "P1"}
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		if normalized := strings.ToUpper(strings.TrimSpace(value)); normalized != "" {
			result[normalized] = true
		}
	}
	return result
}

func normalizedNames(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return dedupeStrings(result)
}

func dedupeStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func isResolvedReview(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "resolved", "dismissed", "closed", "superseded":
		return true
	default:
		return false
	}
}

func displayID(record codereview.EvidenceRecord) string {
	if value := strings.TrimSpace(record.ExternalID); value != "" {
		return value
	}
	return record.ID
}
