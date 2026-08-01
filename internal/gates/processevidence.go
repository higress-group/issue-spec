package gates

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/model"
)

const (
	CodeProcessExecutionClassLegacy        = "process.execution_class.legacy"
	CodeProcessExecutionClassInvalid       = "process.execution_class.invalid"
	CodeProcessTaskLinkMissing             = "process.task_link_missing"
	CodeProcessSpecLinkMissing             = "process.spec_link_missing"
	CodeProcessCarrierMissing              = "process.carrier_missing"
	CodeProcessExecutorCoordinatorConflict = "process.executor.coordinator_conflict"
	CodeProcessReviewAuthorConflict        = "process.review.author_conflict"
)

type RationaleEvidence struct {
	ProcessID   string `json:"process_id"`
	SpecID      string `json:"spec_id"`
	SpecURL     string `json:"spec_url,omitempty"`
	MarkerPath  string `json:"marker_path"`
	MarkerLine  int    `json:"marker_line"`
	CommentPath string `json:"comment_path"`
	CommentLine int    `json:"comment_line"`
	AuthorAgent string `json:"author_agent,omitempty"`
}

type ReviewEvidence struct {
	ProcessID       string `json:"process_id"`
	SpecID          string `json:"spec_id"`
	URL             string `json:"url,omitempty"`
	Done            bool   `json:"done"`
	FindingResolved bool   `json:"finding_resolved"`
	ReviewerAgent   string `json:"reviewer_agent,omitempty"`
	SubjectRevision string `json:"subject_revision,omitempty"`
	Trusted         bool   `json:"trusted"`
	Source          string `json:"source,omitempty"`
}

type VerificationEvidence struct {
	ProcessID        string `json:"process_id"`
	SpecID           string `json:"spec_id"`
	URL              string `json:"url,omitempty"`
	Done             bool   `json:"done"`
	TestEvidence     bool   `json:"test_evidence"`
	StructuredTests  bool   `json:"structured_tests,omitempty"`
	TestAssurance    string `json:"test_assurance,omitempty"`
	StructuredChecks bool   `json:"structured_checks,omitempty"`
	SubjectRevision  string `json:"subject_revision,omitempty"`
	Trusted          bool   `json:"trusted"`
	Source           string `json:"source,omitempty"`
}

type CheckEvidence struct {
	ProcessID       string `json:"process_id"`
	SpecID          string `json:"spec_id"`
	Name            string `json:"name"`
	Required        bool   `json:"required"`
	Passed          bool   `json:"passed"`
	TestEvidence    bool   `json:"test_evidence"`
	SubjectRevision string `json:"subject_revision,omitempty"`
	Trusted         bool   `json:"trusted"`
	Source          string `json:"source,omitempty"`
}

type ExternalProcessEvidence struct {
	ProcessID          string   `json:"process_id"`
	SpecID             string   `json:"spec_id"`
	ProviderKey        string   `json:"provider_key,omitempty"`
	ExternalRepository string   `json:"external_repository,omitempty"`
	ChangeID           string   `json:"change_id,omitempty"`
	ReferenceVersion   int64    `json:"reference_version,omitempty"`
	SubjectRevision    string   `json:"subject_revision"`
	EvidenceRevision   string   `json:"evidence_revision"`
	EvidenceKind       string   `json:"evidence_kind,omitempty"`
	Consumed           bool     `json:"consumed"`
	EvidenceIDs        []string `json:"evidence_ids,omitempty"`
	Trusted            bool     `json:"trusted"`
	Source             string   `json:"source,omitempty"`
}

// ActiveAssignmentEvidence is the exact portable role assignment authority
// projected from one managed PROCESS. Complete assignment bodies stay in the
// workspace store; final selection needs only this immutable identity plus the
// structured PROCESS selectors used for exact stable-identity matching.
type ActiveAssignmentEvidence struct {
	ProcessID        string                     `json:"process_id"`
	AssignmentID     string                     `json:"assignment_id"`
	AssignmentDigest string                     `json:"assignment_digest"`
	Generation       uint64                     `json:"generation"`
	Role             assignment.Role            `json:"role"`
	SubjectRevision  string                     `json:"subject_revision,omitempty"`
	RequiredTests    []assignment.TestSelector  `json:"required_tests,omitempty"`
	RequiredChecks   []assignment.CheckSelector `json:"required_checks,omitempty"`
}

type CodeChangeRationaleEvidence struct {
	ProcessID          string `json:"process_id"`
	SpecID             string `json:"spec_id"`
	SpecURL            string `json:"spec_url"`
	ProviderKey        string `json:"provider_key"`
	ExternalRepository string `json:"external_repository"`
	ChangeID           string `json:"change_id"`
	ReferenceVersion   int64  `json:"reference_version"`
	SubjectRevision    string `json:"subject_revision"`
	AuthorAgent        string `json:"author_agent"`
	AuthorSessionID    string `json:"author_session_id"`
	URL                string `json:"url,omitempty"`
}

type ProcessEvidenceInput struct {
	Process          model.Artifact            `json:"process"`
	RequiredPRURL    string                    `json:"required_pr_url,omitempty"`
	ActiveAssignment *ActiveAssignmentEvidence `json:"active_assignment,omitempty"`
	// RequiredRevision is the authoritative PR/provider head that review
	// evidence must cover. When present, review satisfaction is evaluated per
	// SPEC so one current carrier cannot hide another SPEC's stale evidence.
	RequiredRevision string            `json:"required_revision,omitempty"`
	ActiveSpecs      map[string]string `json:"active_specs,omitempty"`
	TaskURLs         map[string]bool   `json:"task_urls,omitempty"`
	// AuthorAgentsBySpec maps an active SPEC ID to the set of normalized
	// (lowercased, trimmed) --agent names that authored change-bearing code
	// rationale for that SPEC. A review PROCESS whose reviewer --agent name is
	// in this set for the SPEC it covers fails the independence check.
	AuthorAgentsBySpec   map[string]map[string]bool    `json:"author_agents_by_spec,omitempty"`
	Rationales           []RationaleEvidence           `json:"rationales,omitempty"`
	CodeChangeRationales []CodeChangeRationaleEvidence `json:"code_change_rationales,omitempty"`
	Reviews              []ReviewEvidence              `json:"reviews,omitempty"`
	Verifications        []VerificationEvidence        `json:"verifications,omitempty"`
	Checks               []CheckEvidence               `json:"checks,omitempty"`
	External             []ExternalProcessEvidence     `json:"external,omitempty"`
}

type ProcessEvidenceReport struct {
	ProcessID       string                      `json:"process_id"`
	ProcessURL      string                      `json:"process_url,omitempty"`
	ExecutionClass  model.ProcessExecutionClass `json:"execution_class"`
	ExplicitClass   bool                        `json:"explicit_class"`
	Required        []string                    `json:"required"`
	Satisfied       []string                    `json:"satisfied,omitempty"`
	Missing         []string                    `json:"missing,omitempty"`
	Diagnostics     []Diagnostic                `json:"diagnostics,omitempty"`
	CarrierRevision CarrierRevisionFact         `json:"carrier_revision"`
	// SatisfiedSpecs lists the active SPEC IDs this PROCESS cleanly satisfied
	// for its execution class: for change-bearing, the SPECs with a matching
	// inline rationale or exact-current self-hosted rationale/evidence carrier;
	// for review, the SPECs independently (non-self) reviewed. The final gate
	// joins these across PROCESS reports to require an independent review PROCESS
	// for every change-bearing SPEC.
	SatisfiedSpecs []string `json:"satisfied_specs,omitempty"`
}

// ProcessCarrierRevisionFacts projects provider-owned carrier facts by PROCESS.
// Expected PR/provider heads are deliberately absent: callers compare these
// collected facts with their expectation in the workspace gate.
func ProcessCarrierRevisionFacts(reports []ProcessEvidenceReport) map[string]CarrierRevisionFact {
	facts := make(map[string]CarrierRevisionFact, len(reports))
	for _, report := range reports {
		facts[report.ProcessID] = report.CarrierRevision
	}
	return facts
}

func EvaluateProcessEvidence(input ProcessEvidenceInput, target Target, mode Mode) ProcessEvidenceReport {
	process := input.Process
	parsed := model.ParseProcessExecutionClass(process.Comment.ID, process.URL, process.Comment.Body)
	report := ProcessEvidenceReport{ProcessID: process.Comment.ID, ProcessURL: process.URL,
		ExecutionClass: parsed.Class, ExplicitClass: parsed.Explicit}
	add := func(code string, severity Severity, blocking bool, message, current, expected, command string) {
		report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: code, Gate: target, Severity: severity,
			Blocking: blocking, Message: message, Artifact: artifactRef(process), Current: current, Expected: expected,
			Remediation: Remediation{CommandFamily: command}, Freshness: FreshnessLocal})
	}
	for _, diagnostic := range parsed.Diagnostics {
		if diagnostic.Severity == "warning" {
			add(CodeProcessExecutionClassLegacy, SeverityWarning, false, diagnostic.Message, "missing", "explicit class", "comment generate")
		} else {
			add(CodeProcessExecutionClassInvalid, SeverityError, true, diagnostic.Message, diagnostic.Element, "known class", "comment generate")
		}
	}
	if parsed.Blocking() {
		report.Missing = []string{"valid execution class"}
		return report
	}

	report.Required = append(report.Required, "TASK link", "PR link", "active SPEC coverage")
	taskLinked := hasRelatedURL(process, input.TaskURLs)
	prLinked := hasRequiredPRLink(process.Comment.Links["PR"], input.RequiredPRURL)
	activeSpec := func(id string) bool { _, ok := input.ActiveSpecs[id]; return ok }
	specSatisfied := false
	if taskLinked {
		report.Satisfied = append(report.Satisfied, "TASK link")
	} else {
		report.Missing = append(report.Missing, "TASK link")
		add(CodeProcessTaskLinkMissing, SeverityError, true, "PROCESS is not linked to an active TASK", "missing", "linked", "link")
	}
	if prLinked {
		report.Satisfied = append(report.Satisfied, "PR link")
	} else {
		report.Missing = append(report.Missing, "PR link")
		add(CodeProcessPRLinkMissing, SeverityError, true, "PROCESS does not link the required PR", "missing", input.RequiredPRURL, "pr link-process")
	}

	switch parsed.Class {
	case model.ProcessExecutionChangeBearing:
		providerBacked := strings.TrimSpace(input.RequiredPRURL) == "" &&
			(strings.TrimSpace(input.RequiredRevision) != "" || len(input.External) > 0 || len(input.CodeChangeRationales) > 0)
		if providerBacked {
			report.Required = append(report.Required, "exact-current code-change rationale with trusted provider evidence")
		} else {
			report.Required = append(report.Required, "inline rationale on matching PR path/line")
		}
		carrier := false
		carriedSpecs := map[string]bool{}
		conflictedAgentsBySpec := map[string]string{}
		coordinator := normalizeAgent(process.Comment.Agent)
		if providerBacked {
			externalBySpec, invalidExternal := exactProviderEvidenceBySpec(input, report.ProcessID, activeSpec)
			var revisions []CarrierRevisionFact
			for _, rationale := range input.CodeChangeRationales {
				if rationale.ProcessID != report.ProcessID || !activeSpec(rationale.SpecID) || invalidExternal[rationale.SpecID] {
					continue
				}
				if want := input.ActiveSpecs[rationale.SpecID]; rationale.SpecURL == "" ||
					model.NormalizeURL(rationale.SpecURL) != model.NormalizeURL(want) {
					continue
				}
				matched := false
				for _, external := range externalBySpec[rationale.SpecID] {
					if rationale.ProviderKey == external.ProviderKey && rationale.ExternalRepository == external.ExternalRepository &&
						rationale.ChangeID == external.ChangeID && rationale.ReferenceVersion == external.ReferenceVersion &&
						rationale.SubjectRevision == external.SubjectRevision {
						matched = true
						revisions = append(revisions, CarrierRevisionFact{Known: true, Revision: external.EvidenceRevision,
							Trusted: true, Source: external.Source})
					}
				}
				if !matched {
					continue
				}
				if author := normalizeAgent(rationale.AuthorAgent); coordinator != "" && author != "" && author == coordinator {
					conflictedAgentsBySpec[rationale.SpecID] = strings.TrimSpace(rationale.AuthorAgent)
					continue
				}
				carrier, specSatisfied = true, true
				carriedSpecs[rationale.SpecID] = true
			}
			report.CarrierRevision = aggregateCarrierRevisions(revisions)
		} else {
			for _, evidence := range input.Rationales {
				if evidence.ProcessID != report.ProcessID || !activeSpec(evidence.SpecID) {
					continue
				}
				if evidence.MarkerPath == "" || evidence.MarkerLine <= 0 || evidence.MarkerPath != evidence.CommentPath || evidence.MarkerLine != evidence.CommentLine {
					continue
				}
				if want := input.ActiveSpecs[evidence.SpecID]; evidence.SpecURL == "" || model.NormalizeURL(evidence.SpecURL) != model.NormalizeURL(want) {
					continue
				}
				if author := normalizeAgent(evidence.AuthorAgent); coordinator != "" && author != "" && author == coordinator {
					conflictedAgentsBySpec[evidence.SpecID] = strings.TrimSpace(evidence.AuthorAgent)
					continue
				}
				carrier, specSatisfied = true, true
				carriedSpecs[evidence.SpecID] = true
			}
		}
		report.SatisfiedSpecs = sortedKeys(carriedSpecs)
		for _, spec := range sortedKeys(conflictedAgentsBySpec) {
			agent := conflictedAgentsBySpec[spec]
			add(CodeProcessExecutorCoordinatorConflict, SeverityError, true,
				fmt.Sprintf("change-bearing PROCESS evidence for %s was authored by agent %q, which is also the PROCESS coordinator; agent-executed change-bearing code MUST be authored by a real non-coordinator worker, so dispatch the work to a runtime-native child and add rationale under that worker's --agent identity", spec, agent),
				"same agent as PROCESS coordinator", "rationale authored by a real non-coordinator worker", "pr rationale")
		}
		if carrier {
			if providerBacked {
				report.Satisfied = append(report.Satisfied, "exact-current provider rationale")
			} else {
				report.Satisfied = append(report.Satisfied, "matching inline rationale")
			}
		} else if len(conflictedAgentsBySpec) > 0 {
			if providerBacked {
				report.Missing = append(report.Missing, "non-coordinator exact-current provider rationale")
			} else {
				report.Missing = append(report.Missing, "non-coordinator matching inline rationale")
			}
		} else {
			if providerBacked {
				report.Missing = append(report.Missing, "exact-current provider rationale")
				add(CodeProcessCarrierMissing, SeverityError, true, "change-bearing PROCESS lacks an append-only code-change rationale exactly matching the active reference and trusted native-ledger PROCESS/SPEC evidence", "missing or stale", "exact-current rationale plus trusted provider evidence", "code-change rationale")
			} else {
				report.Missing = append(report.Missing, "matching inline rationale")
				add(CodeProcessCarrierMissing, SeverityError, true, "change-bearing PROCESS lacks an inline rationale whose marker path/line matches the real PR comment and active SPEC", "missing", "matching rationale", "pr rationale")
			}
		}
	case model.ProcessExecutionReview:
		report.Required = append(report.Required, "linked done REVIEW or resolved finding by an independent agent")
		// Group review evidence by REVIEW artifact: a reviewer who authored code
		// for ANY SPEC the artifact covers taints the whole artifact, so a clean
		// SPEC on the same REVIEW can never rescue another SPEC's conflict.
		// Satisfaction is then tracked PER SPEC: a SPEC whose review conflicts
		// with its code author MUST be independently re-covered by a clean
		// artifact for THAT SPEC; a clean review of some other SPEC cannot
		// substitute. The node is satisfied only when at least one SPEC is
		// cleanly covered and every conflicted SPEC is independently re-covered.
		type reviewGroup struct {
			conflicted bool
			evidence   []ReviewEvidence
		}
		groups := map[string]*reviewGroup{}
		var order []string
		claimedSpecs := map[string]bool{}
		cleanCovered := map[string]bool{}
		conflictedAgentBySpec := map[string]string{}
		var conflictOrder []string
		for i, evidence := range input.Reviews {
			if evidence.ProcessID != report.ProcessID || !activeSpec(evidence.SpecID) || !(evidence.Done || evidence.FindingResolved) {
				continue
			}
			claimedSpecs[evidence.SpecID] = true
			key := strings.TrimSpace(evidence.URL)
			if key == "" {
				key = fmt.Sprintf("\x00entry-%d", i)
			}
			group := groups[key]
			if group == nil {
				group = &reviewGroup{}
				groups[key] = group
				order = append(order, key)
			}
			if reviewer := normalizeAgent(evidence.ReviewerAgent); reviewer != "" && input.AuthorAgentsBySpec[evidence.SpecID][reviewer] {
				group.conflicted = true
				if _, seen := conflictedAgentBySpec[evidence.SpecID]; !seen {
					conflictOrder = append(conflictOrder, evidence.SpecID)
				}
				conflictedAgentBySpec[evidence.SpecID] = strings.TrimSpace(evidence.ReviewerAgent)
				continue
			}
			group.evidence = append(group.evidence, evidence)
		}
		cleanBySpec := map[string][]ReviewEvidence{}
		for _, key := range order {
			group := groups[key]
			if group.conflicted {
				continue
			}
			for _, evidence := range group.evidence {
				cleanBySpec[evidence.SpecID] = append(cleanBySpec[evidence.SpecID], evidence)
			}
		}

		// Select carriers only after independence is known. At an authoritative
		// revision, every claimed SPEC needs its own exact-current trusted
		// carrier. A current typed REVIEW takes precedence over resolved findings
		// for the same SPEC, but a self-authored typed REVIEW never suppresses an
		// independent finding because it was removed with its conflicted group.
		requiredRevision := strings.TrimSpace(input.RequiredRevision)
		var revisions []CarrierRevisionFact
		var missingCurrentSpec string
		for _, spec := range sortedKeys(claimedSpecs) {
			candidates := cleanBySpec[spec]
			var typed, findings []ReviewEvidence
			for _, evidence := range candidates {
				revision := strings.TrimSpace(evidence.SubjectRevision)
				if requiredRevision != "" && (!evidence.Trusted || revision == "" || !strings.EqualFold(revision, requiredRevision)) {
					continue
				}
				if evidence.Done {
					typed = append(typed, evidence)
				} else {
					findings = append(findings, evidence)
				}
			}
			var selected []ReviewEvidence
			if requiredRevision != "" {
				selected = append(selected, typed...)
				if len(selected) == 0 {
					selected = findings
				}
			} else {
				// Without an authoritative head, retain legacy semantic
				// visibility. Prefer a revision-bound typed REVIEW when one is
				// available; otherwise a resolved finding remains the trusted
				// compatibility carrier before falling back to revisionless text.
				for _, evidence := range typed {
					if evidence.Trusted && strings.TrimSpace(evidence.SubjectRevision) != "" {
						selected = append(selected, evidence)
					}
				}
				if len(selected) == 0 {
					selected = append(selected, findings...)
				}
				if len(selected) == 0 {
					selected = typed
				}
			}
			if len(selected) == 0 {
				if missingCurrentSpec == "" {
					missingCurrentSpec = spec
				}
				continue
			}
			cleanCovered[spec] = true
			for _, evidence := range selected {
				revisions = append(revisions, CarrierRevisionFact{Known: strings.TrimSpace(evidence.SubjectRevision) != "",
					Revision: strings.TrimSpace(evidence.SubjectRevision), Trusted: evidence.Trusted, Source: evidence.Source})
			}
		}
		var conflictAgent, conflictSpec string
		for _, spec := range conflictOrder {
			if !cleanCovered[spec] {
				conflictSpec, conflictAgent = spec, conflictedAgentBySpec[spec]
				break
			}
		}
		carrier := len(cleanCovered) > 0 && missingCurrentSpec == "" && conflictSpec == ""
		specSatisfied = len(cleanCovered) > 0
		report.SatisfiedSpecs = sortedKeys(cleanCovered)
		report.CarrierRevision = aggregateCarrierRevisions(revisions)
		if requiredRevision != "" && missingCurrentSpec != "" && report.CarrierRevision.Known {
			// A trusted current carrier for one SPEC must not become the PROCESS
			// carrier while another claimed SPEC has no exact-current carrier.
			// Preserve the observed revision for diagnostics, but fail closed on
			// trust so the workspace gate also rejects the mixed evidence set.
			report.CarrierRevision.Trusted = false
		}
		switch {
		case carrier:
			report.Satisfied = append(report.Satisfied, "review evidence")
		case conflictSpec != "":
			report.Missing = append(report.Missing, "independent review evidence")
			add(CodeProcessReviewAuthorConflict, SeverityError, true,
				fmt.Sprintf("review PROCESS evidence for %s was authored by agent %q, which also authored the code under review; the review MUST be authored by a different agent than the code author, so route %s through a review PROCESS owned by an independent reviewing agent (its --agent must differ from the code author) and re-run review sync once that node produces its REVIEW or resolved finding", conflictSpec, conflictAgent, conflictSpec),
				"same agent as code author", "review authored by an independent reviewing agent (different --agent than the code author)", "review sync")
		case missingCurrentSpec != "" && requiredRevision != "":
			report.Missing = append(report.Missing, "exact-current independent review evidence")
			add(CodeProcessCarrierMissing, SeverityError, true,
				fmt.Sprintf("review PROCESS lacks exact-current trusted independent review evidence for %s", missingCurrentSpec),
				"missing or stale", requiredRevision, "review sync")
		default:
			report.Missing = append(report.Missing, "review evidence")
			add(CodeProcessCarrierMissing, SeverityError, true, "review PROCESS lacks linked done REVIEW or resolved finding evidence for an active SPEC", "missing", "review evidence", "review sync")
		}
	case model.ProcessExecutionVerification:
		report.Required = append(report.Required, "linked done VERIFY or required passing check with test evidence")
		carrier := false
		requiredRevision := strings.TrimSpace(input.RequiredRevision)
		var revisions []CarrierRevisionFact
		for _, evidence := range input.Verifications {
			if evidence.ProcessID != report.ProcessID || !activeSpec(evidence.SpecID) || !evidence.Done || !evidence.TestEvidence {
				continue
			}
			if strings.HasPrefix(evidence.Source, "accepted-verification-receipt:") &&
				((!evidence.StructuredTests && !evidence.StructuredChecks) ||
					(evidence.StructuredTests && evidence.TestAssurance != "self-reported")) {
				continue
			}
			revision := strings.TrimSpace(evidence.SubjectRevision)
			// Revisionless typed VERIFY comments remain semantic compatibility
			// evidence; the snapshot workspace gate separately requires a trusted
			// exact carrier before readiness. Once a VERIFY claims a revision here,
			// it must itself be trusted and exact-current.
			if requiredRevision != "" && revision != "" && (!evidence.Trusted || !strings.EqualFold(revision, requiredRevision)) {
				continue
			}
			carrier, specSatisfied = true, true
			revisions = append(revisions, CarrierRevisionFact{Known: revision != "", Revision: revision,
				Trusted: evidence.Trusted, Source: evidence.Source})
		}
		for _, evidence := range input.Checks {
			if evidence.ProcessID != report.ProcessID || !activeSpec(evidence.SpecID) || !evidence.Required || !evidence.Passed || !evidence.TestEvidence {
				continue
			}
			revision := strings.TrimSpace(evidence.SubjectRevision)
			if requiredRevision != "" && (!evidence.Trusted || revision == "" || !strings.EqualFold(revision, requiredRevision)) {
				continue
			}
			carrier, specSatisfied = true, true
			revisions = append(revisions, CarrierRevisionFact{Known: revision != "", Revision: revision,
				Trusted: evidence.Trusted, Source: evidence.Source})
		}
		report.CarrierRevision = aggregateCarrierRevisions(revisions)
		if carrier {
			report.Satisfied = append(report.Satisfied, "verification evidence")
		} else if requiredRevision != "" {
			report.Missing = append(report.Missing, "exact-current verification evidence")
			add(CodeProcessCarrierMissing, SeverityError, true,
				"verification PROCESS lacks linked done VERIFY or a required passing check with trusted test evidence at the exact current revision",
				"missing, stale, or untrusted", requiredRevision, "verify submit")
		} else {
			report.Missing = append(report.Missing, "verification evidence")
			add(CodeProcessCarrierMissing, SeverityError, true, "verification PROCESS lacks linked done VERIFY or a required passing check with test evidence for an active SPEC", "missing", "verification evidence", "comment upsert")
		}
	case model.ProcessExecutionOrchestration:
		report.Required = append(report.Required, "non-empty coordination handoff")
		for id := range input.ActiveSpecs {
			if ReferencesArtifactID(process.Comment.Body, id) {
				specSatisfied = true
				break
			}
		}
		if !emptyOrNA(section(process.Comment.Body, "### Handoff")) {
			report.Satisfied = append(report.Satisfied, "coordination handoff")
		} else {
			report.Missing = append(report.Missing, "coordination handoff")
			add(CodeProcessCarrierMissing, SeverityError, true, "orchestration PROCESS lacks non-empty coordination handoff", "missing", "non-empty handoff", "comment transition")
		}
	case model.ProcessExecutionExternal:
		report.Required = append(report.Required, "consumed exact-revision provider evidence")
		carrier := false
		var revisions []CarrierRevisionFact
		for _, evidence := range input.External {
			if evidence.ProcessID == report.ProcessID && activeSpec(evidence.SpecID) && evidence.Consumed && evidence.Trusted && evidence.SubjectRevision != "" && evidence.SubjectRevision == evidence.EvidenceRevision && len(evidence.EvidenceIDs) > 0 {
				carrier, specSatisfied = true, true
				revisions = append(revisions, CarrierRevisionFact{Known: true, Revision: strings.TrimSpace(evidence.EvidenceRevision),
					Trusted: true, Source: evidence.Source})
			}
		}
		report.CarrierRevision = aggregateCarrierRevisions(revisions)
		if carrier {
			report.Satisfied = append(report.Satisfied, "exact-revision external evidence")
		} else {
			report.Missing = append(report.Missing, "exact-revision external evidence")
			add(CodeProcessCarrierMissing, SeverityError, true, "external PROCESS lacks consumed provider evidence at the exact subject revision for an active SPEC", "missing", "exact-revision consumed evidence", "evidence explain")
		}
	}
	if specSatisfied {
		report.Satisfied = append(report.Satisfied, "active SPEC coverage")
	} else {
		report.Missing = append(report.Missing, "active SPEC coverage")
		add(CodeProcessSpecLinkMissing, SeverityError, true, "PROCESS evidence does not cover an active SPEC", "missing", "active SPEC", "link")
	}
	sort.Strings(report.Required)
	sort.Strings(report.Satisfied)
	sort.Strings(report.Missing)
	return report
}

func aggregateCarrierRevisions(candidates []CarrierRevisionFact) CarrierRevisionFact {
	trusted := make([]CarrierRevisionFact, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Known && candidate.Trusted && strings.TrimSpace(candidate.Revision) != "" {
			trusted = append(trusted, candidate)
		}
	}
	if len(trusted) == 0 {
		if len(candidates) == 0 {
			return CarrierRevisionFact{}
		}
		return CarrierRevisionFact{Source: firstNonEmptySource(candidates)}
	}
	revision := strings.TrimSpace(trusted[0].Revision)
	sources := make([]string, 0, len(trusted))
	for _, candidate := range trusted {
		sources = append(sources, candidate.Source)
		if strings.TrimSpace(candidate.Revision) != revision {
			return CarrierRevisionFact{Known: true, Revision: revision, Trusted: false,
				Source: strings.Join(sortedNonEmpty(sources), ",")}
		}
	}
	return CarrierRevisionFact{Known: true, Revision: revision, Trusted: true, Source: strings.Join(sortedNonEmpty(sources), ",")}
}

func normalizeAgent(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func sortedKeys[V any](set map[string]V) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func firstNonEmptySource(candidates []CarrierRevisionFact) string {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.Source) != "" {
			return candidate.Source
		}
	}
	return ""
}

func sortedNonEmpty(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

// ReferencesArtifactID reports an exact, token-bounded typed artifact ID.
// Prefix collisions such as PROCESS-001/PROCESS-0010 and SPEC-001/SPEC-0010
// must never let evidence for one logical artifact satisfy another.
func ReferencesArtifactID(body, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	pattern := `(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(id) + `($|[^A-Za-z0-9_-])`
	return regexp.MustCompile(pattern).MatchString(body)
}

func hasRelatedURL(process model.Artifact, urls map[string]bool) bool {
	for _, url := range process.Comment.Links["Related Comments"] {
		if urls[model.NormalizeURL(url)] {
			return true
		}
	}
	return false
}

func hasExactLink(values []string, want string) bool {
	want = model.NormalizeURL(want)
	for _, value := range values {
		if model.NormalizeURL(value) == want {
			return true
		}
	}
	return false
}

func hasRequiredPRLink(values []string, want string) bool {
	if strings.TrimSpace(want) != "" {
		return hasExactLink(values, want)
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !strings.EqualFold(value, "N/A") {
			return true
		}
	}
	return false
}

func exactProviderEvidenceBySpec(input ProcessEvidenceInput, processID string,
	activeSpec func(string) bool) (map[string][]ExternalProcessEvidence, map[string]bool) {
	result := map[string][]ExternalProcessEvidence{}
	invalid := map[string]bool{}
	requiredRevision := strings.TrimSpace(input.RequiredRevision)
	for _, evidence := range input.External {
		if evidence.ProcessID != processID || !activeSpec(evidence.SpecID) {
			continue
		}
		valid := evidence.Consumed && evidence.Trusted && len(evidence.EvidenceIDs) > 0 &&
			strings.TrimSpace(evidence.ProviderKey) != "" && strings.TrimSpace(evidence.ExternalRepository) != "" &&
			strings.TrimSpace(evidence.ChangeID) != "" && evidence.ReferenceVersion > 0 &&
			strings.TrimSpace(evidence.SubjectRevision) != "" && evidence.SubjectRevision == evidence.EvidenceRevision &&
			(requiredRevision == "" || evidence.SubjectRevision == requiredRevision) &&
			strings.HasPrefix(evidence.Source, "native-authoritative-ledger:")
		if !valid {
			invalid[evidence.SpecID] = true
			continue
		}
		result[evidence.SpecID] = append(result[evidence.SpecID], evidence)
	}
	return result, invalid
}

func (r ProcessEvidenceReport) Summary() string {
	return fmt.Sprintf("%s class=%s explicit=%t required=%s satisfied=%s missing=%s", r.ProcessID, r.ExecutionClass, r.ExplicitClass,
		strings.Join(r.Required, ","), strings.Join(r.Satisfied, ","), strings.Join(r.Missing, ","))
}
