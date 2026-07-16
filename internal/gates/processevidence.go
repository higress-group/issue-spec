package gates

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
)

const (
	CodeProcessExecutionClassLegacy  = "process.execution_class.legacy"
	CodeProcessExecutionClassInvalid = "process.execution_class.invalid"
	CodeProcessTaskLinkMissing       = "process.task_link_missing"
	CodeProcessSpecLinkMissing       = "process.spec_link_missing"
	CodeProcessCarrierMissing        = "process.carrier_missing"
	CodeProcessReviewAuthorConflict  = "process.review.author_conflict"
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
	ProcessID       string `json:"process_id"`
	SpecID          string `json:"spec_id"`
	URL             string `json:"url,omitempty"`
	Done            bool   `json:"done"`
	TestEvidence    bool   `json:"test_evidence"`
	SubjectRevision string `json:"subject_revision,omitempty"`
	Trusted         bool   `json:"trusted"`
	Source          string `json:"source,omitempty"`
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
	ProcessID        string   `json:"process_id"`
	SpecID           string   `json:"spec_id"`
	SubjectRevision  string   `json:"subject_revision"`
	EvidenceRevision string   `json:"evidence_revision"`
	Consumed         bool     `json:"consumed"`
	EvidenceIDs      []string `json:"evidence_ids,omitempty"`
	Trusted          bool     `json:"trusted"`
	Source           string   `json:"source,omitempty"`
}

type ProcessEvidenceInput struct {
	Process       model.Artifact    `json:"process"`
	RequiredPRURL string            `json:"required_pr_url,omitempty"`
	ActiveSpecs   map[string]string `json:"active_specs,omitempty"`
	TaskURLs      map[string]bool   `json:"task_urls,omitempty"`
	// AuthorAgentsBySpec maps an active SPEC ID to the set of normalized
	// (lowercased, trimmed) --agent names that authored change-bearing code
	// rationale for that SPEC. A review PROCESS whose reviewer --agent name is
	// in this set for the SPEC it covers fails the independence check.
	AuthorAgentsBySpec map[string]map[string]bool `json:"author_agents_by_spec,omitempty"`
	Rationales         []RationaleEvidence        `json:"rationales,omitempty"`
	Reviews            []ReviewEvidence           `json:"reviews,omitempty"`
	Verifications      []VerificationEvidence     `json:"verifications,omitempty"`
	Checks             []CheckEvidence            `json:"checks,omitempty"`
	External           []ExternalProcessEvidence  `json:"external,omitempty"`
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
		report.Required = append(report.Required, "inline rationale on matching PR path/line")
		carrier := false
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
			carrier, specSatisfied = true, true
			break
		}
		if carrier {
			report.Satisfied = append(report.Satisfied, "matching inline rationale")
		} else {
			report.Missing = append(report.Missing, "matching inline rationale")
			add(CodeProcessCarrierMissing, SeverityError, true, "change-bearing PROCESS lacks an inline rationale whose marker path/line matches the real PR comment and active SPEC", "missing", "matching rationale", "pr rationale")
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
			specs      map[string]bool
			conflicted bool
			revisions  []CarrierRevisionFact
		}
		groups := map[string]*reviewGroup{}
		var order []string
		cleanCovered := map[string]bool{}
		conflictedAgentBySpec := map[string]string{}
		var conflictOrder []string
		for i, evidence := range input.Reviews {
			if evidence.ProcessID != report.ProcessID || !activeSpec(evidence.SpecID) || !(evidence.Done || evidence.FindingResolved) {
				continue
			}
			key := strings.TrimSpace(evidence.URL)
			if key == "" {
				key = fmt.Sprintf("\x00entry-%d", i)
			}
			group := groups[key]
			if group == nil {
				group = &reviewGroup{specs: map[string]bool{}}
				groups[key] = group
				order = append(order, key)
			}
			group.specs[evidence.SpecID] = true
			if reviewer := normalizeAgent(evidence.ReviewerAgent); reviewer != "" && input.AuthorAgentsBySpec[evidence.SpecID][reviewer] {
				group.conflicted = true
				if _, seen := conflictedAgentBySpec[evidence.SpecID]; !seen {
					conflictOrder = append(conflictOrder, evidence.SpecID)
				}
				conflictedAgentBySpec[evidence.SpecID] = strings.TrimSpace(evidence.ReviewerAgent)
				continue
			}
			group.revisions = append(group.revisions, CarrierRevisionFact{Known: strings.TrimSpace(evidence.SubjectRevision) != "",
				Revision: strings.TrimSpace(evidence.SubjectRevision), Trusted: evidence.Trusted, Source: evidence.Source})
		}
		cleanArtifact := false
		var revisions []CarrierRevisionFact
		for _, key := range order {
			group := groups[key]
			if group.conflicted {
				continue
			}
			cleanArtifact = true
			for spec := range group.specs {
				cleanCovered[spec] = true
			}
			revisions = append(revisions, group.revisions...)
		}
		var conflictAgent, conflictSpec string
		for _, spec := range conflictOrder {
			if !cleanCovered[spec] {
				conflictSpec, conflictAgent = spec, conflictedAgentBySpec[spec]
				break
			}
		}
		carrier := cleanArtifact && conflictSpec == ""
		if carrier {
			specSatisfied = true
		}
		report.CarrierRevision = aggregateCarrierRevisions(revisions)
		switch {
		case carrier:
			report.Satisfied = append(report.Satisfied, "review evidence")
		case conflictSpec != "":
			report.Missing = append(report.Missing, "independent review evidence")
			add(CodeProcessReviewAuthorConflict, SeverityError, true,
				fmt.Sprintf("review PROCESS evidence for %s was authored by agent %q, which also authored the code under review; the review MUST be authored by a different agent than the code author, so route %s through a review PROCESS owned by an independent reviewing agent (its --agent must differ from the code author) and re-run review sync once that node produces its REVIEW or resolved finding", conflictSpec, conflictAgent, conflictSpec),
				"same agent as code author", "review authored by an independent reviewing agent (different --agent than the code author)", "review sync")
		default:
			report.Missing = append(report.Missing, "review evidence")
			add(CodeProcessCarrierMissing, SeverityError, true, "review PROCESS lacks linked done REVIEW or resolved finding evidence for an active SPEC", "missing", "review evidence", "review sync")
		}
	case model.ProcessExecutionVerification:
		report.Required = append(report.Required, "linked done VERIFY or required passing check with test evidence")
		carrier := false
		var revisions []CarrierRevisionFact
		for _, evidence := range input.Verifications {
			if evidence.ProcessID == report.ProcessID && activeSpec(evidence.SpecID) && evidence.Done && evidence.TestEvidence {
				carrier, specSatisfied = true, true
				revisions = append(revisions, CarrierRevisionFact{Known: strings.TrimSpace(evidence.SubjectRevision) != "",
					Revision: strings.TrimSpace(evidence.SubjectRevision), Trusted: evidence.Trusted, Source: evidence.Source})
			}
		}
		for _, evidence := range input.Checks {
			if evidence.ProcessID == report.ProcessID && activeSpec(evidence.SpecID) && evidence.Required && evidence.Passed && evidence.TestEvidence {
				carrier, specSatisfied = true, true
				revisions = append(revisions, CarrierRevisionFact{Known: strings.TrimSpace(evidence.SubjectRevision) != "",
					Revision: strings.TrimSpace(evidence.SubjectRevision), Trusted: evidence.Trusted, Source: evidence.Source})
			}
		}
		report.CarrierRevision = aggregateCarrierRevisions(revisions)
		if carrier {
			report.Satisfied = append(report.Satisfied, "verification evidence")
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

func (r ProcessEvidenceReport) Summary() string {
	return fmt.Sprintf("%s class=%s explicit=%t required=%s satisfied=%s missing=%s", r.ProcessID, r.ExecutionClass, r.ExplicitClass,
		strings.Join(r.Required, ","), strings.Join(r.Satisfied, ","), strings.Join(r.Missing, ","))
}
