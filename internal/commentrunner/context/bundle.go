package contextbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/higress-group/issue-spec/internal/model"
)

const BundleSchemaVersion = 1

const (
	SourceAuthorizedCommand = "authorized_command"
	SourceRunnerMetadata    = "runner_metadata"
	SourceIssueSpecArtifact = "issue_spec_artifact"

	TrustRunnerProduced = "runner_produced"
	TrustUntrustedData  = "untrusted_artifact_data"
)

type CommandVerb string

const (
	CommandNew    CommandVerb = "new"
	CommandResume CommandVerb = "resume"
	CommandCancel CommandVerb = "cancel"
)

type Bounds struct {
	MaxCommandPromptBytes int `json:"max_command_prompt_bytes"`
	MaxArtifactBytes      int `json:"max_artifact_bytes"`
	MaxArtifacts          int `json:"max_artifacts"`
}

func DefaultBounds() Bounds {
	return Bounds{
		MaxCommandPromptBytes: 16 * 1024,
		MaxArtifactBytes:      8 * 1024,
		MaxArtifacts:          64,
	}
}

type BuildOptions struct {
	Command         CommandCandidate
	Runner          RunnerMetadata
	Artifacts       []model.Artifact
	Bounds          Bounds
	RedactionValues []string
}

type Bundle struct {
	SchemaVersion int                `json:"schema_version"`
	BundleSHA256  string             `json:"bundle_sha256"`
	Command       CommandCandidate   `json:"command"`
	Runner        RunnerMetadata     `json:"runner"`
	Artifacts     []BundleArtifact   `json:"artifacts"`
	Bounds        Bounds             `json:"bounds"`
	Truncations   []TruncationRecord `json:"truncations,omitempty"`
	Redactions    []RedactionRecord  `json:"redactions,omitempty"`
}

type CommandCandidate struct {
	SourceLabel             string      `json:"source_label"`
	Trust                   string      `json:"trust"`
	Authorized              bool        `json:"authorized"`
	Verb                    CommandVerb `json:"verb"`
	Repo                    string      `json:"repo"`
	Issue                   int         `json:"issue"`
	TriggerCommentID        int64       `json:"trigger_comment_id"`
	TriggerCommentURL       string      `json:"trigger_comment_url,omitempty"`
	Commenter               string      `json:"commenter"`
	FirstObservedUpdatedAt  string      `json:"first_observed_updated_at,omitempty"`
	FirstObservedBodySHA256 string      `json:"first_observed_body_sha256,omitempty"`
	IdempotencyKey          string      `json:"idempotency_key,omitempty"`
	PublicSessionID         string      `json:"public_session_id,omitempty"`
	Prompt                  string      `json:"prompt"`
	PromptSHA256            string      `json:"prompt_sha256"`
	IncludedPromptSHA256    string      `json:"included_prompt_sha256"`
	PromptBytes             int         `json:"prompt_bytes"`
	IncludedPromptBytes     int         `json:"included_prompt_bytes"`
	PromptLimitBytes        int         `json:"prompt_limit_bytes"`
	PromptTruncated         bool        `json:"prompt_truncated,omitempty"`
	PromptRedacted          bool        `json:"prompt_redacted,omitempty"`
	TurnCorrelationSentinel string      `json:"turn_correlation_sentinel,omitempty"`
}

type RunnerMetadata struct {
	SourceLabel      string   `json:"source_label"`
	Trust            string   `json:"trust"`
	JobID            string   `json:"job_id"`
	PublicSessionID  string   `json:"public_session_id,omitempty"`
	Repo             string   `json:"repo"`
	Issue            int      `json:"issue"`
	TriggerCommentID int64    `json:"trigger_comment_id"`
	WorkspacePath    string   `json:"workspace_path,omitempty"`
	CloneURL         string   `json:"clone_url,omitempty"`
	Branch           string   `json:"branch,omitempty"`
	Ref              string   `json:"ref,omitempty"`
	AgentKind        string   `json:"agent_kind,omitempty"`
	Model            string   `json:"model,omitempty"`
	IssueSpecBinary  string   `json:"issue_spec_binary,omitempty"`
	Constraints      []string `json:"constraints,omitempty"`
}

type BundleArtifact struct {
	SourceLabel       string `json:"source_label"`
	Trust             string `json:"trust"`
	Issue             int    `json:"issue"`
	CommentID         int64  `json:"comment_id,omitempty"`
	URL               string `json:"url,omitempty"`
	APIURL            string `json:"api_url,omitempty"`
	Type              string `json:"type"`
	ID                string `json:"id"`
	Status            string `json:"status,omitempty"`
	Scope             string `json:"scope,omitempty"`
	Content           string `json:"content"`
	ContentSHA256     string `json:"content_sha256"`
	IncludedSHA256    string `json:"included_sha256"`
	ContentBytes      int    `json:"content_bytes"`
	IncludedBytes     int    `json:"included_bytes"`
	ContentLimitBytes int    `json:"content_limit_bytes"`
	Truncated         bool   `json:"truncated,omitempty"`
	Redacted          bool   `json:"redacted,omitempty"`
}

type TruncationRecord struct {
	Target        string `json:"target"`
	OriginalBytes int    `json:"original_bytes"`
	IncludedBytes int    `json:"included_bytes"`
	LimitBytes    int    `json:"limit_bytes"`
	Reason        string `json:"reason"`
}

type RedactionRecord struct {
	Target string `json:"target"`
	Count  int    `json:"count"`
}

func BuildBundle(opts BuildOptions) (Bundle, error) {
	bounds := normalizeBounds(opts.Bounds)
	command, truncations, redactions, err := normalizeCommand(opts.Command, bounds, opts.RedactionValues)
	if err != nil {
		return Bundle{}, err
	}
	runner, runnerRedactions := normalizeRunner(opts.Runner, command, opts.RedactionValues)
	redactions = append(redactions, runnerRedactions...)

	artifacts, artifactTruncations, artifactRedactions, err := normalizeArtifacts(opts.Artifacts, bounds, opts.RedactionValues)
	if err != nil {
		return Bundle{}, err
	}
	truncations = append(truncations, artifactTruncations...)
	redactions = append(redactions, artifactRedactions...)

	if len(artifacts) > bounds.MaxArtifacts {
		original := len(artifacts)
		artifacts = artifacts[:bounds.MaxArtifacts]
		truncations = append(truncations, TruncationRecord{
			Target:        "artifacts",
			OriginalBytes: original,
			IncludedBytes: len(artifacts),
			LimitBytes:    bounds.MaxArtifacts,
			Reason:        "artifact_count_limit",
		})
	}

	bundle := Bundle{
		SchemaVersion: BundleSchemaVersion,
		Command:       command,
		Runner:        runner,
		Artifacts:     artifacts,
		Bounds:        bounds,
		Truncations:   truncations,
		Redactions:    redactions,
	}
	bundle.BundleSHA256 = hashBundle(bundle)
	return bundle, nil
}

func (b Bundle) JSON() ([]byte, error) {
	return json.MarshalIndent(b, "", "  ")
}

func normalizeCommand(command CommandCandidate, bounds Bounds, redactionValues []string) (CommandCandidate, []TruncationRecord, []RedactionRecord, error) {
	command.SourceLabel = SourceAuthorizedCommand
	command.Trust = TrustRunnerProduced
	if !command.Authorized {
		return CommandCandidate{}, nil, nil, fmt.Errorf("command candidate must be authorized before building a context bundle")
	}
	switch command.Verb {
	case CommandNew:
		if strings.TrimSpace(command.Prompt) == "" {
			return CommandCandidate{}, nil, nil, fmt.Errorf("/new command prompt is required")
		}
	case CommandResume:
		if strings.TrimSpace(command.PublicSessionID) == "" {
			return CommandCandidate{}, nil, nil, fmt.Errorf("/resume command requires a public session id")
		}
		if strings.TrimSpace(command.Prompt) == "" {
			return CommandCandidate{}, nil, nil, fmt.Errorf("/resume command prompt is required")
		}
	case CommandCancel:
		return CommandCandidate{}, nil, nil, fmt.Errorf("/cancel does not create a coordinator context bundle")
	default:
		return CommandCandidate{}, nil, nil, fmt.Errorf("unsupported command verb %q", command.Verb)
	}
	if strings.TrimSpace(command.Repo) == "" {
		return CommandCandidate{}, nil, nil, fmt.Errorf("command repo is required")
	}
	if command.Issue <= 0 {
		return CommandCandidate{}, nil, nil, fmt.Errorf("command issue is required")
	}
	if command.TriggerCommentID <= 0 {
		return CommandCandidate{}, nil, nil, fmt.Errorf("command trigger comment id is required")
	}
	if strings.TrimSpace(command.Commenter) == "" {
		return CommandCandidate{}, nil, nil, fmt.Errorf("command commenter is required")
	}

	originalPrompt := command.Prompt
	command.PromptSHA256 = sha256String(originalPrompt)
	command.PromptBytes = len([]byte(originalPrompt))
	if strings.TrimSpace(command.FirstObservedBodySHA256) == "" {
		command.FirstObservedBodySHA256 = command.PromptSHA256
	}
	redactedPrompt, count := redactString(originalPrompt, redactionValues)
	if count > 0 {
		command.PromptRedacted = true
	}
	includedPrompt, truncated := truncateBytes(redactedPrompt, bounds.MaxCommandPromptBytes)
	command.Prompt = includedPrompt
	command.IncludedPromptSHA256 = sha256String(includedPrompt)
	command.IncludedPromptBytes = len([]byte(includedPrompt))
	command.PromptLimitBytes = bounds.MaxCommandPromptBytes
	command.PromptTruncated = truncated

	var truncations []TruncationRecord
	if truncated {
		truncations = append(truncations, TruncationRecord{
			Target:        "command.prompt",
			OriginalBytes: len([]byte(redactedPrompt)),
			IncludedBytes: command.IncludedPromptBytes,
			LimitBytes:    bounds.MaxCommandPromptBytes,
			Reason:        "command_prompt_limit",
		})
	}
	var redactions []RedactionRecord
	if count > 0 {
		redactions = append(redactions, RedactionRecord{Target: "command.prompt", Count: count})
	}
	return command, truncations, redactions, nil
}

func normalizeRunner(runner RunnerMetadata, command CommandCandidate, redactionValues []string) (RunnerMetadata, []RedactionRecord) {
	runner.SourceLabel = SourceRunnerMetadata
	runner.Trust = TrustRunnerProduced
	if runner.Repo == "" {
		runner.Repo = command.Repo
	}
	if runner.Issue == 0 {
		runner.Issue = command.Issue
	}
	if runner.TriggerCommentID == 0 {
		runner.TriggerCommentID = command.TriggerCommentID
	}
	if runner.PublicSessionID == "" {
		runner.PublicSessionID = command.PublicSessionID
	}
	var redactions []RedactionRecord
	for i, constraint := range runner.Constraints {
		redacted, count := redactString(constraint, redactionValues)
		runner.Constraints[i] = redacted
		if count > 0 {
			redactions = append(redactions, RedactionRecord{Target: fmt.Sprintf("runner.constraints[%d]", i), Count: count})
		}
	}
	return runner, redactions
}

func normalizeArtifacts(input []model.Artifact, bounds Bounds, redactionValues []string) ([]BundleArtifact, []TruncationRecord, []RedactionRecord, error) {
	artifacts := append([]model.Artifact{}, input...)
	sort.SliceStable(artifacts, func(i, j int) bool {
		return artifactLess(artifacts[i], artifacts[j])
	})

	out := make([]BundleArtifact, 0, len(artifacts))
	var truncations []TruncationRecord
	var redactions []RedactionRecord
	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.Type == "" || tc.ID == "" {
			return nil, nil, nil, fmt.Errorf("artifact on issue %d comment %d is not an issue-spec typed artifact", artifact.Issue, artifact.CommentID)
		}
		content := tc.Body
		if strings.TrimSpace(content) == "" {
			content = typedCommentMetadata(tc)
		}
		target := fmt.Sprintf("artifact.%s", tc.ID)
		originalBytes := len([]byte(content))
		contentHash := sha256String(content)
		redactedContent, redactionCount := redactString(content, redactionValues)
		included, truncated := truncateBytes(redactedContent, bounds.MaxArtifactBytes)
		item := BundleArtifact{
			SourceLabel:       SourceIssueSpecArtifact,
			Trust:             TrustUntrustedData,
			Issue:             artifact.Issue,
			CommentID:         artifact.CommentID,
			URL:               artifact.URL,
			APIURL:            artifact.APIURL,
			Type:              tc.Type,
			ID:                tc.ID,
			Status:            tc.Status,
			Scope:             tc.Scope,
			Content:           included,
			ContentSHA256:     contentHash,
			IncludedSHA256:    sha256String(included),
			ContentBytes:      originalBytes,
			IncludedBytes:     len([]byte(included)),
			ContentLimitBytes: bounds.MaxArtifactBytes,
			Truncated:         truncated,
			Redacted:          redactionCount > 0,
		}
		if truncated {
			truncations = append(truncations, TruncationRecord{
				Target:        target + ".content",
				OriginalBytes: len([]byte(redactedContent)),
				IncludedBytes: item.IncludedBytes,
				LimitBytes:    bounds.MaxArtifactBytes,
				Reason:        "artifact_content_limit",
			})
		}
		if redactionCount > 0 {
			redactions = append(redactions, RedactionRecord{Target: target + ".content", Count: redactionCount})
		}
		out = append(out, item)
	}
	return out, truncations, redactions, nil
}

func normalizeBounds(bounds Bounds) Bounds {
	defaults := DefaultBounds()
	if bounds.MaxCommandPromptBytes <= 0 {
		bounds.MaxCommandPromptBytes = defaults.MaxCommandPromptBytes
	}
	if bounds.MaxArtifactBytes <= 0 {
		bounds.MaxArtifactBytes = defaults.MaxArtifactBytes
	}
	if bounds.MaxArtifacts <= 0 {
		bounds.MaxArtifacts = defaults.MaxArtifacts
	}
	return bounds
}

func artifactLess(a, b model.Artifact) bool {
	ar, br := typeRank(a.Comment.Type), typeRank(b.Comment.Type)
	if ar != br {
		return ar < br
	}
	if a.Comment.ID != b.Comment.ID {
		return a.Comment.ID < b.Comment.ID
	}
	if a.Issue != b.Issue {
		return a.Issue < b.Issue
	}
	if a.CommentID != b.CommentID {
		return a.CommentID < b.CommentID
	}
	return a.URL < b.URL
}

func typeRank(commentType string) int {
	switch commentType {
	case "SPEC":
		return 0
	case "TASK":
		return 1
	case "PROCESS":
		return 2
	case "QUESTION":
		return 3
	case "REVIEW":
		return 4
	case "VERIFY":
		return 5
	default:
		return 99
	}
}

func typedCommentMetadata(tc model.TypedComment) string {
	var lines []string
	if tc.Agent != "" {
		lines = append(lines, "Agent: "+tc.Agent)
	}
	if tc.AgentSessionID != "" {
		lines = append(lines, "Agent Session ID: "+tc.AgentSessionID)
	}
	if tc.AgentSessionSource != "" {
		lines = append(lines, "Agent Session Source: "+tc.AgentSessionSource)
	}
	lines = append(lines,
		"Type: "+tc.Type,
		"ID: "+tc.ID,
	)
	if tc.Status != "" {
		lines = append(lines, "Status: "+tc.Status)
	}
	if tc.Scope != "" {
		lines = append(lines, "Scope: "+tc.Scope)
	}
	return strings.Join(lines, "\n")
}

func truncateBytes(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len([]byte(value)) <= maxBytes {
		return value, false
	}
	truncated := value[:maxBytes]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated, true
}

func redactString(value string, redactionValues []string) (string, int) {
	count := 0
	out := value
	for _, secret := range redactionValues {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		n := strings.Count(out, secret)
		if n == 0 {
			continue
		}
		out = strings.ReplaceAll(out, secret, "[REDACTED]")
		count += n
	}
	return out, count
}

func hashBundle(bundle Bundle) string {
	bundle.BundleSHA256 = ""
	data, _ := json.Marshal(bundle)
	return sha256Bytes(data)
}

func sha256String(value string) string {
	return sha256Bytes([]byte(value))
}

func sha256Bytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
