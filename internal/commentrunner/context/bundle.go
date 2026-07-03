package context

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
)

const (
	DefaultMaxArtifactBodyBytes = 4096
	DefaultMaxPromptBytes       = 12288
	DefaultMaxStdoutBytes       = 8192
	DefaultMaxStderrBytes       = 8192
	DefaultMaxHistoryItems      = 12
)

type RunnerMetadata struct {
	ProcessID       string `json:"process_id,omitempty"`
	Agent           string `json:"agent,omitempty"`
	Repo            string `json:"repo,omitempty"`
	IssueNumber     int    `json:"issue_number,omitempty"`
	TriggerComment  string `json:"trigger_comment,omitempty"`
	SelectedCommand string `json:"selected_command,omitempty"`
}

type ArtifactSnapshot struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Issue      int    `json:"issue,omitempty"`
	URL        string `json:"url,omitempty"`
	APIURL     string `json:"api_url,omitempty"`
	Body       string `json:"body,omitempty"`
	BodySHA256 string `json:"body_sha256,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Source     string `json:"source"`
}

type CommandCandidate struct {
	Name        string   `json:"name"`
	Args        []string `json:"args,omitempty"`
	Description string   `json:"description,omitempty"`
	Authorized  bool     `json:"authorized"`
}

type TruncationInfo struct {
	MaxArtifacts int    `json:"max_artifacts,omitempty"`
	MaxBytes     int    `json:"max_bytes,omitempty"`
	Truncated    bool   `json:"truncated"`
	Reason       string `json:"reason,omitempty"`
}

type ContextBundle struct {
	Runner    RunnerMetadata     `json:"runner"`
	Command   CommandCandidate   `json:"command"`
	Artifacts []ArtifactSnapshot `json:"artifacts"`
	Sources   []string           `json:"sources"`
	Limits    TruncationInfo     `json:"limits"`
	History   TruncationInfo     `json:"history"`
	Prompt    string             `json:"prompt,omitempty"`
}

func BuildContextBundle(opts BundleOptions) (ContextBundle, error) {
	if len(opts.Commands) != 1 {
		return ContextBundle{}, fmt.Errorf("exactly one authorized command candidate is required")
	}
	command := opts.Commands[0]
	if !command.Authorized {
		return ContextBundle{}, fmt.Errorf("command candidate %q is not authorized", command.Name)
	}

	bundle := ContextBundle{
		Runner: RunnerMetadata{
			ProcessID:       strings.TrimSpace(opts.Runner.ProcessID),
			Agent:           strings.TrimSpace(opts.Runner.Agent),
			Repo:            strings.TrimSpace(opts.Runner.Repo),
			IssueNumber:     opts.Runner.IssueNumber,
			TriggerComment:  strings.TrimSpace(opts.Runner.TriggerComment),
			SelectedCommand: strings.TrimSpace(command.Name),
		},
		Command: command,
		Limits:  TruncationInfo{MaxArtifacts: opts.MaxArtifacts, MaxBytes: opts.MaxArtifactBytes},
		History: TruncationInfo{Truncated: len(opts.History) > 0, Reason: "history is intentionally excluded from default prompt input"},
	}

	remaining := opts.MaxArtifacts
	if remaining <= 0 {
		remaining = DefaultMaxHistoryItems
	}
	maxBytes := opts.MaxArtifactBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxArtifactBodyBytes
	}
	for _, artifact := range opts.Artifacts {
		if len(bundle.Artifacts) >= remaining {
			bundle.Limits.Truncated = true
			bundle.Limits.Reason = "artifact limit reached"
			break
		}
		snap := snapshotArtifact(artifact, maxBytes)
		bundle.Artifacts = append(bundle.Artifacts, snap)
		bundle.Sources = append(bundle.Sources, snap.Source)
		if snap.Truncated {
			bundle.Limits.Truncated = true
			if bundle.Limits.Reason == "" {
				bundle.Limits.Reason = "artifact body truncated to fit size budget"
			}
		}
	}
	return bundle, nil
}

type BundleOptions struct {
	Runner           RunnerMetadata
	Commands         []CommandCandidate
	Artifacts        []model.Artifact
	History          []model.Artifact
	MaxArtifacts     int
	MaxArtifactBytes int
}

func snapshotArtifact(artifact model.Artifact, maxBytes int) ArtifactSnapshot {
	tc := artifact.Comment
	body := strings.TrimSpace(tc.Body)
	truncated := false
	if maxBytes > 0 && len(body) > maxBytes {
		body = body[:maxBytes]
		truncated = true
	}
	sum := sha256.Sum256([]byte(tc.Body))
	return ArtifactSnapshot{
		ID:         tc.ID,
		Type:       tc.Type,
		Issue:      artifact.Issue,
		URL:        artifact.URL,
		APIURL:     artifact.APIURL,
		Body:       body,
		BodySHA256: hex.EncodeToString(sum[:]),
		Truncated:  truncated,
		Source:     sourceLabel(tc.Type),
	}
}

func sourceLabel(commentType string) string {
	switch strings.ToUpper(strings.TrimSpace(commentType)) {
	case "SPEC":
		return "spec"
	case "TASK":
		return "task"
	case "QUESTION":
		return "question"
	case "PROCESS":
		return "process"
	case "REVIEW":
		return "review"
	case "VERIFY":
		return "verify"
	default:
		return "issue-activity"
	}
}
