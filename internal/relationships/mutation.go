package relationships

import (
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/preview"
)

const (
	MutationVersion            = 1
	DefaultMutationTargetLimit = 64
)

// Mutation is the versioned, provider-neutral description of one owner write.
// Targets are exact provider identities; no reverse endpoint is ever included.
type Mutation struct {
	Version int                 `json:"version"`
	Owner   model.ArtifactRef   `json:"owner"`
	Add     []model.ArtifactRef `json:"add,omitempty"`
	Remove  []model.ArtifactRef `json:"remove,omitempty"`
}

// FrozenMutation binds a pure mutation to the exact owner representation that
// was used to compute it. Commands and reconcile persist these values before
// selecting a provider mutation guarantee.
type FrozenMutation struct {
	Version               int      `json:"version"`
	Mutation              Mutation `json:"mutation"`
	RepresentationVersion int64    `json:"representation_version,omitempty"`
	BeforeDigest          string   `json:"before_digest"`
	DesiredBody           string   `json:"desired_body"`
	AfterDigest           string   `json:"after_digest"`
}

// PlanMutation resolves all endpoints from one bounded snapshot before
// computing a byte-exact owner body. It performs no provider I/O.
func PlanMutation(artifacts []model.Artifact, owner model.ArtifactRef, add, remove []model.ArtifactRef,
	before string, representationVersion int64, limit int) (FrozenMutation, error) {
	if limit == 0 {
		limit = DefaultMutationTargetLimit
	}
	if limit < 1 || len(add)+len(remove) > limit {
		return FrozenMutation{}, fmt.Errorf("%w: mutation targets=%d limit=%d", ErrBound, len(add)+len(remove), limit)
	}
	if err := owner.Validate(); err != nil {
		return FrozenMutation{}, fmt.Errorf("%w: owner: %v", ErrInvalid, err)
	}
	if representationVersion < 0 {
		return FrozenMutation{}, fmt.Errorf("%w: representation version cannot be negative", ErrInvalid)
	}

	resolve := func(values []model.ArtifactRef, action string) ([]model.ArtifactRef, error) {
		result := make([]model.ArtifactRef, 0, len(values))
		seen := map[string]bool{}
		for _, target := range values {
			if err := target.Validate(); err != nil {
				return nil, fmt.Errorf("%w: %s target: %v", ErrInvalid, action, err)
			}
			rule, resolvedOwner, resolvedTarget, err := Resolve(artifacts, owner, target)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", action, target.ID, err)
			}
			if !rule.GenericLink || resolvedOwner.Key() != owner.Key() || resolvedOwner.URL != owner.URL {
				return nil, fmt.Errorf("%w: %s %s resolves to a different or dedicated owner", ErrInvalid, action, target.ID)
			}
			if seen[resolvedTarget.Key()] {
				continue
			}
			seen[resolvedTarget.Key()] = true
			result = append(result, resolvedTarget)
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Key() < result[j].Key() })
		return result, nil
	}
	resolvedAdd, err := resolve(add, "add")
	if err != nil {
		return FrozenMutation{}, err
	}
	resolvedRemove, err := resolve(remove, "remove")
	if err != nil {
		return FrozenMutation{}, err
	}
	for index := range resolvedRemove {
		target := resolvedRemove[index]
		aliases := map[string]bool{model.NormalizeURL(target.URL): true}
		for _, artifact := range artifacts {
			ref, refErr := artifact.Ref()
			if refErr == nil && ref.Key() == target.Key() {
				for _, alias := range model.ArtifactProviderURLs(artifact) {
					aliases[alias] = true
				}
			}
		}
		physicalURL := ""
		for _, artifact := range artifacts {
			ref, refErr := artifact.Ref()
			if refErr != nil || ref.Key() != owner.Key() {
				continue
			}
			for _, related := range model.RelatedCommentURLs(artifact.Comment) {
				if aliases[model.NormalizeURL(related)] {
					physicalURL = model.NormalizeURL(related)
				}
			}
		}
		if physicalURL == "" {
			return FrozenMutation{}, fmt.Errorf("%w: remove target %s is not an existing canonical owner link", ErrInvalid, target.ID)
		}
		resolvedRemove[index].URL = physicalURL
	}
	adds := map[string]bool{}
	for _, target := range resolvedAdd {
		adds[target.Key()] = true
	}
	for _, target := range resolvedRemove {
		if adds[target.Key()] {
			return FrozenMutation{}, fmt.Errorf("%w: target %s appears in both add and remove", ErrInvalid, target.ID)
		}
	}
	mutation := Mutation{Version: MutationVersion, Owner: owner, Add: resolvedAdd, Remove: resolvedRemove}
	desired, _, err := ApplyMutation(before, mutation)
	if err != nil {
		return FrozenMutation{}, err
	}
	return FrozenMutation{Version: MutationVersion, Mutation: mutation, RepresentationVersion: representationVersion,
		BeforeDigest: model.RepresentationDigest(before), DesiredBody: desired,
		AfterDigest: model.RepresentationDigest(desired)}, nil
}

// ApplyMutation changes only the owner's canonical Related Comments URL set
// and, for supported removals, its declared semantic relationship section.
// It is deterministic, bounded, idempotent, and never invents a peer write.
func ApplyMutation(body string, mutation Mutation) (string, bool, error) {
	if mutation.Version != MutationVersion {
		return "", false, fmt.Errorf("%w: unsupported mutation version %d", ErrInvalid, mutation.Version)
	}
	if err := mutation.Owner.Validate(); err != nil {
		return "", false, fmt.Errorf("%w: owner: %v", ErrInvalid, err)
	}
	if len(mutation.Add)+len(mutation.Remove) > DefaultMutationTargetLimit {
		return "", false, fmt.Errorf("%w: mutation targets=%d limit=%d", ErrBound,
			len(mutation.Add)+len(mutation.Remove), DefaultMutationTargetLimit)
	}
	typed := model.ParseTypedComment(body)
	if !model.HasTypedMarker(body) || len(typed.Errors) != 0 || typed.Type != mutation.Owner.Type || typed.ID != mutation.Owner.ID {
		return "", false, fmt.Errorf("%w: owner body is not the exact canonical typed artifact %s", ErrInvalid, mutation.Owner.ID)
	}

	add, remove := map[string]model.ArtifactRef{}, map[string]model.ArtifactRef{}
	validateTargets := func(values []model.ArtifactRef, out map[string]model.ArtifactRef, action string) error {
		for _, target := range values {
			if err := target.Validate(); err != nil {
				return fmt.Errorf("%w: %s target: %v", ErrInvalid, action, err)
			}
			if _, duplicate := out[target.Key()]; duplicate {
				continue
			}
			if _, ok := ruleForTypes(mutation.Owner.Type, target.Type); !ok {
				return fmt.Errorf("%w: %s cannot own a generic relationship to %s", ErrUnsupported,
					mutation.Owner.Type, target.Type)
			}
			out[target.Key()] = target
		}
		return nil
	}
	if err := validateTargets(mutation.Add, add, "add"); err != nil {
		return "", false, err
	}
	if err := validateTargets(mutation.Remove, remove, "remove"); err != nil {
		return "", false, err
	}
	for key, target := range remove {
		if _, overlap := add[key]; overlap {
			return "", false, fmt.Errorf("%w: target %s appears in both add and remove", ErrInvalid, target.ID)
		}
	}
	for _, target := range sortedRefValues(add) {
		rule, _ := ruleForTypes(mutation.Owner.Type, target.Type)
		var heading string
		switch rule.Kind {
		case TaskCoversSpec:
			heading = "### Covers"
		case ProcessParentTask:
			heading = "### Parent TASK"
		case ProcessDependsProcess:
			heading = "### Dependencies"
		}
		if heading != "" {
			values := model.TypedSectionList(body, heading)
			found := false
			for _, value := range values {
				found = found || value == target.ID
			}
			if !found || (rule.Kind == ProcessParentTask && len(values) != 1) {
				return "", false, fmt.Errorf("%w: %s does not semantically authorize %s", ErrUnsupported,
					mutation.Owner.ID, target.ID)
			}
		}
	}

	updated := body
	for _, target := range sortedRefValues(remove) {
		rule, _ := ruleForTypes(mutation.Owner.Type, target.Type)
		var err error
		updated, err = removeSemanticAuthority(updated, rule, target.ID)
		if err != nil {
			return "", false, err
		}
	}
	updated, err := mutateRelatedHeader(updated, sortedRefValues(add), sortedRefValues(remove))
	if err != nil {
		return "", false, err
	}
	return updated, updated != body, nil
}

func ruleForTypes(ownerType, targetType string) (OwnerRule, bool) {
	var matches []OwnerRule
	for _, rule := range registry {
		if rule.GenericLink && rule.OwnerType == ownerType && rule.TargetType == targetType {
			matches = append(matches, rule)
		}
	}
	if len(matches) != 1 {
		return OwnerRule{}, false
	}
	return matches[0], true
}

func sortedRefValues(values map[string]model.ArtifactRef) []model.ArtifactRef {
	result := make([]model.ArtifactRef, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key() < result[j].Key() })
	return result
}

func mutateRelatedHeader(body string, add, remove []model.ArtifactRef) (string, error) {
	semanticLines := strings.Split(preview.SemanticView(body), "\n")
	rawLines := strings.Split(body, "\n")
	links, related, blockEnd := -1, -1, -1
	for index, line := range semanticLines {
		if strings.TrimSpace(line) != "Links:" {
			continue
		}
		links, blockEnd = index, index+1
		for blockEnd < len(semanticLines) {
			trimmed := strings.TrimSpace(semanticLines[blockEnd])
			if !strings.HasPrefix(trimmed, "- ") {
				break
			}
			name, _, ok := cutLinkLine(trimmed)
			if !ok {
				return "", fmt.Errorf("%w: invalid relationship header line %q", ErrInvalid, trimmed)
			}
			if name == RelatedCommentsField {
				if related >= 0 {
					return "", fmt.Errorf("%w: duplicate Related Comments header", ErrAmbiguous)
				}
				related = blockEnd
			}
			blockEnd++
		}
		break
	}
	if links < 0 {
		return "", fmt.Errorf("%w: typed owner is missing Links block", ErrInvalid)
	}
	values := map[string]string{}
	if related >= 0 {
		_, value, _ := cutLinkLine(strings.TrimSpace(rawLines[related]))
		for _, raw := range splitLinkValue(value) {
			normalized := model.NormalizeURL(raw)
			if normalized == "" {
				continue
			}
			if _, duplicate := values[normalized]; duplicate {
				return "", fmt.Errorf("%w: duplicate Related Comments URL %s", ErrAmbiguous, normalized)
			}
			values[normalized] = raw
		}
	}
	for _, target := range remove {
		delete(values, model.NormalizeURL(target.URL))
	}
	for _, target := range add {
		values[model.NormalizeURL(target.URL)] = target.URL
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rendered := "N/A"
	if len(keys) != 0 {
		items := make([]string, 0, len(keys))
		for _, key := range keys {
			items = append(items, values[key])
		}
		rendered = strings.Join(items, ", ")
	}
	line := "- " + RelatedCommentsField + ": " + rendered
	if related >= 0 {
		rawLines[related] = line
	} else {
		rawLines = append(rawLines, "")
		copy(rawLines[blockEnd+1:], rawLines[blockEnd:])
		rawLines[blockEnd] = line
	}
	return strings.Join(rawLines, "\n"), nil
}

func removeSemanticAuthority(body string, rule OwnerRule, targetID string) (string, error) {
	var heading string
	switch rule.Kind {
	case TaskCoversSpec:
		heading = "### Covers"
	case ProcessDependsProcess:
		heading = "### Dependencies"
	case ProcessParentTask:
		return "", fmt.Errorf("%w: parent TASK removal requires a replacement planning command", ErrUnsupported)
	case ReviewCoversProcess, ReviewCoversSpec, VerifyCoversProcess, VerifyCoversSpec:
		return "", fmt.Errorf("%w: accepted REVIEW/VERIFY relationships are append-only", ErrUnsupported)
	default:
		return "", fmt.Errorf("%w: removal is not supported for %s", ErrUnsupported, rule.Kind)
	}
	raw := strings.Split(body, "\n")
	semantic := strings.Split(preview.SemanticView(body), "\n")
	end := len(semantic)
	// A machine-translation suffix repeats section lines; semantic authority
	// never lives there, so the scan stops at the divider line.
	if divider := model.TranslationDividerLine(body); divider >= 0 && divider < end {
		end = divider
	}
	headingIndex := -1
	for index := 0; index < end; index++ {
		trimmed := strings.TrimSpace(semantic[index])
		if headingIndex < 0 {
			if trimmed == heading {
				headingIndex = index
			}
			continue
		}
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
			end = index
			break
		}
	}
	if headingIndex < 0 {
		return "", fmt.Errorf("%w: semantic section %s is absent", ErrInvalid, heading)
	}
	match := -1
	for index := headingIndex + 1; index < end; index++ {
		if strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(semantic[index]), "- ")) == targetID &&
			strings.HasPrefix(strings.TrimSpace(semantic[index]), "- ") {
			if match >= 0 {
				return "", fmt.Errorf("%w: semantic target %s is duplicated", ErrAmbiguous, targetID)
			}
			match = index
		}
	}
	if match < 0 {
		return "", fmt.Errorf("%w: semantic section %s does not name %s", ErrInvalid, heading, targetID)
	}
	raw = append(raw[:match], raw[match+1:]...)
	semantic = append(semantic[:match], semantic[match+1:]...)
	end--
	hasValue := false
	for index := headingIndex + 1; index < end; index++ {
		trimmed := strings.TrimSpace(semantic[index])
		if strings.HasPrefix(trimmed, "- ") && !strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")), "N/A") {
			hasValue = true
		}
	}
	if !hasValue {
		insertAt := headingIndex + 1
		for insertAt < len(raw) && strings.TrimSpace(raw[insertAt]) == "" {
			insertAt++
		}
		raw = append(raw, "")
		copy(raw[insertAt+1:], raw[insertAt:])
		raw[insertAt] = "- N/A"
	}
	return strings.Join(raw, "\n"), nil
}

func cutLinkLine(line string) (string, string, bool) {
	line = strings.TrimPrefix(strings.TrimSpace(line), "- ")
	name, value, ok := strings.Cut(line, ":")
	return strings.TrimSpace(name), strings.TrimSpace(value), ok
}

func splitLinkValue(value string) []string {
	if strings.TrimSpace(value) == "" || strings.EqualFold(strings.TrimSpace(value), "N/A") {
		return nil
	}
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" && !strings.EqualFold(item, "N/A") {
			result = append(result, item)
		}
	}
	return result
}
