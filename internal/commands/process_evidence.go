package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/finalization"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

var processTestEvidencePattern = regexp.MustCompile(`(?i)\btest(s|ing|ed)?\b`)

const MaxCanonicalEvidenceIndexEntries = 4096

var canonicalEvidenceDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type CanonicalEvidenceKind string

const (
	CanonicalEvidenceReview       CanonicalEvidenceKind = "review"
	CanonicalEvidenceVerification CanonicalEvidenceKind = "verification"
	CanonicalEvidenceTest         CanonicalEvidenceKind = "test"
	CanonicalEvidenceCheck        CanonicalEvidenceKind = "check"
)

type CanonicalEvidenceAuthority string

const (
	CanonicalEvidenceRoleOwned     CanonicalEvidenceAuthority = "role-owned"
	CanonicalEvidenceProviderOwned CanonicalEvidenceAuthority = "provider-owned"
)

// CanonicalEvidenceRecord is one already-validated canonical evidence fact
// bound to an active PROCESS/SPEC pair. Role-owned facts carry their immutable
// receipt and assignment identities; provider-owned facts carry a stable
// evidence ID and source. Trusted is explicit so prose-derived candidates
// cannot accidentally enter the final index.
type CanonicalEvidenceRecord struct {
	ProcessID            string
	SpecID               string
	Kind                 CanonicalEvidenceKind
	Authority            CanonicalEvidenceAuthority
	EvidenceID           string
	ReceiptID            string
	ReceiptDigest        string
	AssignmentProcessID  string
	AssignmentID         string
	AssignmentDigest     string
	AssignmentGeneration uint64
	SubjectRevision      string
	TestID               string
	TestAuthorityRole    assignment.Role
	AssignedSelector     *assignment.TestSelector
	ResolvedRevision     string
	ExecutedCommand      string
	CheckSelector        *assignment.CheckSelector
	URL                  string
	Source               string
	Trusted              bool
}

type CanonicalEvidenceKey struct {
	ProcessID string
	SpecID    string
	Kind      CanonicalEvidenceKind
}

// CanonicalEvidenceIndex is a pure bounded read model. Its map and slices are
// private, and Records always returns a copy, so indexing shared REVIEW/VERIFY
// evidence cannot become a backdoor mutation of a PROCESS or caller input.
type CanonicalEvidenceIndex struct {
	entries map[CanonicalEvidenceKey][]CanonicalEvidenceRecord
	count   int
}

// BuildCanonicalEvidenceIndex validates exact-current identity and builds at
// most MaxCanonicalEvidenceIndexEntries unique evidence records.
func BuildCanonicalEvidenceIndex(records []CanonicalEvidenceRecord, expectedRevision string) (CanonicalEvidenceIndex, error) {
	return buildCanonicalEvidenceIndex(records, expectedRevision, MaxCanonicalEvidenceIndexEntries)
}

func buildCanonicalEvidenceIndex(records []CanonicalEvidenceRecord, expectedRevision string, limit int) (CanonicalEvidenceIndex, error) {
	return buildCanonicalEvidenceIndexForAssignments(records, expectedRevision, nil, limit)
}

func buildCanonicalEvidenceIndexForAssignments(records []CanonicalEvidenceRecord, expectedRevision string,
	active map[string]gates.ActiveAssignmentEvidence, limit int) (CanonicalEvidenceIndex, error) {
	expectedRevision = strings.TrimSpace(expectedRevision)
	if expectedRevision == "" {
		return CanonicalEvidenceIndex{}, errors.New("canonical evidence index requires an exact subject revision")
	}
	if limit <= 0 || limit > MaxCanonicalEvidenceIndexEntries {
		return CanonicalEvidenceIndex{}, fmt.Errorf("canonical evidence index limit must be between 1 and %d", MaxCanonicalEvidenceIndexEntries)
	}
	index := CanonicalEvidenceIndex{entries: map[CanonicalEvidenceKey][]CanonicalEvidenceRecord{}}
	receiptIdentityByID := map[string]string{}
	assignmentIdentityByID := map[string]string{}
	receiptByActiveAssignment := map[string]string{}
	providerIdentityByID := map[string]string{}
	seenEntry := map[string]bool{}
	for _, original := range records {
		record := cloneCanonicalEvidenceRecord(original)
		eligible, err := selectActiveAssignmentEvidence(record, active, expectedRevision)
		if err != nil {
			return CanonicalEvidenceIndex{}, err
		}
		if !eligible {
			continue
		}
		if err := validateCanonicalEvidenceRecord(record, expectedRevision); err != nil {
			return CanonicalEvidenceIndex{}, err
		}
		identity := canonicalEvidenceIdentity(record)
		if record.Authority == CanonicalEvidenceRoleOwned {
			receiptIdentity := strings.Join([]string{record.ReceiptDigest, record.AssignmentID,
				record.AssignmentDigest, fmt.Sprint(record.AssignmentGeneration),
				strings.ToLower(strings.TrimSpace(record.SubjectRevision))}, "\x00")
			if previous, exists := receiptIdentityByID[record.ReceiptID]; exists && previous != receiptIdentity {
				return CanonicalEvidenceIndex{}, fmt.Errorf("canonical evidence %q has conflicting receipt identity or digest", record.EvidenceID)
			}
			receiptIdentityByID[record.ReceiptID] = receiptIdentity
			assignmentIdentity := strings.Join([]string{record.AssignmentDigest, fmt.Sprint(record.AssignmentGeneration),
				strings.ToLower(strings.TrimSpace(record.SubjectRevision))}, "\x00")
			if previous, exists := assignmentIdentityByID[record.AssignmentID]; exists && previous != assignmentIdentity {
				return CanonicalEvidenceIndex{}, fmt.Errorf("canonical evidence %q has conflicting assignment identity or digest", record.EvidenceID)
			}
			assignmentIdentityByID[record.AssignmentID] = assignmentIdentity
			if record.AssignmentProcessID != "" {
				assignmentSlot := record.AssignmentProcessID + "\x00" + record.AssignmentID + "\x00" +
					record.AssignmentDigest + "\x00" + fmt.Sprint(record.AssignmentGeneration)
				receiptIdentity := record.ReceiptID + "\x00" + record.ReceiptDigest
				if previous, exists := receiptByActiveAssignment[assignmentSlot]; exists && previous != receiptIdentity {
					return CanonicalEvidenceIndex{}, fmt.Errorf("canonical evidence %q duplicates the active assignment generation with a conflicting receipt", record.EvidenceID)
				}
				receiptByActiveAssignment[assignmentSlot] = receiptIdentity
			}
		} else {
			providerKey := record.Source + "\x00" + record.EvidenceID
			if previous, exists := providerIdentityByID[providerKey]; exists && previous != identity {
				return CanonicalEvidenceIndex{}, fmt.Errorf("canonical evidence %q has conflicting provider identity", record.EvidenceID)
			}
			providerIdentityByID[providerKey] = identity
		}
		key := CanonicalEvidenceKey{ProcessID: record.ProcessID, SpecID: record.SpecID, Kind: record.Kind}
		entryIdentity := record.ProcessID + "\x00" + record.SpecID + "\x00" + identity
		if seenEntry[entryIdentity] {
			continue
		}
		if index.count == limit {
			return CanonicalEvidenceIndex{}, fmt.Errorf("canonical evidence index exceeds bounded limit %d", limit)
		}
		seenEntry[entryIdentity] = true
		index.entries[key] = append(index.entries[key], record)
		index.count++
	}
	for key := range index.entries {
		sort.Slice(index.entries[key], func(i, j int) bool {
			return index.entries[key][i].EvidenceID < index.entries[key][j].EvidenceID
		})
	}
	return index, nil
}

func selectActiveAssignmentEvidence(record CanonicalEvidenceRecord, active map[string]gates.ActiveAssignmentEvidence,
	expectedRevision string) (bool, error) {
	if record.AssignmentProcessID == "" || len(active) == 0 {
		return true, nil
	}
	authority, managed := active[record.AssignmentProcessID]
	if !managed {
		return true, nil
	}
	if record.AssignmentGeneration < authority.Generation {
		return false, nil
	}
	if record.AssignmentGeneration > authority.Generation {
		return false, fmt.Errorf("canonical evidence %q is from future assignment generation %d; active generation is %d",
			record.EvidenceID, record.AssignmentGeneration, authority.Generation)
	}
	if record.AssignmentID != authority.AssignmentID || record.AssignmentDigest != authority.AssignmentDigest ||
		record.SubjectRevision != authority.SubjectRevision || record.SubjectRevision != expectedRevision {
		return false, fmt.Errorf("canonical evidence %q conflicts with the active assignment identity, digest, or subject", record.EvidenceID)
	}
	wantRole := assignment.RoleVerification
	if record.Kind == CanonicalEvidenceReview {
		wantRole = assignment.RoleReview
	} else if record.Kind == CanonicalEvidenceTest {
		wantRole = record.TestAuthorityRole
	}
	if authority.Role != wantRole {
		return false, fmt.Errorf("canonical evidence %q uses active %s assignment for %s evidence", record.EvidenceID, authority.Role, record.Kind)
	}
	if record.Kind == CanonicalEvidenceTest {
		selector, ok, err := canonicalEvidenceTestSelector(record, expectedRevision)
		if err != nil || !ok {
			return false, fmt.Errorf("canonical evidence %q has invalid stable selector or expanded command: %v", record.EvidenceID, err)
		}
		assigned := false
		for _, required := range authority.RequiredTests {
			if required.ID != selector.ID {
				continue
			}
			assigned = true
			if !assignment.TestSelectorIdentityEqual(required, selector) {
				return false, fmt.Errorf("canonical evidence %q changes the active assignment selector identity", record.EvidenceID)
			}
		}
		if !assigned {
			return false, fmt.Errorf("canonical evidence %q is not assigned by the active %s assignment", record.EvidenceID, authority.Role)
		}
	}
	if record.Kind == CanonicalEvidenceCheck && record.AssignmentProcessID != "" {
		if record.CheckSelector == nil || strings.TrimSpace(record.CheckSelector.Provider) == "" ||
			strings.TrimSpace(record.CheckSelector.Name) == "" {
			return false, fmt.Errorf("canonical evidence %q lacks exact accepted check selector identity", record.EvidenceID)
		}
		assigned := false
		for _, required := range authority.RequiredChecks {
			if required == *record.CheckSelector {
				assigned = true
				break
			}
		}
		if !assigned {
			return false, fmt.Errorf("canonical evidence %q is not assigned by the active verification assignment", record.EvidenceID)
		}
	}
	return true, nil
}

func canonicalEvidenceTestSelector(record CanonicalEvidenceRecord, expectedRevision string) (assignment.TestSelector, bool, error) {
	if record.Kind != CanonicalEvidenceTest {
		return assignment.TestSelector{}, false, nil
	}
	if record.AssignedSelector == nil {
		selector := assignment.TestSelector{ID: record.TestID, Command: record.ExecutedCommand}
		if record.ResolvedRevision != "" {
			return assignment.TestSelector{}, false, errors.New("literal selector carries a resolved revision")
		}
		if err := assignment.ValidateTestSelectorRevisionContract(record.TestAuthorityRole, expectedRevision, selector); err != nil {
			return assignment.TestSelector{}, false, err
		}
		return selector, true, nil
	}
	selector := cloneFinalTestSelector(*record.AssignedSelector)
	if selector.ID != record.TestID {
		return assignment.TestSelector{}, false, errors.New("assigned selector id differs from test id")
	}
	if err := assignment.ValidateTestSelectorRevisionContract(record.TestAuthorityRole, expectedRevision, selector); err != nil {
		return assignment.TestSelector{}, false, err
	}
	resolved, err := assignment.ResolveTestSelector(selector, record.ResolvedRevision)
	if err != nil {
		return assignment.TestSelector{}, false, err
	}
	if record.ResolvedRevision != expectedRevision || record.ExecutedCommand != resolved.Command {
		return assignment.TestSelector{}, false, errors.New("resolved revision or executed command differs from deterministic expansion")
	}
	return selector, true, nil
}

func validateCanonicalEvidenceRecord(record CanonicalEvidenceRecord, expectedRevision string) error {
	if !record.Trusted {
		return fmt.Errorf("canonical evidence %q is not validated and trusted", record.EvidenceID)
	}
	if !externalProcessIDPattern.MatchString(record.ProcessID) || !externalSpecIDPattern.MatchString(record.SpecID) {
		return fmt.Errorf("canonical evidence %q has invalid PROCESS/SPEC identity", record.EvidenceID)
	}
	switch record.Kind {
	case CanonicalEvidenceReview, CanonicalEvidenceVerification, CanonicalEvidenceTest, CanonicalEvidenceCheck:
	default:
		return fmt.Errorf("canonical evidence %q has unsupported kind %q", record.EvidenceID, record.Kind)
	}
	if strings.TrimSpace(record.EvidenceID) == "" || record.EvidenceID != strings.TrimSpace(record.EvidenceID) ||
		strings.TrimSpace(record.SubjectRevision) == "" || record.SubjectRevision != expectedRevision ||
		strings.TrimSpace(record.Source) == "" || record.Source != strings.TrimSpace(record.Source) {
		return fmt.Errorf("canonical evidence %q is missing exact-current canonical identity", record.EvidenceID)
	}
	if record.Kind == CanonicalEvidenceTest {
		if record.TestAuthorityRole != assignment.RoleReview && record.TestAuthorityRole != assignment.RoleVerification {
			return fmt.Errorf("canonical evidence %q has invalid test authority role %q", record.EvidenceID, record.TestAuthorityRole)
		}
	} else if record.TestAuthorityRole != "" {
		return fmt.Errorf("canonical evidence %q carries test authority on non-test evidence", record.EvidenceID)
	}
	switch record.Authority {
	case CanonicalEvidenceRoleOwned:
		if strings.TrimSpace(record.ReceiptID) == "" || record.ReceiptID != strings.TrimSpace(record.ReceiptID) ||
			strings.TrimSpace(record.AssignmentID) == "" || record.AssignmentID != strings.TrimSpace(record.AssignmentID) ||
			!canonicalEvidenceDigestPattern.MatchString(record.ReceiptDigest) ||
			!canonicalEvidenceDigestPattern.MatchString(record.AssignmentDigest) || record.AssignmentGeneration == 0 {
			return fmt.Errorf("canonical evidence %q has invalid role-owned receipt identity", record.EvidenceID)
		}
		if record.AssignmentProcessID != "" && !externalProcessIDPattern.MatchString(record.AssignmentProcessID) {
			return fmt.Errorf("canonical evidence %q has invalid assignment PROCESS identity", record.EvidenceID)
		}
		validSource := (record.Kind == CanonicalEvidenceReview && strings.HasPrefix(record.Source, "accepted-review-receipt:")) ||
			(record.Kind == CanonicalEvidenceTest && record.TestAuthorityRole == assignment.RoleReview &&
				strings.HasPrefix(record.Source, "accepted-review-receipt:")) ||
			(record.Kind == CanonicalEvidenceTest && record.TestAuthorityRole == assignment.RoleVerification &&
				strings.HasPrefix(record.Source, "accepted-verification-receipt:")) ||
			(record.Kind != CanonicalEvidenceReview && record.Kind != CanonicalEvidenceTest &&
				strings.HasPrefix(record.Source, "accepted-verification-receipt:"))
		if !validSource {
			return fmt.Errorf("canonical evidence %q is not sourced from an accepted role-owned receipt", record.EvidenceID)
		}
	case CanonicalEvidenceProviderOwned:
		if record.ReceiptID != "" || record.ReceiptDigest != "" || record.AssignmentProcessID != "" ||
			record.AssignmentID != "" || record.AssignmentDigest != "" || record.AssignmentGeneration != 0 {
			return fmt.Errorf("canonical evidence %q mixes provider and role-owned identity", record.EvidenceID)
		}
		if !canonicalProviderEvidenceSource(record.Source) {
			return fmt.Errorf("canonical evidence %q is not sourced from a provider-owned record", record.EvidenceID)
		}
	default:
		return fmt.Errorf("canonical evidence %q has no canonical authority", record.EvidenceID)
	}
	if record.Kind == CanonicalEvidenceTest {
		if _, ok, err := canonicalEvidenceTestSelector(record, expectedRevision); err != nil || !ok {
			return fmt.Errorf("canonical evidence %q has invalid test selector identity: %v", record.EvidenceID, err)
		}
	} else if record.TestID != "" || record.AssignedSelector != nil || record.ResolvedRevision != "" || record.ExecutedCommand != "" {
		return fmt.Errorf("canonical evidence %q carries test identity on non-test evidence", record.EvidenceID)
	}
	if record.Kind != CanonicalEvidenceCheck && record.CheckSelector != nil {
		return fmt.Errorf("canonical evidence %q carries check identity on non-check evidence", record.EvidenceID)
	}
	return nil
}

func canonicalProviderEvidenceSource(source string) bool {
	for _, prefix := range []string{"github-check-run:", "github-pull-request-head:", "github-pr-review-comment:",
		"native-evidence:", "native-authoritative-ledger:", "external-review-completion:"} {
		if strings.HasPrefix(source, prefix) {
			return true
		}
	}
	return false
}

func canonicalEvidenceIdentity(record CanonicalEvidenceRecord) string {
	selectorJSON, _ := json.Marshal(record.AssignedSelector)
	checkJSON, _ := json.Marshal(record.CheckSelector)
	return strings.Join([]string{string(record.Kind), string(record.Authority), record.EvidenceID,
		record.ReceiptID, record.ReceiptDigest, record.AssignmentID, record.AssignmentDigest,
		fmt.Sprint(record.AssignmentGeneration), record.AssignmentProcessID,
		strings.ToLower(strings.TrimSpace(record.SubjectRevision)), record.TestID, string(record.TestAuthorityRole), string(selectorJSON),
		record.ResolvedRevision, record.ExecutedCommand, string(checkJSON), record.URL, record.Source}, "\x00")
}

func (index CanonicalEvidenceIndex) Len() int { return index.count }

func (index CanonicalEvidenceIndex) Records(processID, specID string, kind CanonicalEvidenceKind) []CanonicalEvidenceRecord {
	key := CanonicalEvidenceKey{ProcessID: processID, SpecID: specID, Kind: kind}
	values := index.entries[key]
	result := make([]CanonicalEvidenceRecord, len(values))
	for item := range values {
		result[item] = cloneCanonicalEvidenceRecord(values[item])
	}
	return result
}

func cloneCanonicalEvidenceRecord(record CanonicalEvidenceRecord) CanonicalEvidenceRecord {
	clone := record
	if record.AssignedSelector != nil {
		selector := cloneFinalTestSelector(*record.AssignedSelector)
		clone.AssignedSelector = &selector
	}
	if record.CheckSelector != nil {
		selector := *record.CheckSelector
		clone.CheckSelector = &selector
	}
	return clone
}

// activeProcessIDs is the command layer's only PROCESS activity projection.
// Selection deliberately retains legacy Status: superseded carriers when no
// explicit superseded-by authority exists, and fails closed by retaining all
// unique PROCESS carriers when the replacement graph is invalid.
func activeProcessIDs(artifacts []model.Artifact) map[string]bool {
	selection := finalization.Select(artifacts)
	active := make(map[string]bool, len(selection.ActiveProcessIDs))
	for _, id := range selection.ActiveProcessIDs {
		active[id] = true
	}
	return active
}

func activeProcessArtifacts(artifacts []model.Artifact) []model.Artifact {
	selection := finalization.Select(artifacts)
	return append([]model.Artifact(nil), selection.Active...)
}

func currentFinalizationArtifacts(artifacts []model.Artifact) []model.Artifact {
	active := activeProcessIDs(artifacts)
	current := make([]model.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Comment.Type != "PROCESS" || active[artifact.Comment.ID] {
			current = append(current, artifact)
		}
	}
	return current
}

func buildProcessEvidenceInputs(artifacts []model.Artifact, prURL string, reviewComments []github.PullRequestReviewComment,
	review reviewSyncReport, external *externalEvidenceConsumption) []gates.ProcessEvidenceInput {
	return buildProcessEvidenceInputsWithExternalReview(artifacts, prURL, reviewComments, review, external, nil, time.Now().UTC())
}

// buildProcessEvidenceInputsWithExternalReview is the narrow integration seam
// for self-hosted gates. The ordinary wrapper preserves existing callers while
// a caller that has explicitly reloaded the authoritative ledger can supply
// that gate result and obtain completion/legacy REVIEW carrier validation.
func buildProcessEvidenceInputsWithExternalReview(artifacts []model.Artifact, prURL string,
	reviewComments []github.PullRequestReviewComment, review reviewSyncReport, external *externalEvidenceConsumption,
	externalReview *externalGateResult, validationNow time.Time) []gates.ProcessEvidenceInput {
	if external == nil && externalReview != nil {
		external = &externalReview.Consumption
	}
	activeSpecs := map[string]string{}
	inactiveSpecURLs := map[string]bool{}
	taskURLs := map[string]bool{}
	selection := finalization.Select(artifacts)
	processes := append([]model.Artifact(nil), selection.Active...)
	var reviews, verifications []model.Artifact
	for _, artifact := range artifacts {
		switch artifact.Comment.Type {
		case "SPEC":
			if artifact.Comment.Status != "superseded" {
				activeSpecs[artifact.Comment.ID] = artifact.URL
			}
		case "TASK":
			if artifact.Comment.Status != "superseded" {
				taskURLs[model.NormalizeURL(artifact.URL)] = true
			}
		case "REVIEW":
			if artifact.Comment.Status == "done" {
				reviews = append(reviews, artifact)
			}
		case "VERIFY":
			if artifact.Comment.Status == "done" {
				verifications = append(verifications, artifact)
			}
		}
	}
	for _, artifact := range artifacts {
		if artifact.Comment.Type == "SPEC" && artifact.Comment.Status == "superseded" {
			inactiveSpecURLs[model.NormalizeURL(artifact.URL)] = true
		}
	}
	externalValid := external != nil && validateExternalEvidenceConsumption(*external, processes, activeSpecs) == nil
	requiredRevision := strings.TrimSpace(review.SubjectRevision)
	if externalValid {
		requiredRevision = strings.TrimSpace(external.SubjectRevision)
	}
	if externalReview != nil {
		requiredRevision = strings.TrimSpace(externalReview.Target.SubjectRevision)
	}
	processByID := make(map[string]model.Artifact, len(processes))
	for _, process := range processes {
		processByID[process.Comment.ID] = process
	}
	authorAgentsBySpec := map[string]map[string]bool{}
	authorAgentsByProcessSpec := map[string]map[string]map[string]bool{}
	rationaleIdentityCounts := map[string]int{}
	for _, artifact := range artifacts {
		if !model.IsLikelyCodeChangeRationale(artifact.Comment.Body) {
			continue
		}
		marker, found, err := model.FindCodeChangeRationaleMarker(artifact.Comment.Body)
		if err == nil && found && marker.RationaleID != "" {
			rationaleIdentityCounts[marker.RationaleID]++
		}
	}
	for _, comment := range reviewComments {
		marker, ok, err := model.FindRationaleMarker(comment.Body)
		if err != nil || !ok {
			continue
		}
		agent := strings.ToLower(strings.TrimSpace(marker.Agent))
		spec := strings.TrimSpace(marker.Spec)
		if agent == "" || spec == "" {
			continue
		}
		// Credit an author identity only from a genuine change-bearing carrier,
		// mirroring the gate's change-bearing validation: real path/line, active
		// SPEC, matching SPEC URL, and an active (non-superseded) change-bearing
		// PROCESS that actually covers the SPEC. Otherwise a stale, forged, or
		// wrong-class marker could pollute the author set and falsely block an
		// independent reviewer of that SPEC.
		if marker.Path == "" || marker.Line <= 0 || marker.Path != comment.Path || marker.Line != comment.Line {
			continue
		}
		want, active := activeSpecs[spec]
		if !active {
			continue
		}
		if specURL := rationaleSpecURL(comment.Body); specURL == "" || model.NormalizeURL(specURL) != model.NormalizeURL(want) {
			continue
		}
		process, resolved := processByID[strings.TrimSpace(marker.Process)]
		if !resolved {
			continue
		}
		if model.ParseProcessExecutionClass(process.Comment.ID, process.URL, process.Comment.Body).Class != model.ProcessExecutionChangeBearing {
			continue
		}
		if !artifactReferencesSpec(process, spec, want) {
			continue
		}
		if authorAgentsBySpec[spec] == nil {
			authorAgentsBySpec[spec] = map[string]bool{}
		}
		authorAgentsBySpec[spec][agent] = true
		addProcessSpecAgent(authorAgentsByProcessSpec, process.Comment.ID, spec, agent)
	}
	inputs := make([]gates.ProcessEvidenceInput, 0, len(processes))
	for _, process := range processes {
		input := gates.ProcessEvidenceInput{Process: process, RequiredPRURL: prURL, ActiveSpecs: activeSpecs, TaskURLs: taskURLs,
			RequiredRevision: requiredRevision, AuthorAgentsBySpec: authorAgentsBySpec,
			ActiveAssignment: activeAssignmentEvidence(process)}
		for _, comment := range reviewComments {
			marker, ok, err := model.FindRationaleMarker(comment.Body)
			if err != nil || !ok || marker.Process != process.Comment.ID {
				continue
			}
			input.Rationales = append(input.Rationales, gates.RationaleEvidence{ProcessID: marker.Process, SpecID: marker.Spec,
				SpecURL: rationaleSpecURL(comment.Body), MarkerPath: marker.Path, MarkerLine: marker.Line,
				CommentPath: comment.Path, CommentLine: comment.Line, AuthorAgent: marker.Agent})
		}
		if externalValid {
			input.External = externalProcessEvidenceFor(process.Comment.ID, activeSpecs, *external)
		}
		if externalReview != nil && strings.TrimSpace(prURL) == "" {
			input.External = append(input.External, completionRationaleEvidenceForProcess(process, reviews, processes,
				activeSpecs, inactiveSpecURLs, *externalReview, validationNow)...)
		}
		for _, artifact := range artifacts {
			if artifact.Issue != process.Issue || !model.IsLikelyCodeChangeRationale(artifact.Comment.Body) {
				continue
			}
			marker, found, err := model.FindCodeChangeRationaleMarker(artifact.Comment.Body)
			if err != nil || !found || marker.Process != process.Comment.ID {
				continue
			}
			if !model.CodeChangeRationaleGateEligible(marker) ||
				(marker.RationaleID != "" && rationaleIdentityCounts[marker.RationaleID] != 1) {
				continue
			}
			evidence := gates.CodeChangeRationaleEvidence{ProcessID: marker.Process, SpecID: marker.Spec,
				SpecURL: marker.SpecURL, ProviderKey: marker.ProviderKey, ExternalRepository: marker.ExternalRepository,
				ChangeID: marker.ChangeID, ReferenceVersion: marker.ReferenceVersion, SubjectRevision: marker.SubjectRevision,
				AuthorAgent: marker.Agent, AuthorSessionID: marker.AgentSessionID, URL: artifact.URL}
			input.CodeChangeRationales = append(input.CodeChangeRationales, evidence)
			if validCodeChangeRationaleAuthor(evidence, process, activeSpecs, input.External) {
				agent := strings.ToLower(strings.TrimSpace(evidence.AuthorAgent))
				if authorAgentsBySpec[evidence.SpecID] == nil {
					authorAgentsBySpec[evidence.SpecID] = map[string]bool{}
				}
				authorAgentsBySpec[evidence.SpecID][agent] = true
				addProcessSpecAgent(authorAgentsByProcessSpec, process.Comment.ID, evidence.SpecID, agent)
			}
		}
		for _, artifact := range reviews {
			if externalReview != nil && strings.TrimSpace(prURL) == "" {
				coverage, covered := explicitExternalReviewCoverage(artifact, processes, activeSpecs, inactiveSpecURLs)
				if !covered || coverage.ReviewProcessID != process.Comment.ID {
					continue
				}
				for _, specID := range coverage.SpecIDs {
					revision, trusted, source := externalReviewCarrierRevision(artifact, coverage, specID, processes,
						activeSpecs, *externalReview, validationNow)
					input.Reviews = append(input.Reviews, gates.ReviewEvidence{ProcessID: process.Comment.ID, SpecID: specID,
						URL: artifact.URL, Done: true, ReviewerAgent: artifact.Comment.Agent, SubjectRevision: revision,
						Trusted: trusted, Source: source})
				}
				continue
			}
			if !artifactReferencesProcess(artifact, process) {
				continue
			}
			for specID := range activeSpecs {
				if artifactReferencesSpec(artifact, specID, activeSpecs[specID]) {
					revision, trusted, source := reviewArtifactRevision(artifact, prURL, review, input.External, process.Comment.ID, specID)
					input.Reviews = append(input.Reviews, gates.ReviewEvidence{ProcessID: process.Comment.ID, SpecID: specID, URL: artifact.URL,
						Done: true, ReviewerAgent: artifact.Comment.Agent, SubjectRevision: revision, Trusted: trusted, Source: source})
				}
			}
		}
		for _, finding := range review.ResolvedFindings {
			if finding.Process == process.Comment.ID {
				input.Reviews = append(input.Reviews, gates.ReviewEvidence{ProcessID: finding.Process, SpecID: finding.Spec, URL: finding.URL,
					FindingResolved: true, ReviewerAgent: finding.Agent, SubjectRevision: finding.SubjectRevision, Trusted: finding.SubjectRevision != "", Source: finding.RevisionSource})
			}
		}
		for _, artifact := range verifications {
			if !artifactReferencesProcess(artifact, process) {
				continue
			}
			for specID := range activeSpecs {
				if artifactReferencesSpec(artifact, specID, activeSpecs[specID]) {
					input.Verifications = append(input.Verifications, gates.VerificationEvidence{ProcessID: process.Comment.ID,
						SpecID: specID, URL: artifact.URL, Done: true, TestEvidence: processTestEvidencePattern.MatchString(artifact.Comment.Body), Source: "typed-verify"})
				}
			}
		}
		for _, check := range review.PassedChecks {
			if !strings.Contains(process.Comment.Body, check.Name) {
				continue
			}
			for specID := range activeSpecs {
				testEvidence := processTestEvidencePattern.MatchString(check.Name)
				for _, verify := range input.Verifications {
					if verify.SpecID == specID && verify.TestEvidence {
						testEvidence = true
					}
				}
				if gates.ReferencesArtifactID(process.Comment.Body, specID) {
					input.Checks = append(input.Checks, gates.CheckEvidence{ProcessID: process.Comment.ID, SpecID: specID, Name: check.Name,
						Required: true, Passed: true, TestEvidence: testEvidence, SubjectRevision: check.SubjectRevision, Trusted: check.Trusted, Source: check.Source})
				}
			}
		}
		inputs = append(inputs, input)
	}
	filterSharedVerificationIdentity(inputs, verifications, authorAgentsByProcessSpec, requiredRevision)
	return inputs
}

func activeAssignmentEvidence(process model.Artifact) *gates.ActiveAssignmentEvidence {
	workspace := model.ParseProcessWorkspace(process.Comment.ID, process.URL, process.Comment.Body)
	if workspace.Blocking() || workspace.Workspace == nil || workspace.Workspace.Assignment == nil {
		return nil
	}
	binding := workspace.Workspace.Assignment
	result := &gates.ActiveAssignmentEvidence{ProcessID: process.Comment.ID, AssignmentID: binding.AssignmentID,
		AssignmentDigest: binding.Digest, Generation: binding.Generation, Role: binding.Role,
		SubjectRevision: binding.SubjectRevision}
	if binding.SelectorAuthority != nil {
		result.RequiredTests = cloneCanonicalTestSelectors(binding.SelectorAuthority.Tests)
		result.RequiredChecks = append([]assignment.CheckSelector(nil), binding.SelectorAuthority.Checks...)
	}
	return result
}

func cloneCanonicalTestSelectors(values []assignment.TestSelector) []assignment.TestSelector {
	result := make([]assignment.TestSelector, len(values))
	for index, value := range values {
		result[index] = cloneFinalTestSelector(value)
	}
	return result
}

func addProcessSpecAgent(values map[string]map[string]map[string]bool, processID, specID, agent string) {
	processID, specID, agent = strings.TrimSpace(processID), strings.TrimSpace(specID), strings.ToLower(strings.TrimSpace(agent))
	if processID == "" || specID == "" || agent == "" {
		return
	}
	if values[processID] == nil {
		values[processID] = map[string]map[string]bool{}
	}
	if values[processID][specID] == nil {
		values[processID][specID] = map[string]bool{}
	}
	values[processID][specID][agent] = true
}

// filterSharedVerificationIdentity is a collector-side trust boundary, not a
// second gate evaluator. Shared-carrier policy remains in gates.Evaluate; this
// projection only retains a PROCESS/SPEC fact when the exact VERIFY carrier has
// either stronger accepted-receipt authority or the explicitly preserved
// legacy manual self-reported identity, and that identity is independent from
// the exact PROCESS/SPEC code author set.
//
// Manual self-reported identity is not provider-authenticated. It is accepted
// only from a canonical, done, exact-current VERIFY whose explicit Agent is a
// real non-Coordinator role and whose collected pair already has exact PROCESS
// and SPEC links plus test evidence. Single change-bearing carrier workflows
// retain their existing legacy/manual compatibility behavior.
func filterSharedVerificationIdentity(inputs []gates.ProcessEvidenceInput, carriers []model.Artifact,
	authors map[string]map[string]map[string]bool, requiredRevision string) {
	changeBearing := 0
	for _, input := range inputs {
		if model.ParseProcessExecutionClass(input.Process.Comment.ID, input.Process.URL, input.Process.Comment.Body).Class == model.ProcessExecutionChangeBearing {
			changeBearing++
		}
	}
	if changeBearing < 2 {
		return
	}
	type carrierIdentity struct {
		agent              string
		manualSelfReported bool
		carrier            model.Artifact
	}
	verifierByURL := make(map[string]carrierIdentity, len(carriers))
	for _, carrier := range carriers {
		if verifier, manual := validatedVerificationCarrierAgent(carrier, requiredRevision); verifier != "" {
			verifierByURL[model.NormalizeURL(carrier.URL)] = carrierIdentity{
				agent: verifier, manualSelfReported: manual, carrier: carrier,
			}
		}
	}
	for inputIndex := range inputs {
		input := &inputs[inputIndex]
		if model.ParseProcessExecutionClass(input.Process.Comment.ID, input.Process.URL, input.Process.Comment.Body).Class != model.ProcessExecutionChangeBearing {
			continue
		}
		kept := input.Verifications[:0]
		for _, evidence := range input.Verifications {
			identity, found := verifierByURL[model.NormalizeURL(evidence.URL)]
			pairAuthors := authors[input.Process.Comment.ID][evidence.SpecID]
			if !found || len(pairAuthors) == 0 || pairAuthors[identity.agent] {
				continue
			}
			if identity.manualSelfReported {
				specURL := input.ActiveSpecs[evidence.SpecID]
				if evidence.ProcessID != input.Process.Comment.ID || specURL == "" || !evidence.Done || !evidence.TestEvidence ||
					!linksContainURL(identity.carrier.Comment.Links["Related Comments"], input.Process.URL) ||
					!linksContainURL(identity.carrier.Comment.Links["Related Comments"], specURL) {
					continue
				}
				evidence.SubjectRevision = strings.TrimSpace(requiredRevision)
				evidence.Trusted = true
				evidence.Source = "manual-self-reported-verify:exact-current"
			}
			kept = append(kept, evidence)
		}
		input.Verifications = kept
	}
}

// validatedVerificationCarrierAgent returns the normalized verifier and
// whether its authority is the legacy manual self-reported compatibility path.
// A malformed or invalid accepted-receipt marker never falls back to manual.
func validatedVerificationCarrierAgent(carrier model.Artifact, requiredRevision string) (string, bool) {
	if carrier.Comment.Type != "VERIFY" || carrier.Comment.Status != "done" || len(carrier.Comment.Errors) != 0 {
		return "", false
	}
	authority, found, err := parseAcceptedVerificationReceipt(carrier.Comment.Body)
	if err != nil {
		return "", false
	}
	expectedRevision := strings.TrimSpace(requiredRevision)
	if found {
		if expectedRevision == "" {
			expectedRevision = strings.TrimSpace(carrier.Comment.SubjectRevision)
		}
		if expectedRevision == "" {
			return "", false
		}
		if _, _, _, valid := exactAcceptedVerificationCarrier(carrier, expectedRevision); !valid {
			return "", false
		}
		verifier := strings.ToLower(strings.TrimSpace(authority.Provenance.Writer))
		if verifier == "" || !strings.EqualFold(verifier, strings.TrimSpace(carrier.Comment.Agent)) {
			return "", false
		}
		return verifier, false
	}

	// The manual path deliberately carries only legacy self-reported assurance.
	// Exact-current binding and a non-Coordinator visible Agent are therefore
	// mandatory, and callers must still validate pair links and test evidence.
	if expectedRevision == "" || !strings.EqualFold(strings.TrimSpace(carrier.Comment.SubjectRevision), expectedRevision) {
		return "", false
	}
	verifier := strings.ToLower(strings.TrimSpace(carrier.Comment.Agent))
	if verifier == "" || strings.EqualFold(verifier, "Coordinator") {
		return "", false
	}
	return verifier, true
}

type explicitReviewCoverage struct {
	ReviewProcessID      string
	SpecIDs              []string
	ImplementationBySpec map[string][]string
}

// explicitExternalReviewCoverage accepts no textual ID inference. A carrier
// must link one active review PROCESS, at least one active change-bearing
// PROCESS, and the exact active SPEC URLs covered by both sides. Equality of
// those SPEC sets prevents a partially linked multi-SPEC REVIEW from rescuing
// the covered subset while silently omitting the rest.
func explicitExternalReviewCoverage(review model.Artifact, processes []model.Artifact, activeSpecs map[string]string,
	inactiveSpecURLs map[string]bool) (explicitReviewCoverage, bool) {
	related := review.Comment.Links["Related Comments"]
	if len(related) == 0 {
		return explicitReviewCoverage{}, false
	}
	linked := map[string]bool{}
	for _, raw := range related {
		url := model.NormalizeURL(raw)
		if url == "" || inactiveSpecURLs[url] {
			return explicitReviewCoverage{}, false
		}
		linked[url] = true
	}
	var reviewProcesses []model.Artifact
	var implementations []model.Artifact
	for _, process := range processes {
		if !linked[model.NormalizeURL(process.URL)] {
			continue
		}
		switch model.ParseProcessExecutionClass(process.Comment.ID, process.URL, process.Comment.Body).Class {
		case model.ProcessExecutionReview:
			reviewProcesses = append(reviewProcesses, process)
		case model.ProcessExecutionChangeBearing:
			implementations = append(implementations, process)
		}
	}
	if len(reviewProcesses) != 1 || len(implementations) == 0 {
		return explicitReviewCoverage{}, false
	}
	reviewSpecs := processActiveSpecs(reviewProcesses[0], activeSpecs)
	if len(reviewSpecs) == 0 {
		return explicitReviewCoverage{}, false
	}
	implementationBySpec := map[string][]string{}
	for _, process := range implementations {
		specs := processActiveSpecs(process, activeSpecs)
		if len(specs) == 0 {
			return explicitReviewCoverage{}, false
		}
		for _, specID := range specs {
			implementationBySpec[specID] = append(implementationBySpec[specID], process.Comment.ID)
		}
	}
	linkedSpecs := map[string]bool{}
	for specID, specURL := range activeSpecs {
		if linked[model.NormalizeURL(specURL)] {
			linkedSpecs[specID] = true
		}
	}
	if len(linkedSpecs) != len(reviewSpecs) || len(linkedSpecs) != len(implementationBySpec) {
		return explicitReviewCoverage{}, false
	}
	for _, specID := range reviewSpecs {
		if !linkedSpecs[specID] || len(implementationBySpec[specID]) == 0 {
			return explicitReviewCoverage{}, false
		}
	}
	for specID := range implementationBySpec {
		if !linkedSpecs[specID] {
			return explicitReviewCoverage{}, false
		}
		sort.Strings(implementationBySpec[specID])
	}
	return explicitReviewCoverage{ReviewProcessID: reviewProcesses[0].Comment.ID,
		SpecIDs: reviewSpecs, ImplementationBySpec: implementationBySpec}, true
}

func processActiveSpecs(process model.Artifact, activeSpecs map[string]string) []string {
	var result []string
	for specID, specURL := range activeSpecs {
		if artifactReferencesSpec(process, specID, specURL) {
			result = append(result, specID)
		}
	}
	sort.Strings(result)
	return result
}

func completionRationaleEvidenceForProcess(process model.Artifact, reviews, processes []model.Artifact,
	activeSpecs map[string]string, inactiveSpecURLs map[string]bool, gate externalGateResult,
	validationNow time.Time) []gates.ExternalProcessEvidence {
	if model.ParseProcessExecutionClass(process.Comment.ID, process.URL, process.Comment.Body).Class != model.ProcessExecutionChangeBearing {
		return nil
	}
	if !validExternalReviewGateContext(gate) {
		return nil
	}
	seen := map[string]bool{}
	var result []gates.ExternalProcessEvidence
	for _, review := range reviews {
		coverage, ok := explicitExternalReviewCoverage(review, processes, activeSpecs, inactiveSpecURLs)
		if !ok {
			continue
		}
		completion, found, err := parseExternalReviewCompletion(review.Comment.Body)
		if err != nil || !found {
			continue
		}
		policy := gate.ReviewCompletionPolicy
		policy.Required = true
		if validateExternalReviewCompletionAt(review.Comment, gate.Target, policy, validationNow) != nil {
			continue
		}
		for _, specID := range coverage.SpecIDs {
			if !exactStringInSlice(coverage.ImplementationBySpec[specID], process.Comment.ID) {
				continue
			}
			key := process.Comment.ID + "\x00" + specID + "\x00" + review.Comment.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, gates.ExternalProcessEvidence{ProcessID: process.Comment.ID, SpecID: specID,
				ProviderKey: completion.ProviderKey, ExternalRepository: completion.ExternalRepository,
				ChangeID: completion.ChangeID, ReferenceVersion: completion.ReferenceVersion,
				SubjectRevision: completion.SubjectRevision, EvidenceRevision: completion.SubjectRevision,
				EvidenceKind: "review_completion", Consumed: true,
				EvidenceIDs: []string{"external-review-completion:" + review.Comment.ID}, Trusted: true,
				Source: "native-authoritative-ledger:external-review-completion:" + review.Comment.ID})
		}
	}
	return result
}

func externalReviewCarrierRevision(review model.Artifact, coverage explicitReviewCoverage, specID string,
	processes []model.Artifact, activeSpecs map[string]string, gate externalGateResult,
	validationNow time.Time) (string, bool, string) {
	if !validExternalReviewGateContext(gate) {
		return strings.TrimSpace(review.Comment.SubjectRevision), false, "typed-review"
	}
	completion, found, err := parseExternalReviewCompletion(review.Comment.Body)
	if err != nil {
		return strings.TrimSpace(review.Comment.SubjectRevision), false, "typed-review"
	}
	if found {
		policy := gate.ReviewCompletionPolicy
		policy.Required = true
		if validateExternalReviewCompletionAt(review.Comment, gate.Target, policy, validationNow) != nil {
			return strings.TrimSpace(review.Comment.SubjectRevision), false, "typed-review"
		}
		return completion.SubjectRevision, true, "external-review-completion"
	}
	return legacyExternalReviewCarrierRevision(review, coverage, specID, processes, activeSpecs, gate, validationNow)
}

func legacyExternalReviewCarrierRevision(review model.Artifact, coverage explicitReviewCoverage, specID string,
	processes []model.Artifact, activeSpecs map[string]string, gate externalGateResult,
	validationNow time.Time) (string, bool, string) {
	recorded := strings.TrimSpace(review.Comment.SubjectRevision)
	if recorded == "" || recorded != gate.Target.SubjectRevision {
		return recorded, false, "typed-review"
	}
	if header, err := exactReviewSubjectRevision(review.Comment.Body); err != nil || header != gate.Target.SubjectRevision {
		return recorded, false, "typed-review"
	}
	consumption, ok := parseConsumedEvidenceBlock(review.Comment.Body)
	if !ok || validateReviewCompletionTarget(gate.Target) != nil ||
		validateExactSnapshotIdentity(gate.Snapshot, gate.Target) != nil ||
		validateExternalEvidenceConsumption(consumption, processes, activeSpecs) != nil ||
		!sameExternalReviewIdentity(consumption, gate.Target) {
		return recorded, false, "typed-review"
	}
	selected := map[string]bool{}
	for _, id := range consumption.EvidenceIDs {
		selected[id] = true
	}
	evaluated := map[string]bool{}
	for _, id := range gate.Evaluation.EvidenceIDs {
		evaluated[id] = true
	}
	superseded := map[string]bool{}
	records := map[string]codereview.EvidenceRecord{}
	for _, record := range gate.Snapshot.Records {
		id := strings.TrimSpace(record.ID)
		if id == "" || records[id].ID != "" {
			return recorded, false, "typed-review"
		}
		records[id] = record
		if predecessor := strings.TrimSpace(record.SupersedesID); predecessor != "" {
			superseded[predecessor] = true
		}
	}
	bindings := map[string]externalEvidenceBinding{}
	for _, binding := range consumption.Bindings {
		if binding.Kind != codereview.EvidenceReview {
			continue
		}
		if bindings[binding.EvidenceID].EvidenceID != "" {
			return recorded, false, "typed-review"
		}
		bindings[binding.EvidenceID] = binding
	}
	var earliest time.Time
	var relevant []string
	reviewRecords := 0
	for id := range selected {
		record, exists := records[id]
		if !exists || record.Kind != codereview.EvidenceReview {
			continue
		}
		reviewRecords++
		binding, bound := bindings[id]
		if superseded[id] || !evaluated[id] || !bound || !validLegacyReviewRecord(record, binding, gate.Target, validationNow) {
			return recorded, false, "typed-review"
		}
		if earliest.IsZero() || record.ObservedAt.Before(earliest) {
			earliest = record.ObservedAt
		}
		if binding.SpecID == specID && exactStringInSlice(coverage.ImplementationBySpec[specID], binding.ProcessID) {
			relevant = append(relevant, id)
		}
	}
	if reviewRecords == 0 || earliest.IsZero() || len(relevant) == 0 {
		return recorded, false, "typed-review"
	}
	for id := range bindings {
		record, exists := records[id]
		if !selected[id] || !exists || record.Kind != codereview.EvidenceReview || superseded[id] {
			return recorded, false, "typed-review"
		}
	}
	if maximum := gate.ReviewCompletionPolicy.Freshness; maximum < 0 ||
		(maximum > 0 && validationNow.UTC().Sub(earliest) > maximum) {
		return recorded, false, "typed-review"
	}
	sort.Strings(relevant)
	return gate.Target.SubjectRevision, true, "native-authoritative-ledger:" + relevant[0]
}

func validExternalReviewGateContext(gate externalGateResult) bool {
	return gate.Evaluation.Passed && validateReviewCompletionTarget(gate.Target) == nil &&
		validateExactSnapshotIdentity(gate.Snapshot, gate.Target) == nil
}

func sameExternalReviewIdentity(consumption externalEvidenceConsumption, target coreevidence.NativeTarget) bool {
	return consumption.ProviderKey == target.Reference.ProviderKey &&
		consumption.ExternalRepository == target.Reference.ExternalRepository &&
		consumption.ChangeID == target.Reference.ChangeID && consumption.ReferenceVersion == target.ReferenceVersion &&
		consumption.SubjectRevision == target.SubjectRevision
}

func validLegacyReviewRecord(record codereview.EvidenceRecord, binding externalEvidenceBinding,
	target coreevidence.NativeTarget, validationNow time.Time) bool {
	if record.Kind != codereview.EvidenceReview || record.SubjectRevision != target.SubjectRevision ||
		!record.Trusted || strings.TrimSpace(record.WriterIdentity) == "" || strings.TrimSpace(record.PayloadDigest) == "" ||
		record.ObservedAt.IsZero() || record.ObservedAt.After(validationNow.UTC().Add(time.Minute)) ||
		(record.ValidUntil != nil && !validationNow.UTC().Before(*record.ValidUntil)) || record.ValidateReviewLinkage() != nil {
		return false
	}
	return binding.EvidenceID == record.ID && binding.Kind == codereview.EvidenceReview && binding.Trusted &&
		binding.Source == "native-authoritative-ledger" && binding.SubjectRevision == target.SubjectRevision &&
		binding.ProcessID == record.ProcessID && binding.SpecID == record.SpecID
}

func exactStringInSlice(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// reviewArtifactRevision turns a review-sync subject revision into trusted
// process evidence only after re-binding it to the authoritative PR facts
// collected for this command. The typed value records what review sync saw;
// the trusted value returned on a match comes from the current PR head.
func reviewArtifactRevision(artifact model.Artifact, requiredPRURL string, report reviewSyncReport,
	external []gates.ExternalProcessEvidence, processID, specID string) (string, bool, string) {
	if strings.TrimSpace(requiredPRURL) == "" {
		stamped, ok := parseConsumedEvidenceBlock(artifact.Comment.Body)
		if !ok {
			return strings.TrimSpace(artifact.Comment.SubjectRevision), false, "typed-review"
		}
		var revision, source string
		for _, evidence := range external {
			if evidence.ProcessID != processID || evidence.SpecID != specID || evidence.EvidenceKind != string(codereview.EvidenceReview) ||
				!evidence.Consumed || !evidence.Trusted ||
				len(evidence.EvidenceIDs) == 0 || evidence.SubjectRevision == "" ||
				evidence.SubjectRevision != evidence.EvidenceRevision || !strings.HasPrefix(evidence.Source, "native-authoritative-ledger:") ||
				!consumptionBlockMatchesEvidence(stamped, evidence) {
				continue
			}
			if revision != "" && revision != evidence.EvidenceRevision {
				return "", false, "native-authoritative-ledger"
			}
			revision = evidence.EvidenceRevision
			source = evidence.Source
		}
		if revision != "" {
			return revision, true, source
		}
		return strings.TrimSpace(artifact.Comment.SubjectRevision), false, "typed-review"
	}
	recorded := strings.TrimSpace(artifact.Comment.SubjectRevision)
	if recorded == "" {
		return "", false, "typed-review"
	}
	requiredPRURL = model.NormalizeURL(requiredPRURL)
	authoritativePRURL := model.NormalizeURL(report.PRURL)
	authoritativeRevision := strings.TrimSpace(report.SubjectRevision)
	authoritativeSource := strings.TrimSpace(report.RevisionSource)
	if requiredPRURL == "" || authoritativePRURL != requiredPRURL || authoritativeRevision == "" ||
		authoritativeSource == "" || !hasExactReviewPRCarrier(artifact.Comment.Links["PR"], requiredPRURL) ||
		!strings.EqualFold(recorded, authoritativeRevision) {
		return recorded, false, "typed-review"
	}
	return authoritativeRevision, true, authoritativeSource
}

func parseConsumedEvidenceBlock(body string) (externalEvidenceConsumption, bool) {
	if strings.Count(body, consumedEvidenceStart) != 1 || strings.Count(body, consumedEvidenceEnd) != 1 {
		return externalEvidenceConsumption{}, false
	}
	start := strings.Index(body, consumedEvidenceStart)
	end := strings.Index(body, consumedEvidenceEnd)
	if start < 0 || end <= start {
		return externalEvidenceConsumption{}, false
	}
	const prefix = "\n### Consumed External Evidence\n\n```json\n"
	const suffix = "\n```\n"
	rawBlock := body[start+len(consumedEvidenceStart) : end]
	if !strings.HasPrefix(rawBlock, prefix) || !strings.HasSuffix(rawBlock, suffix) {
		return externalEvidenceConsumption{}, false
	}
	raw := []byte(strings.TrimSuffix(strings.TrimPrefix(rawBlock, prefix), suffix))
	var consumption externalEvidenceConsumption
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&consumption); err != nil {
		return externalEvidenceConsumption{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return externalEvidenceConsumption{}, false
	}
	canonical, err := json.Marshal(consumption)
	if err != nil || !bytes.Equal(raw, canonical) {
		return externalEvidenceConsumption{}, false
	}
	return consumption, true
}

func consumptionBlockMatchesEvidence(consumption externalEvidenceConsumption, evidence gates.ExternalProcessEvidence) bool {
	if consumption.ProviderKey != evidence.ProviderKey || consumption.ExternalRepository != evidence.ExternalRepository ||
		consumption.ChangeID != evidence.ChangeID || consumption.ReferenceVersion != evidence.ReferenceVersion ||
		consumption.SubjectRevision != evidence.SubjectRevision {
		return false
	}
	id := strings.TrimPrefix(evidence.Source, "native-authoritative-ledger:")
	if id == "" || id == evidence.Source {
		return false
	}
	selected := false
	for _, candidate := range consumption.EvidenceIDs {
		if candidate == id {
			selected = true
		}
	}
	if !selected {
		return false
	}
	for _, binding := range consumption.Bindings {
		if binding.ProcessID == evidence.ProcessID && binding.SpecID == evidence.SpecID && binding.EvidenceID == id &&
			binding.Kind == codereview.EvidenceReview && binding.SubjectRevision == evidence.EvidenceRevision &&
			binding.Trusted && binding.Source == "native-authoritative-ledger" {
			return true
		}
	}
	return false
}

func hasExactReviewPRCarrier(values []string, want string) bool {
	want = model.NormalizeURL(want)
	if want == "" {
		return false
	}
	var carrier string
	count := 0
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.EqualFold(value, "N/A") {
			continue
		}
		count++
		if count > 1 {
			return false
		}
		carrier = model.NormalizeURL(value)
	}
	return count == 1 && carrier == want
}

func validateExternalEvidenceConsumption(consumption externalEvidenceConsumption, processes []model.Artifact, activeSpecs map[string]string) error {
	revision := strings.TrimSpace(consumption.SubjectRevision)
	if strings.TrimSpace(consumption.ProviderKey) == "" || consumption.ProviderKey != strings.TrimSpace(consumption.ProviderKey) ||
		strings.TrimSpace(consumption.ExternalRepository) == "" || consumption.ExternalRepository != strings.TrimSpace(consumption.ExternalRepository) ||
		strings.TrimSpace(consumption.ChangeID) == "" || consumption.ChangeID != strings.TrimSpace(consumption.ChangeID) ||
		consumption.ReferenceVersion <= 0 || revision == "" || consumption.SubjectRevision != revision || len(consumption.Bindings) == 0 {
		return fmt.Errorf("external evidence consumption has no revision-bound bindings")
	}
	selected := make(map[string]bool, len(consumption.EvidenceIDs))
	for _, raw := range consumption.EvidenceIDs {
		id := strings.TrimSpace(raw)
		if id == "" || id != raw || selected[id] {
			return fmt.Errorf("external evidence selection contains an invalid or duplicate id %q", raw)
		}
		selected[id] = true
	}
	processByID := make(map[string]model.Artifact, len(processes))
	for _, process := range processes {
		id := process.Comment.ID
		if !externalProcessIDPattern.MatchString(id) || processByID[id].Comment.ID != "" {
			return fmt.Errorf("active PROCESS identity %q is invalid or ambiguous", id)
		}
		processByID[id] = process
	}
	bound := make(map[string]bool, len(consumption.Bindings))
	for _, binding := range consumption.Bindings {
		if !externalProcessIDPattern.MatchString(binding.ProcessID) || !externalSpecIDPattern.MatchString(binding.SpecID) ||
			strings.TrimSpace(binding.EvidenceID) == "" || binding.EvidenceID != strings.TrimSpace(binding.EvidenceID) ||
			!selected[binding.EvidenceID] || bound[binding.EvidenceID] || !binding.Trusted ||
			binding.Source != "native-authoritative-ledger" || binding.SubjectRevision != revision ||
			(binding.Kind != codereview.EvidenceReview && binding.Kind != codereview.EvidenceCheck) {
			return fmt.Errorf("external evidence binding %q is invalid, conflicting, or stale", binding.EvidenceID)
		}
		process, processOK := processByID[binding.ProcessID]
		specURL, specOK := activeSpecs[binding.SpecID]
		if !processOK || !specOK || !artifactReferencesSpec(process, binding.SpecID, specURL) {
			return fmt.Errorf("external evidence binding %q does not map to an active PROCESS/SPEC edge", binding.EvidenceID)
		}
		bound[binding.EvidenceID] = true
	}
	return nil
}

func externalProcessEvidenceFor(processID string, activeSpecs map[string]string, consumption externalEvidenceConsumption) []gates.ExternalProcessEvidence {
	selected := make(map[string]bool, len(consumption.EvidenceIDs))
	for _, id := range consumption.EvidenceIDs {
		selected[strings.TrimSpace(id)] = true
	}
	var result []gates.ExternalProcessEvidence
	invalid := false
	seen := map[string]bool{}
	for _, binding := range consumption.Bindings {
		if strings.TrimSpace(binding.ProcessID) != processID {
			continue
		}
		_, activeSpec := activeSpecs[strings.TrimSpace(binding.SpecID)]
		validKind := binding.Kind == codereview.EvidenceReview || binding.Kind == codereview.EvidenceCheck
		valid := externalProcessIDPattern.MatchString(binding.ProcessID) && externalSpecIDPattern.MatchString(binding.SpecID) &&
			activeSpec && strings.TrimSpace(binding.EvidenceID) != "" && selected[strings.TrimSpace(binding.EvidenceID)] &&
			binding.Trusted && strings.TrimSpace(binding.SubjectRevision) != "" &&
			strings.TrimSpace(binding.SubjectRevision) == strings.TrimSpace(consumption.SubjectRevision) && validKind &&
			binding.Source == "native-authoritative-ledger"
		if !valid {
			invalid = true
			continue
		}
		key := binding.SpecID + "\x00" + binding.EvidenceID + "\x00" + string(binding.Kind)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, gates.ExternalProcessEvidence{ProcessID: processID, SpecID: binding.SpecID,
			ProviderKey: consumption.ProviderKey, ExternalRepository: consumption.ExternalRepository,
			ChangeID: consumption.ChangeID, ReferenceVersion: consumption.ReferenceVersion,
			SubjectRevision: consumption.SubjectRevision, EvidenceRevision: binding.SubjectRevision, EvidenceKind: string(binding.Kind), Consumed: true,
			EvidenceIDs: []string{binding.EvidenceID}, Trusted: true, Source: binding.Source + ":" + binding.EvidenceID})
	}
	if invalid {
		return nil
	}
	return result
}

func validCodeChangeRationaleAuthor(rationale gates.CodeChangeRationaleEvidence, process model.Artifact,
	activeSpecs map[string]string, external []gates.ExternalProcessEvidence) bool {
	want, active := activeSpecs[rationale.SpecID]
	if !active || strings.TrimSpace(rationale.AuthorAgent) == "" ||
		model.NormalizeURL(rationale.SpecURL) != model.NormalizeURL(want) || !artifactReferencesSpec(process, rationale.SpecID, want) {
		return false
	}
	matched := false
	for _, evidence := range external {
		if evidence.ProcessID != rationale.ProcessID || evidence.SpecID != rationale.SpecID {
			continue
		}
		if !evidence.Consumed || !evidence.Trusted || len(evidence.EvidenceIDs) == 0 || !strings.HasPrefix(evidence.Source, "native-authoritative-ledger:") ||
			evidence.SubjectRevision == "" || evidence.SubjectRevision != evidence.EvidenceRevision {
			return false
		}
		if evidence.ProviderKey == rationale.ProviderKey && evidence.ExternalRepository == rationale.ExternalRepository &&
			evidence.ChangeID == rationale.ChangeID && evidence.ReferenceVersion == rationale.ReferenceVersion &&
			evidence.SubjectRevision == rationale.SubjectRevision {
			matched = true
		}
	}
	return matched
}

func rationaleSpecURL(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Spec Comment:"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func artifactReferencesProcess(artifact, process model.Artifact) bool {
	return gates.ReferencesArtifactID(artifact.Comment.Body, process.Comment.ID) || linksContainURL(artifact.Comment.Links["Related Comments"], process.URL)
}

func artifactReferencesSpec(artifact model.Artifact, specID, specURL string) bool {
	return gates.ReferencesArtifactID(artifact.Comment.Body, specID) || linksContainURL(artifact.Comment.Links["Related Comments"], specURL)
}

func linksContainURL(values []string, want string) bool {
	want = model.NormalizeURL(want)
	for _, value := range values {
		if model.NormalizeURL(value) == want {
			return true
		}
	}
	return false
}
