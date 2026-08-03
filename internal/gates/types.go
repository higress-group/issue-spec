// Package gates evaluates optional issue-spec planning gates from a
// provider-neutral snapshot. Merge readiness is owned by internal/mergecheck.
package gates

import (
	"fmt"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/relationships"
)

type Target string

const (
	TargetProposal  Target = "proposal"
	TargetDesign    Target = "design"
	TargetImplement Target = "implement"
)

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

type Freshness string

const (
	FreshnessLocal       Freshness = "local"
	FreshnessPointInTime Freshness = "point-in-time"
	FreshnessUnknown     Freshness = "unknown"
)

type ArtifactRef struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	URL  string `json:"url,omitempty"`
}

type Remediation struct {
	CommandFamily string   `json:"command_family"`
	Arguments     []string `json:"arguments,omitempty"`
}

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

type Fact struct {
	Required   bool       `json:"required"`
	Known      bool       `json:"known"`
	Passed     bool       `json:"passed"`
	Current    string     `json:"current,omitempty"`
	Expected   string     `json:"expected,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

type RemoteFacts struct {
	Workspace WorkspaceFacts `json:"workspace"`
}

type WorkspaceFacts struct {
	Observed bool `json:"observed"`
}

type WorkflowFacts struct {
	Required bool     `json:"required"`
	Known    bool     `json:"known"`
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
}

type CanonicalFacts struct {
	Observed    bool                        `json:"observed"`
	Diagnostics []model.CanonicalDiagnostic `json:"diagnostics,omitempty"`
}

type TraceabilityFacts struct {
	Observed bool               `json:"observed"`
	Report   model.VerifyReport `json:"report"`
}

type RelationshipFacts struct {
	Required bool                `json:"required,omitempty"`
	Observed bool                `json:"observed"`
	Index    relationships.Index `json:"index"`
	Error    string              `json:"error,omitempty"`
}

type Snapshot struct {
	Target        Target                 `json:"target"`
	Mode          Mode                   `json:"mode"`
	Artifacts     []model.Artifact       `json:"artifacts"`
	Answers       model.AnswerResolution `json:"answers"`
	Canonical     CanonicalFacts         `json:"canonical"`
	Traceability  TraceabilityFacts      `json:"traceability"`
	Relationships RelationshipFacts      `json:"relationships"`
	Workflow      WorkflowFacts          `json:"workflow"`
	Remote        RemoteFacts            `json:"remote"`
}

type Report struct {
	Ready       bool         `json:"ready"`
	Target      Target       `json:"target"`
	Mode        Mode         `json:"mode"`
	PointInTime bool         `json:"point_in_time"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func (t Target) validate() error {
	switch t {
	case TargetProposal, TargetDesign, TargetImplement:
		return nil
	default:
		return fmt.Errorf("unsupported planning gate target %q", t)
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
