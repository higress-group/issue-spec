package changes

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

type Stage string

const (
	StageUnknown   Stage = "unknown"
	StageProposal  Stage = "proposal"
	StageDesign    Stage = "design"
	StageImplement Stage = "implement"
)

type Lifecycle string

const (
	LifecycleActive    Lifecycle = "active"
	LifecycleBlocked   Lifecycle = "blocked"
	LifecycleCompleted Lifecycle = "completed"
	LifecycleClosed    Lifecycle = "closed"
)

const (
	AnomalyDuplicateArtifactType       = "duplicate_artifact_type"
	AnomalyMarkerLabelMismatch         = "marker_label_mismatch"
	AnomalyMissingRequiredLinks        = "missing_required_links"
	AnomalyUnsupportedMarkerVersion    = "unsupported_marker_version"
	AnomalyImplementMissingPredecessor = "implement_missing_predecessor"
	AnomalyOrphanTypedArtifact         = "orphan_typed_artifact"
	AnomalyMalformedIssueMarker        = "malformed_issue_marker"
)

type Repository struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
}

type Artifact struct {
	ID            uuid.UUID `json:"id"`
	Number        int64     `json:"number"`
	Title         string    `json:"title"`
	State         string    `json:"state"`
	URL           string    `json:"url"`
	MarkerVersion string    `json:"marker_version"`
	UpdatedAt     time.Time `json:"updated_at"`
	Valid         bool      `json:"valid"`
}

type ArtifactSlots struct {
	Proposal  *Artifact `json:"proposal,omitempty"`
	Design    *Artifact `json:"design,omitempty"`
	Implement *Artifact `json:"implement,omitempty"`
}

type Progress struct {
	Total      int `json:"total"`
	Completed  int `json:"completed"`
	InProgress int `json:"in_progress"`
	Blocked    int `json:"blocked"`
	Pending    int `json:"pending"`
}

type ChangeCard struct {
	Repository   Repository    `json:"repository"`
	ChangeKey    string        `json:"change_key"`
	Title        string        `json:"title"`
	CurrentStage Stage         `json:"current_stage"`
	Lifecycle    Lifecycle     `json:"lifecycle"`
	Artifacts    ArtifactSlots `json:"artifacts"`
	Tasks        Progress      `json:"tasks"`
	Processes    Progress      `json:"processes"`
	Anomalies    []string      `json:"anomalies"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

type BoardCounts struct {
	Total     int `json:"total"`
	Active    int `json:"active"`
	Blocked   int `json:"blocked"`
	Completed int `json:"completed"`
	Closed    int `json:"closed"`
	Proposal  int `json:"proposal"`
	Design    int `json:"design"`
	Implement int `json:"implement"`
	Unknown   int `json:"unknown"`
}

type DiagnosticCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type BoardPage struct {
	Cards        []ChangeCard      `json:"cards"`
	Page         int               `json:"page"`
	PerPage      int               `json:"per_page"`
	Total        int               `json:"total"`
	Counts       BoardCounts       `json:"counts"`
	Diagnostics  []DiagnosticCount `json:"diagnostics"`
	Validator    string            `json:"-"`
	LastModified time.Time         `json:"-"`
}

type ListOptions struct {
	Stage     Stage     `json:"stage,omitempty"`
	Lifecycle Lifecycle `json:"lifecycle,omitempty"`
	Anomaly   string    `json:"anomaly,omitempty"`
	Page      int       `json:"page,omitempty"`
	PerPage   int       `json:"per_page,omitempty"`
}

func NormalizeChangeKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
