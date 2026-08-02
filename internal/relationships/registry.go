// Package relationships defines the closed typed-artifact relationship policy
// and builds pure bounded topology indexes from already-collected artifacts.
package relationships

import (
	"errors"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
)

// Kind is a stable built-in relationship identifier.
type Kind string

const (
	TaskCoversSpec        Kind = "task-covers-spec"
	ProcessParentTask     Kind = "process-parent-task"
	ProcessDependsProcess Kind = "process-depends-on-process"
	ReviewCoversProcess   Kind = "review-covers-process"
	ReviewCoversSpec      Kind = "review-covers-spec"
	VerifyCoversProcess   Kind = "verify-covers-process"
	VerifyCoversSpec      Kind = "verify-covers-spec"
	ProcessCodeSubject    Kind = "process-code-subject"
	ProcessSupersededBy   Kind = "superseded-by"
	RelatedCommentsField       = "Related Comments"
)

// OwnerRule names the sole writer and semantic source for one relationship.
// GenericLink is false for relationships owned by dedicated lifecycle/code
// subject commands rather than the generic typed-comment link path.
type OwnerRule struct {
	Kind           Kind   `json:"kind"`
	OwnerType      string `json:"owner_type"`
	TargetType     string `json:"target_type"`
	SemanticSource string `json:"semantic_source"`
	LinkField      string `json:"link_field"`
	GenericLink    bool   `json:"generic_link"`
}

var registry = []OwnerRule{
	{TaskCoversSpec, "TASK", "SPEC", "section:covers", RelatedCommentsField, true},
	{ProcessParentTask, "PROCESS", "TASK", "section:parent-task", RelatedCommentsField, true},
	{ProcessDependsProcess, "PROCESS", "PROCESS", "section:dependencies", RelatedCommentsField, true},
	{ReviewCoversProcess, "REVIEW", "PROCESS", "accepted-review-or-explicit-sync", RelatedCommentsField, true},
	{ReviewCoversSpec, "REVIEW", "SPEC", "accepted-review-or-explicit-sync", RelatedCommentsField, true},
	{VerifyCoversProcess, "VERIFY", "PROCESS", "accepted-verification-assignment", RelatedCommentsField, true},
	{VerifyCoversSpec, "VERIFY", "SPEC", "section:covered-specs-or-accepted-assignment", RelatedCommentsField, true},
	{ProcessCodeSubject, "PROCESS", "CODE_SUBJECT", "provider-code-subject-binding", "PR", false},
	{ProcessSupersededBy, "PROCESS", "PROCESS", "carrier:superseded-by", RelatedCommentsField, false},
}

// Exclusion documents relationship families that never enter the built-in
// typed-comment owner registry.
type Exclusion struct {
	Name      string `json:"name"`
	Authority string `json:"authority"`
}

var exclusions = []Exclusion{
	{"proposal-design-implement-predecessors", "phase issue-body metadata"},
	{"question-answer-resolution", "QUESTION/ANSWER resolution carrier"},
	{"review-findings-and-replies", "code-review finding carrier"},
	{"code-change-rationale", "rationale carrier"},
	{"provider-checks", "provider evidence"},
	{"arbitrary-free-form-urls", "no built-in relationship authority"},
}

var (
	ErrUnsupported = errors.New("relationship_unsupported")
	ErrInvalid     = errors.New("relationship_target_invalid")
	ErrAmbiguous   = errors.New("relationship_ambiguous")
	ErrBound       = errors.New("relationship_bound_exceeded")
)

// Registry returns a defensive copy of the closed owner table.
func Registry() []OwnerRule { return append([]OwnerRule(nil), registry...) }

// Exclusions returns a defensive copy of the explicit non-relationship table.
func Exclusions() []Exclusion { return append([]Exclusion(nil), exclusions...) }

// Lookup returns the one rule for kind.
func Lookup(kind Kind) (OwnerRule, bool) {
	for _, rule := range registry {
		if rule.Kind == kind {
			return rule, true
		}
	}
	return OwnerRule{}, false
}

// Normalize orients an explicitly selected relationship kind regardless of
// legacy pair order. Semantic authority is validated later against the owner
// body; this function cannot invent a relationship from a pair alone.
func Normalize(kind Kind, left, right model.ArtifactRef) (owner, target model.ArtifactRef, err error) {
	rule, ok := Lookup(kind)
	if !ok || rule.TargetType == "CODE_SUBJECT" {
		return model.ArtifactRef{}, model.ArtifactRef{}, fmt.Errorf("%w: %s", ErrUnsupported, kind)
	}
	if err := left.Validate(); err != nil {
		return model.ArtifactRef{}, model.ArtifactRef{}, fmt.Errorf("%w: left endpoint: %v", ErrInvalid, err)
	}
	if err := right.Validate(); err != nil {
		return model.ArtifactRef{}, model.ArtifactRef{}, fmt.Errorf("%w: right endpoint: %v", ErrInvalid, err)
	}
	switch {
	case rule.OwnerType == rule.TargetType && left.Type == rule.OwnerType && right.Type == rule.TargetType:
		return model.ArtifactRef{}, model.ArtifactRef{}, fmt.Errorf("%w: %s requires owner semantic authority", ErrAmbiguous, kind)
	case left.Type == rule.OwnerType && right.Type == rule.TargetType:
		return left, right, nil
	case right.Type == rule.OwnerType && left.Type == rule.TargetType:
		return right, left, nil
	default:
		return model.ArtifactRef{}, model.ArtifactRef{}, fmt.Errorf("%w: %s does not relate %s and %s",
			ErrUnsupported, kind, left.Type, right.Type)
	}
}

// Resolve selects the sole schema owner after exact endpoint and semantic
// validation against one already-collected bounded artifact set.
func Resolve(artifacts []model.Artifact, left, right model.ArtifactRef) (OwnerRule, model.ArtifactRef, model.ArtifactRef, error) {
	set, err := buildCatalog(artifacts)
	if err != nil {
		return OwnerRule{}, model.ArtifactRef{}, model.ArtifactRef{}, err
	}
	for _, endpoint := range []struct {
		name string
		ref  model.ArtifactRef
	}{{"left", left}, {"right", right}} {
		name, ref := endpoint.name, endpoint.ref
		if err := ref.Validate(); err != nil {
			return OwnerRule{}, model.ArtifactRef{}, model.ArtifactRef{}, fmt.Errorf("%w: %s endpoint: %v", ErrInvalid, name, err)
		}
		key, ok := set.byID[ref.ID]
		if !ok || set.refs[key].Key() != ref.Key() || !artifactHasURL(set.artifacts[key], ref.URL) ||
			(ref.CommentID != 0 && set.refs[key].CommentID != ref.CommentID) {
			return OwnerRule{}, model.ArtifactRef{}, model.ArtifactRef{}, fmt.Errorf("%w: %s endpoint %s is not exact", ErrInvalid, name, ref.ID)
		}
	}
	var matches []struct {
		rule          OwnerRule
		owner, target model.ArtifactRef
	}
	for _, rule := range candidateRules(left.Type, right.Type) {
		for _, pair := range orientations(rule, left, right) {
			if semanticAuthority(set, rule, pair.owner, pair.target) {
				matches = append(matches, struct {
					rule          OwnerRule
					owner, target model.ArtifactRef
				}{rule, pair.owner, pair.target})
			}
		}
	}
	if len(matches) == 0 {
		return OwnerRule{}, model.ArtifactRef{}, model.ArtifactRef{}, fmt.Errorf("%w: no semantic owner for %s and %s", ErrUnsupported, left.ID, right.ID)
	}
	if len(matches) != 1 {
		return OwnerRule{}, model.ArtifactRef{}, model.ArtifactRef{}, fmt.Errorf("%w: %s and %s match %d owner rules", ErrAmbiguous, left.ID, right.ID, len(matches))
	}
	return matches[0].rule, matches[0].owner, matches[0].target, nil
}

func artifactHasURL(artifact model.Artifact, value string) bool {
	value = model.NormalizeURL(value)
	for _, candidate := range model.ArtifactProviderURLs(artifact) {
		if candidate == value {
			return true
		}
	}
	return false
}

func candidateRules(leftType, rightType string) []OwnerRule {
	leftType, rightType = strings.ToUpper(strings.TrimSpace(leftType)), strings.ToUpper(strings.TrimSpace(rightType))
	var result []OwnerRule
	for _, rule := range registry {
		if rule.LinkField != RelatedCommentsField || rule.TargetType == "CODE_SUBJECT" {
			continue
		}
		if (leftType == rule.OwnerType && rightType == rule.TargetType) ||
			(rightType == rule.OwnerType && leftType == rule.TargetType) {
			result = append(result, rule)
		}
	}
	return result
}
