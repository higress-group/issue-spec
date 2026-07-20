package durable

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/reconcile/filecas"
)

const (
	PlanVersion        = 1
	CompactVersion     = 1
	maxAffectedIDs     = 8
	maxBlockerMessages = 4
)

const (
	BlockWorkflowMode       = "workflow_mode_disabled"
	BlockWorkflowConfig     = "workflow_config_invalid"
	BlockSourceAmbiguous    = "source_spec_ambiguous"
	BlockSourceInvalid      = "source_spec_invalid"
	BlockUnsafeTargetPath   = "unsafe_target_path"
	BlockOperationCollision = "operation_collision"
	BlockTargetCollision    = "target_collision"
	BlockRenameCollision    = "rename_collision"
	BlockTargetFileInvalid  = "target_file_invalid"
	BlockTargetMissing      = "target_missing"
	BlockTargetAmbiguous    = "target_ambiguous"
	BlockTargetExists       = "target_exists"
	BlockProjectionInvalid  = "projection_invalid"
)

type WorkflowAuthority struct {
	ConfigPath   string `json:"config_path"`
	ConfigDigest string `json:"config_digest"`
	Mode         Mode   `json:"mode"`
}

type SourceAuthority struct {
	ID                    string `json:"id"`
	URL                   string `json:"url"`
	RepresentationVersion int64  `json:"representation_version"`
	RepresentationDigest  string `json:"representation_digest"`
	Intent                Intent `json:"intent"`
}

type PlannedOperation struct {
	ID                   string        `json:"id"`
	SourceSpecID         string        `json:"source_spec_id"`
	SourceSpecURL        string        `json:"source_spec_url"`
	Kind                 OperationKind `json:"kind"`
	Path                 string        `json:"path"`
	CurrentRequirement   string        `json:"current_requirement,omitempty"`
	NewRequirement       string        `json:"new_requirement,omitempty"`
	BlockPreimageDigest  string        `json:"block_preimage_digest,omitempty"`
	BlockPostimageDigest string        `json:"block_postimage_digest,omitempty"`
}

type Finding struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	OperationID  string `json:"operation_id,omitempty"`
	SourceSpecID string `json:"source_spec_id,omitempty"`
	Path         string `json:"path,omitempty"`
	Requirement  string `json:"requirement,omitempty"`
}

type Blocker struct {
	Code           string   `json:"code"`
	Messages       []string `json:"messages"`
	AffectedIDs    []string `json:"affected_ids,omitempty"`
	TruncatedCount int      `json:"truncated_count,omitempty"`
}

type Plan struct {
	Version          int                    `json:"version"`
	PlanDigest       string                 `json:"plan_digest"`
	Repository       string                 `json:"repository"`
	Proposal         int                    `json:"proposal"`
	BaselineRevision string                 `json:"baseline_revision"`
	Workflow         WorkflowAuthority      `json:"workflow"`
	Sources          []SourceAuthority      `json:"sources"`
	Operations       []PlannedOperation     `json:"operations"`
	Files            []filecas.FileMutation `json:"files"`
	Blockers         []Blocker              `json:"blockers,omitempty"`
	Findings         []Finding              `json:"findings,omitempty"`
}

type CompactBlocker struct {
	Code           string   `json:"code"`
	AffectedIDs    []string `json:"affected_ids,omitempty"`
	TruncatedCount int      `json:"truncated_count,omitempty"`
	DetailAction   string   `json:"detail_action"`
}

type CompactPlan struct {
	Version        int              `json:"version"`
	OK             bool             `json:"ok"`
	PlanDigest     string           `json:"plan_digest"`
	PlanPath       string           `json:"plan_path"`
	OperationCount int              `json:"operation_count"`
	FileCount      int              `json:"file_count"`
	BlockerCount   int              `json:"blocker_count"`
	Blockers       []CompactBlocker `json:"blockers,omitempty"`
}

var (
	planRepository = regexp.MustCompile(`^[^/[:space:]]+/[^/[:space:]]+$`)
	planRevision   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func DigestPlan(plan Plan) (string, error) {
	plan.PlanDigest = ""
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func CanonicalPlanJSON(plan Plan) ([]byte, error) {
	if err := ValidatePlan(plan); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func ReadPlan(reader io.Reader) (Plan, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Plan{}, errors.New("durable plan contains multiple JSON values")
		}
		return Plan{}, err
	}
	if err := ValidatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func ValidatePlan(plan Plan) error {
	if plan.Version != PlanVersion {
		return fmt.Errorf("unsupported durable plan version %d", plan.Version)
	}
	if !planRepository.MatchString(plan.Repository) || plan.Proposal <= 0 || !planRevision.MatchString(plan.BaselineRevision) {
		return errors.New("repository, proposal, and exact lowercase baseline revision are required")
	}
	if err := validateWorkflowAuthority(plan.Workflow); err != nil {
		return err
	}
	if !sort.SliceIsSorted(plan.Sources, func(i, j int) bool { return sourceAuthorityKey(plan.Sources[i]) < sourceAuthorityKey(plan.Sources[j]) }) {
		return errors.New("source authorities are not in deterministic order")
	}
	for _, source := range plan.Sources {
		if source.ID == "" || source.URL == "" || source.RepresentationVersion < 0 || !isPlanDigest(source.RepresentationDigest) {
			return fmt.Errorf("source authority %q is incomplete", source.ID)
		}
	}
	if !sort.SliceIsSorted(plan.Operations, func(i, j int) bool {
		return plannedOperationKey(plan.Operations[i]) < plannedOperationKey(plan.Operations[j])
	}) {
		return errors.New("planned operations are not in deterministic order")
	}
	if !sort.SliceIsSorted(plan.Blockers, func(i, j int) bool { return plan.Blockers[i].Code < plan.Blockers[j].Code }) {
		return errors.New("blockers are not in deterministic order")
	}
	for _, blocker := range plan.Blockers {
		if blocker.Code == "" || len(blocker.Messages) == 0 || len(blocker.Messages) > maxBlockerMessages || len(blocker.AffectedIDs) > maxAffectedIDs || blocker.TruncatedCount < 0 {
			return fmt.Errorf("blocker %q is not bounded", blocker.Code)
		}
	}
	if len(plan.Blockers) > 0 && len(plan.Files) != 0 {
		return errors.New("blocker-only durable plan must not carry executable file mutations")
	}
	if len(plan.Files) > 0 {
		ordered, err := filecas.ValidateFileMutations(plan.Files)
		if err != nil {
			return fmt.Errorf("durable file mutations: %w", err)
		}
		for index := range ordered {
			if ordered[index].ID != plan.Files[index].ID || ordered[index].Path != plan.Files[index].Path {
				return errors.New("durable file mutations are not in deterministic order")
			}
		}
	}
	digest, err := DigestPlan(plan)
	if err != nil {
		return err
	}
	if plan.PlanDigest != digest {
		return fmt.Errorf("durable plan digest mismatch: declared=%s computed=%s", plan.PlanDigest, digest)
	}
	return nil
}

func validateWorkflowAuthority(authority WorkflowAuthority) error {
	mode, modeErr := NormalizeMode(authority.Mode)
	if modeErr != nil || mode != authority.Mode || strings.TrimSpace(authority.ConfigPath) == "" || !isPlanDigest(authority.ConfigDigest) {
		return errors.New("exact workflow config path, digest, and supported mode are required")
	}
	return nil
}

func Compact(plan Plan, planPath string) CompactPlan {
	result := CompactPlan{Version: CompactVersion, OK: len(plan.Blockers) == 0, PlanDigest: plan.PlanDigest,
		PlanPath: planPath, OperationCount: len(plan.Operations), FileCount: len(plan.Files), BlockerCount: len(plan.Blockers)}
	for _, blocker := range plan.Blockers {
		result.Blockers = append(result.Blockers, CompactBlocker{Code: blocker.Code,
			AffectedIDs: append([]string(nil), blocker.AffectedIDs...), TruncatedCount: blocker.TruncatedCount,
			DetailAction: fmt.Sprintf("issue-spec durable-spec detail --plan %s --code %s", shellQuote(planPath), shellQuote(blocker.Code))})
	}
	return result
}

func shellQuote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n'\"\\$`;&|<>()[]{}*?!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func sourceAuthorityKey(source SourceAuthority) string {
	return source.ID + "\x00" + source.URL + "\x00" + source.RepresentationDigest
}

func plannedOperationKey(operation PlannedOperation) string {
	return operation.ID + "\x00" + operation.SourceSpecID + "\x00" + operation.Path
}

func isPlanDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func summarizeFindings(findings []Finding) []Blocker {
	byCode := map[string][]Finding{}
	for _, finding := range findings {
		byCode[finding.Code] = append(byCode[finding.Code], finding)
	}
	codes := make([]string, 0, len(byCode))
	for code := range byCode {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	result := make([]Blocker, 0, len(codes))
	for _, code := range codes {
		items := byCode[code]
		sort.Slice(items, func(i, j int) bool { return findingKey(items[i]) < findingKey(items[j]) })
		messageSet, idSet := map[string]bool{}, map[string]bool{}
		var messages, ids []string
		for _, item := range items {
			if !messageSet[item.Message] && len(messages) < maxBlockerMessages {
				messageSet[item.Message] = true
				messages = append(messages, item.Message)
			}
			id := item.OperationID
			if id == "" {
				id = item.SourceSpecID
			}
			if id == "" {
				id = item.Path
			}
			if id != "" && !idSet[id] {
				idSet[id] = true
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		truncated := 0
		if len(ids) > maxAffectedIDs {
			truncated = len(ids) - maxAffectedIDs
			ids = ids[:maxAffectedIDs]
		}
		result = append(result, Blocker{Code: code, Messages: messages, AffectedIDs: ids, TruncatedCount: truncated})
	}
	return result
}

func findingKey(finding Finding) string {
	return finding.Code + "\x00" + finding.OperationID + "\x00" + finding.SourceSpecID + "\x00" + finding.Path + "\x00" + finding.Requirement + "\x00" + finding.Message
}
