// Package gates evaluates issue-spec workflow readiness from a provider-neutral
// snapshot. It deliberately contains no GitHub, server, or command dependencies:
// callers collect facts, while this package owns readiness policy.
package gates

import (
	"fmt"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/model"
)

// Target is the workflow gate a caller wants to evaluate.
type Target string

const (
	TargetProposal  Target = "proposal"
	TargetDesign    Target = "design"
	TargetImplement Target = "implement"
	TargetFinal     Target = "final"
	TargetArchive   Target = "archive"
)

// Mode controls whether a report is a point-in-time forecast or an
// authoritative fail-closed decision.
type Mode string

const (
	ModeForecast      Mode = "forecast"
	ModeAuthoritative Mode = "authoritative"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Freshness describes the source of a diagnostic. Local facts are derived from
// the supplied artifact snapshot, point-in-time facts were observed from a
// remote provider, and unknown facts were required but not collected.
type Freshness string

const (
	FreshnessLocal       Freshness = "local"
	FreshnessPointInTime Freshness = "point-in-time"
	FreshnessUnknown     Freshness = "unknown"
)

// ArtifactRef identifies the artifact affected by a diagnostic without tying
// the evaluator to a hosting provider.
type ArtifactRef struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Remediation is intentionally a command family plus arguments rather than a
// shell command. Consumers can render it safely for their own environment.
type Remediation struct {
	CommandFamily string   `json:"command_family"`
	Arguments     []string `json:"arguments,omitempty"`
}

// Diagnostic is the stable machine-readable unit shared by status, preflight,
// and final verification compatibility projections.
type Diagnostic struct {
	Code        string      `json:"code"`
	Gate        Target      `json:"gate"`
	Severity    Severity    `json:"severity"`
	Blocking    bool        `json:"blocking"`
	Message     string      `json:"message"`
	Artifact    ArtifactRef `json:"artifact,omitempty"`
	Current     string      `json:"current,omitempty"`
	Expected    string      `json:"expected,omitempty"`
	Remediation Remediation `json:"remediation"`
	Freshness   Freshness   `json:"freshness"`
	ObservedAt  *time.Time  `json:"observed_at,omitempty"`
}

// Fact is a provider-neutral assertion collected outside this package. A
// required unknown fact blocks both forecast and authoritative readiness; the
// report mode explains whether that blocker is a forecast gap or a final
// fail-closed result.
type Fact struct {
	Required   bool       `json:"required"`
	Known      bool       `json:"known"`
	Passed     bool       `json:"passed"`
	Current    string     `json:"current,omitempty"`
	Expected   string     `json:"expected,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// ProcessEvidenceFact is the collector-owned projection of PR/evidence state
// for one PROCESS. Later execution-class policy can select which of these
// provider-neutral facts is required without changing the evaluator boundary.
type ProcessEvidenceFact struct {
	ProcessID  string `json:"process_id"`
	ProcessURL string `json:"process_url,omitempty"`
	PRLink     Fact   `json:"pr_link"`
	Evidence   Fact   `json:"evidence"`
}

// RemoteFacts are collected by command/provider adapters. The evaluator does
// not know how checks, findings, or external evidence were obtained.
type RemoteFacts struct {
	PRChecks         Fact                  `json:"pr_checks"`
	ReviewFindings   Fact                  `json:"review_findings"`
	ProviderEvidence Fact                  `json:"provider_evidence"`
	DurableSpec      Fact                  `json:"durable_spec"`
	Processes        []ProcessEvidenceFact `json:"processes,omitempty"`
}

// WorkflowFacts describe local workflow-schema resolution.
type WorkflowFacts struct {
	Required bool     `json:"required"`
	Known    bool     `json:"known"`
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
}

// CanonicalFacts lets callers reuse diagnostics from an existing artifact
// collection pass. When Observed is false Evaluate recomputes them.
type CanonicalFacts struct {
	Observed    bool                        `json:"observed"`
	Diagnostics []model.CanonicalDiagnostic `json:"diagnostics,omitempty"`
}

// TraceabilityFacts lets callers reuse a traceability report from the same
// snapshot. When Observed is false Evaluate recomputes it.
type TraceabilityFacts struct {
	Observed bool               `json:"observed"`
	Report   model.VerifyReport `json:"report"`
}

// Snapshot is the complete provider-neutral input to Evaluate. Callers may
// supply canonical and traceability results from an existing collection pass;
// when Observed is false the evaluator computes them from Artifacts.
type Snapshot struct {
	Target Target `json:"target"`
	Mode   Mode   `json:"mode"`

	Artifacts []model.Artifact `json:"artifacts"`

	Canonical       CanonicalFacts         `json:"canonical"`
	Traceability    TraceabilityFacts      `json:"traceability"`
	Workflow        WorkflowFacts          `json:"workflow"`
	Remote          RemoteFacts            `json:"remote"`
	ProcessEvidence []ProcessEvidenceInput `json:"process_evidence,omitempty"`
}

// Report is the single readiness result consumed by status, preflight, and
// final verify. Ready is false whenever any diagnostic is blocking.
type Report struct {
	Ready       bool                    `json:"ready"`
	Target      Target                  `json:"target"`
	Mode        Mode                    `json:"mode"`
	PointInTime bool                    `json:"point_in_time"`
	Diagnostics []Diagnostic            `json:"diagnostics,omitempty"`
	Processes   []ProcessEvidenceReport `json:"processes,omitempty"`
}

func (t Target) validate() error {
	switch t {
	case TargetProposal, TargetDesign, TargetImplement, TargetFinal, TargetArchive:
		return nil
	default:
		return fmt.Errorf("unsupported gate target %q", t)
	}
}

func (m Mode) validate() error {
	switch m {
	case ModeForecast, ModeAuthoritative:
		return nil
	default:
		return fmt.Errorf("unsupported gate mode %q", m)
	}
}

func artifactRef(artifact model.Artifact) ArtifactRef {
	return ArtifactRef{Type: artifact.Comment.Type, ID: artifact.Comment.ID, URL: artifact.URL}
}

func expectedOr(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
