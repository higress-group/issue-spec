package assignment

import "encoding/json"

const (
	AssignmentSchemaVersion = "issue-spec.assignment/v1"
	ReceiptSchemaVersion    = "issue-spec.receipt/v1"

	DesignReadModeCompleteIssueBody       = "complete-issue-body"
	DesignConflictPolicyAuthoritativeStop = "design-authoritative-stop"
)

type Role string

const (
	RoleImplementation Role = "implementation"
	RoleReview         Role = "review"
	RoleVerification   Role = "verification"
)

// Assignment is the portable, digest-covered contract. Machine-local delivery
// details belong in Packet.Delivery and are intentionally absent here.
type Assignment struct {
	SchemaVersion       string                 `json:"schema_version"`
	ID                  string                 `json:"assignment_id"`
	Role                Role                   `json:"role"`
	Repository          string                 `json:"repository"`
	Issue               int64                  `json:"issue"`
	ProcessID           string                 `json:"process_id"`
	BaseRevision        string                 `json:"base_revision,omitempty"`
	SubjectRevision     string                 `json:"subject_revision,omitempty"`
	Scenarios           []ScenarioRef          `json:"scenarios"`
	Dependencies        []string               `json:"dependencies,omitempty"`
	Handoff             string                 `json:"handoff,omitempty"`
	DesignContext       *DesignContext         `json:"design_context,omitempty"`
	Policy              Policy                 `json:"policy"`
	ResultSchemaVersion string                 `json:"result_schema_version"`
	Implementation      *ImplementationPayload `json:"implementation,omitempty"`
	Review              *ReviewPayload         `json:"review,omitempty"`
	Verification        *VerificationPayload   `json:"verification,omitempty"`
}

// DesignContext is the portable, digest-covered projection of the Design
// decisions applicable to one implementation or review assignment. Its text
// and list order are authoritative and must not be normalized or reinterpreted.
type DesignContext struct {
	SourceURL               string   `json:"source_url"`
	ReadMode                string   `json:"read_mode"`
	Invariant               string   `json:"invariant"`
	ApplicableDecisions     []string `json:"applicable_decisions"`
	ImplementationDirection string   `json:"implementation_direction"`
	MustPreserve            []string `json:"must_preserve"`
	MustNot                 []string `json:"must_not"`
	MinimumVerification     []string `json:"minimum_verification"`
	ConflictPolicy          string   `json:"conflict_policy"`
}

type ScenarioRef struct {
	SpecID   string `json:"spec_id"`
	Scenario string `json:"scenario"`
}

type Policy struct {
	RequireExactRevision bool `json:"require_exact_revision"`
	// MaxResultItems is retained only so stored version-1 assignments keep
	// their original canonical digest. New assignments omit it and receipt
	// acceptance does not enforce it.
	MaxResultItems int `json:"max_result_items,omitempty"`
}

type ImplementationPayload struct {
	Objective         string            `json:"objective"`
	Branch            string            `json:"branch"`
	WriteOwnership    []string          `json:"write_ownership"`
	SharedTouchpoints []string          `json:"shared_touchpoints,omitempty"`
	Commit            CommitPolicy      `json:"commit_policy"`
	Generators        []GeneratorPolicy `json:"generators,omitempty"`
	FocusedTests      []TestSelector    `json:"focused_tests,omitempty"`
}

type CommitPolicy struct {
	RequireSingleCommit bool `json:"require_single_commit"`
	RequireDCO          bool `json:"require_dco"`
}

type GeneratorPolicy struct {
	Name                string   `json:"name"`
	Command             string   `json:"command"`
	RequiredOutputs     []string `json:"required_outputs,omitempty"`
	RequiredOutputGlobs []string `json:"required_output_globs,omitempty"`
}

type TestSelector struct {
	ID              string           `json:"id"`
	Command         string           `json:"command"`
	RevisionBinding *RevisionBinding `json:"revision_binding,omitempty"`
}

type RevisionBindingSource string

const RevisionBindingSourceResultRevision RevisionBindingSource = "result-revision"

const RevisionBindingSourceSubjectRevision RevisionBindingSource = "subject-revision"

type RevisionBindingArgument string

const RevisionBindingArgumentSubject RevisionBindingArgument = "--subject"

// RevisionBinding declares one of the closed version-1 revision authorities.
// It is part of selector identity and is therefore covered by assignment and
// receipt digests.
type RevisionBinding struct {
	Source   RevisionBindingSource   `json:"source"`
	Argument RevisionBindingArgument `json:"argument"`
}

// ResolvedTestIdentity pairs the digest-covered declarative selector with the
// exact authoritative revision and executed command identity. Managed
// completion can compare each field without interpreting shell text.
type ResolvedTestIdentity struct {
	AssignedSelector TestSelector
	ResolvedRevision string
	Command          string
}

type CheckSelector struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

type ReviewPayload struct {
	SnapshotRevision string              `json:"snapshot_revision"`
	DiffBaseRevision string              `json:"diff_base_revision"`
	Authors          []string            `json:"authors"`
	Scope            []string            `json:"scope"`
	KnownTests       []KnownTestEvidence `json:"known_tests,omitempty"`
	RequiredTests    []TestSelector      `json:"required_tests,omitempty"`
}

type KnownTestEvidence struct {
	ID      string      `json:"id"`
	Command string      `json:"command"`
	Outcome TestOutcome `json:"outcome"`
}

type VerificationPayload struct {
	SubjectRevision string            `json:"subject_revision"`
	Guidance        *VerifierGuidance `json:"guidance,omitempty"`
	RequiredTests   []TestSelector    `json:"required_tests,omitempty"`
	RequiredChecks  []CheckSelector   `json:"required_checks,omitempty"`
}

// VerifierGuidance is the bounded, declarative project context sealed into a
// verification assignment. Context and rules.verify remain JSON data: core
// validation preserves them but never interprets them as executable policy.
type VerifierGuidance struct {
	Context      json.RawMessage       `json:"context,omitempty"`
	RulesVerify  json.RawMessage       `json:"rules_verify,omitempty"`
	Instructions []VerifierInstruction `json:"instructions,omitempty"`
}

type VerifierInstruction struct {
	ArtifactID string `json:"artifact_id"`
	Text       string `json:"text"`
}

// RequiredSelectors is the stable mechanical extension point for project and
// built-in verification requirements. Test identity includes the exact command
// and optional revision binding; provider-check identity is the exact
// provider/name pair.
type RequiredSelectors struct {
	Tests  []TestSelector  `json:"tests,omitempty"`
	Checks []CheckSelector `json:"checks,omitempty"`
}

// VerifierPacket projects resolved project guidance and required selectors into
// the portable assignment layer. Scenarios and the exact subject revision stay
// on Assignment so the packet cannot broaden either scope.
type VerifierPacket struct {
	Guidance       *VerifierGuidance `json:"guidance,omitempty"`
	RequiredTests  []TestSelector    `json:"required_tests,omitempty"`
	RequiredChecks []CheckSelector   `json:"required_checks,omitempty"`
}

// DeliveryMetadata is deliberately outside Assignment and its digest. It may
// contain machine-local paths needed to deliver a packet to a role worker.
type DeliveryMetadata struct {
	WorktreePath string `json:"worktree_path,omitempty"`
}

type Packet struct {
	Assignment       Assignment        `json:"assignment"`
	AssignmentDigest string            `json:"assignment_digest"`
	Generation       uint64            `json:"generation"`
	Delivery         *DeliveryMetadata `json:"delivery,omitempty"`
}

// ProcessInput is the minimal structured assignment input that may be carried
// in a PROCESS comment. Existing PROCESS fields remain authoritative for scope,
// ownership, dependencies, handoff, and coverage. This block only supplies
// fields that must never be guessed from prose.
type ProcessInput struct {
	Objective         string            `json:"objective,omitempty"`
	DesignContext     *DesignContext    `json:"design_context,omitempty"`
	ScenarioSelectors []ScenarioRef     `json:"scenario_selectors,omitempty"`
	RequiredTests     []TestSelector    `json:"required_tests,omitempty"`
	RequiredChecks    []CheckSelector   `json:"required_checks,omitempty"`
	Generators        []GeneratorPolicy `json:"generators,omitempty"`
	CommitPolicy      *CommitPolicy     `json:"commit_policy,omitempty"`
}

type Assurance string

const (
	AssuranceSelfReported    Assurance = "self-reported"
	AssuranceProviderOwned   Assurance = "provider-owned"
	AssuranceRuntimeAttested Assurance = "runtime-attested"
)

type ProvenanceRoute string

const (
	RouteRoleOwned        ProvenanceRoute = "role-owned"
	RouteUnverifiedImport ProvenanceRoute = "unverified-import"
)

type Provenance struct {
	Route     ProvenanceRoute `json:"route"`
	Assurance Assurance       `json:"assurance"`
	Writer    string          `json:"writer"`
	Subject   string          `json:"subject"`
	Source    string          `json:"source"`
}

type TestOutcome string

const (
	TestPassed  TestOutcome = "passed"
	TestFailed  TestOutcome = "failed"
	TestSkipped TestOutcome = "skipped"
)

type TestResult struct {
	ID               string        `json:"id"`
	Command          string        `json:"command"`
	AssignedSelector *TestSelector `json:"assigned_selector,omitempty"`
	ResolvedRevision string        `json:"resolved_revision,omitempty"`
	Outcome          TestOutcome   `json:"outcome"`
	Assurance        Assurance     `json:"assurance"`
}

// Receipt is revision-bound role evidence. ReceiptDigest is computed from the
// canonical receipt with that field omitted, avoiding a self-referential hash.
type Receipt struct {
	SchemaVersion        string                `json:"schema_version"`
	ID                   string                `json:"receipt_id"`
	ReceiptDigest        string                `json:"receipt_digest,omitempty"`
	AssignmentID         string                `json:"assignment_id"`
	AssignmentDigest     string                `json:"assignment_digest"`
	AssignmentGeneration uint64                `json:"assignment_generation"`
	Role                 Role                  `json:"role"`
	ResultSchemaVersion  string                `json:"result_schema_version"`
	BaseRevision         string                `json:"base_revision,omitempty"`
	ResultRevision       string                `json:"result_revision,omitempty"`
	SubjectRevision      string                `json:"subject_revision,omitempty"`
	Tests                []TestResult          `json:"tests,omitempty"`
	Provenance           Provenance            `json:"provenance"`
	Implementation       *ImplementationResult `json:"implementation,omitempty"`
	Review               *ReviewResult         `json:"review,omitempty"`
	Verification         *VerificationResult   `json:"verification,omitempty"`
}

type ImplementationResult struct {
	ChangedPaths   []string `json:"changed_paths"`
	Decisions      []string `json:"decisions,omitempty"`
	Risks          []string `json:"risks,omitempty"`
	RationaleDraft string   `json:"rationale_draft,omitempty"`
}

type ReviewVerdict string

const (
	ReviewApprove          ReviewVerdict = "approve"
	ReviewChangesRequested ReviewVerdict = "changes-requested"
)

type ReviewResult struct {
	Verdict  ReviewVerdict `json:"verdict"`
	Findings []Finding     `json:"findings,omitempty"`
}

type Finding struct {
	ID             string `json:"id"`
	SpecID         string `json:"spec_id"`
	OwnerProcessID string `json:"owner_process_id"`
	Path           string `json:"path"`
	Side           string `json:"side"`
	Line           int    `json:"line"`
	Severity       string `json:"severity"`
	Message        string `json:"message"`
}

type VerificationResult struct {
	Summary        string          `json:"summary,omitempty"`
	CheckSelectors []CheckSelector `json:"check_selectors,omitempty"`
}
