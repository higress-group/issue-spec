package durable

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const IntentVersion = 1

type Mode string

const (
	ModeNone       Mode = "none"
	ModeRepository Mode = "repository"
)

func NormalizeMode(value Mode) (Mode, error) {
	raw := string(value)
	if raw == "" {
		return ModeNone, nil
	}
	if raw != strings.TrimSpace(raw) {
		return "", errors.New("durable_specs.mode must not have surrounding whitespace")
	}
	if value != ModeNone && value != ModeRepository {
		return "", fmt.Errorf("unsupported durable_specs.mode %q (want none or repository)", value)
	}
	return value, nil
}

type IntentKind string

const (
	IntentUnchanged  IntentKind = "UNCHANGED"
	IntentOperations IntentKind = "OPERATIONS"
)

type OperationKind string

const (
	OperationAdded    OperationKind = "ADDED"
	OperationModified OperationKind = "MODIFIED"
	OperationRemoved  OperationKind = "REMOVED"
	OperationRenamed  OperationKind = "RENAMED"
)

type ProjectionSource string

const (
	ProjectionCurrentSpec ProjectionSource = "current-spec"
	ProjectionInline      ProjectionSource = "inline"
)

type Intent struct {
	Version    int         `json:"version"`
	Kind       IntentKind  `json:"intent"`
	Operations []Operation `json:"operations,omitempty"`
}

type Operation struct {
	ID                 string        `json:"id"`
	Kind               OperationKind `json:"kind"`
	Capability         string        `json:"capability"`
	Path               string        `json:"path"`
	CurrentRequirement string        `json:"current_requirement,omitempty"`
	NewRequirement     string        `json:"new_requirement,omitempty"`
	Projection         *Projection   `json:"projection,omitempty"`
}

type Projection struct {
	Source      ProjectionSource  `json:"source"`
	Requirement *RequirementInput `json:"requirement,omitempty"`
	Scenarios   []ScenarioInput   `json:"scenarios,omitempty"`
}

type RequirementInput struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type ScenarioInput struct {
	Title string `json:"title"`
	When  string `json:"when"`
	Then  string `json:"then"`
}

type ValidationOptions struct {
	RepositoryRoot  string
	SpecID          string
	SpecRequirement string
}

var (
	operationIDPattern = regexp.MustCompile(`^SPEC-[0-9]{3,}-OP-[0-9]{2,}$`)
	capabilityPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)
	normativePattern   = regexp.MustCompile(`\b(MUST|SHALL)\b`)
)

const (
	maxOperations = 128
	maxTextBytes  = 4096
)

func ParseSpecIntent(body string, opts ValidationOptions) (Intent, bool, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	headings := make([]int, 0, 1)
	for index, line := range lines {
		if strings.TrimSpace(line) == "## Durable Intent" {
			headings = append(headings, index)
		}
	}
	if len(headings) == 0 {
		return Intent{}, false, nil
	}
	if len(headings) != 1 {
		return Intent{}, true, errors.New("SPEC must contain exactly one `## Durable Intent` section")
	}
	start, end := headings[0]+1, len(lines)
	for index := start; index < len(lines); index++ {
		if strings.HasPrefix(strings.TrimSpace(lines[index]), "## ") {
			end = index
			break
		}
	}
	section := trimBlankLines(lines[start:end])
	if len(section) < 3 || strings.TrimSpace(section[0]) != "```json" || strings.TrimSpace(section[len(section)-1]) != "```" {
		return Intent{}, true, errors.New("Durable Intent must contain exactly one fenced `json` object")
	}
	for _, line := range section[1 : len(section)-1] {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			return Intent{}, true, errors.New("Durable Intent must contain exactly one fenced `json` object")
		}
	}
	payload := strings.TrimSpace(strings.Join(section[1:len(section)-1], "\n"))
	if payload == "" {
		return Intent{}, true, errors.New("Durable Intent JSON must not be empty")
	}
	var intent Intent
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return Intent{}, true, fmt.Errorf("parse Durable Intent JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Intent{}, true, errors.New("parse Durable Intent JSON: multiple JSON values")
		}
		return Intent{}, true, fmt.Errorf("parse Durable Intent JSON: %w", err)
	}
	if strings.TrimSpace(opts.SpecRequirement) == "" {
		requirements := specRequirementTitles(body)
		if len(requirements) == 1 {
			opts.SpecRequirement = requirements[0]
		}
	}
	normalized, err := NormalizeIntent(intent, opts)
	if err != nil {
		return Intent{}, true, err
	}
	return normalized, true, nil
}

func CanonicalJSON(intent Intent, opts ValidationOptions) ([]byte, error) {
	normalized, err := NormalizeIntent(intent, opts)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(normalized, "", "  ")
}

func NormalizeIntent(intent Intent, opts ValidationOptions) (Intent, error) {
	clone := intent
	clone.Operations = append([]Operation(nil), intent.Operations...)
	for index := range clone.Operations {
		if intent.Operations[index].Projection == nil {
			continue
		}
		projection := *intent.Operations[index].Projection
		projection.Scenarios = append([]ScenarioInput(nil), projection.Scenarios...)
		if projection.Requirement != nil {
			requirement := *projection.Requirement
			projection.Requirement = &requirement
		}
		clone.Operations[index].Projection = &projection
	}
	if err := clone.Validate(opts); err != nil {
		return Intent{}, err
	}
	sort.Slice(clone.Operations, func(i, j int) bool { return clone.Operations[i].ID < clone.Operations[j].ID })
	return clone, nil
}

func (intent Intent) Validate(opts ValidationOptions) error {
	if intent.Version != IntentVersion {
		return fmt.Errorf("durable intent version: unsupported value %d", intent.Version)
	}
	switch intent.Kind {
	case IntentUnchanged:
		if len(intent.Operations) != 0 {
			return errors.New("UNCHANGED durable intent must not contain operations")
		}
		return nil
	case IntentOperations:
		if len(intent.Operations) == 0 {
			return errors.New("OPERATIONS durable intent requires at least one operation")
		}
	default:
		return fmt.Errorf("durable intent: unsupported value %q", intent.Kind)
	}
	if len(intent.Operations) > maxOperations {
		return fmt.Errorf("durable operations exceed %d items", maxOperations)
	}

	operationIDs := map[string]int{}
	primaryTargets := map[string]string{}
	renameEndpoints := map[string]string{}
	for index, operation := range intent.Operations {
		if err := validateOperation(operation, opts); err != nil {
			return fmt.Errorf("operations[%d]: %w", index, err)
		}
		if first, ok := operationIDs[operation.ID]; ok {
			return fmt.Errorf("operations[%d]: duplicate operation id %q (first at index %d)", index, operation.ID, first)
		}
		operationIDs[operation.ID] = index
		primary := operationTarget(operation)
		if owner, ok := primaryTargets[primary]; ok {
			return fmt.Errorf("operations[%d]: duplicate target %q already owned by %s", index, displayTarget(primary), owner)
		}
		primaryTargets[primary] = operation.ID
		if operation.Kind == OperationRenamed {
			endpoint := targetKey(operation.Path, operation.NewRequirement)
			if owner, ok := renameEndpoints[endpoint]; ok {
				return fmt.Errorf("operations[%d]: rename endpoint %q conflicts with %s", index, displayTarget(endpoint), owner)
			}
			renameEndpoints[endpoint] = operation.ID
		}
	}
	endpoints := make([]string, 0, len(renameEndpoints))
	for endpoint := range renameEndpoints {
		endpoints = append(endpoints, endpoint)
	}
	sort.Strings(endpoints)
	for _, endpoint := range endpoints {
		owner := renameEndpoints[endpoint]
		if primaryOwner, ok := primaryTargets[endpoint]; ok && primaryOwner != owner {
			return fmt.Errorf("rename endpoint %q for %s conflicts with target owned by %s", displayTarget(endpoint), owner, primaryOwner)
		}
	}
	return nil
}

func validateOperation(operation Operation, opts ValidationOptions) error {
	if !operationIDPattern.MatchString(operation.ID) {
		return fmt.Errorf("id %q must match SPEC-<n>-OP-<nn>", operation.ID)
	}
	if specID := strings.TrimSpace(opts.SpecID); specID != "" && !strings.HasPrefix(operation.ID, specID+"-OP-") {
		return fmt.Errorf("id %q must belong to %s", operation.ID, specID)
	}
	if !capabilityPattern.MatchString(operation.Capability) {
		return fmt.Errorf("capability %q must be a lowercase path-safe slug", operation.Capability)
	}
	if err := validateTargetPath(operation.Path, operation.Capability, opts.RepositoryRoot); err != nil {
		return err
	}
	if err := validateOptionalIdentity("current_requirement", operation.CurrentRequirement); err != nil {
		return err
	}
	if err := validateOptionalIdentity("new_requirement", operation.NewRequirement); err != nil {
		return err
	}

	switch operation.Kind {
	case OperationAdded:
		if operation.CurrentRequirement != "" || operation.NewRequirement == "" || operation.Projection == nil {
			return errors.New("ADDED requires new_requirement and projection and forbids current_requirement")
		}
		return validateProjection(*operation.Projection, operation.NewRequirement, opts.SpecRequirement)
	case OperationModified:
		if operation.CurrentRequirement == "" || operation.NewRequirement != "" || operation.Projection == nil {
			return errors.New("MODIFIED requires current_requirement and projection and forbids new_requirement")
		}
		return validateProjection(*operation.Projection, operation.CurrentRequirement, opts.SpecRequirement)
	case OperationRemoved:
		if operation.CurrentRequirement == "" || operation.NewRequirement != "" || operation.Projection != nil {
			return errors.New("REMOVED requires current_requirement and forbids new_requirement and projection")
		}
		return nil
	case OperationRenamed:
		if operation.CurrentRequirement == "" || operation.NewRequirement == "" || operation.Projection != nil {
			return errors.New("RENAMED requires current_requirement and new_requirement and forbids projection")
		}
		if operation.CurrentRequirement == operation.NewRequirement {
			return errors.New("RENAMED current_requirement and new_requirement must be distinct")
		}
		return nil
	default:
		return fmt.Errorf("kind: unsupported value %q", operation.Kind)
	}
}

func validateProjection(projection Projection, targetRequirement, currentSpecRequirement string) error {
	switch projection.Source {
	case ProjectionCurrentSpec:
		if projection.Requirement != nil || len(projection.Scenarios) != 0 {
			return errors.New("current-spec projection forbids inline requirement and scenarios")
		}
		if strings.TrimSpace(currentSpecRequirement) == "" {
			return errors.New("current-spec projection requires exactly one canonical current SPEC Requirement")
		}
		if targetRequirement != currentSpecRequirement {
			return fmt.Errorf("current-spec Requirement %q does not match target requirement %q", currentSpecRequirement, targetRequirement)
		}
		return nil
	case ProjectionInline:
		if projection.Requirement == nil || len(projection.Scenarios) == 0 {
			return errors.New("inline projection requires one requirement and at least one scenario")
		}
		if projection.Requirement.Title != targetRequirement {
			return fmt.Errorf("inline Requirement title %q does not match target requirement %q", projection.Requirement.Title, targetRequirement)
		}
		return validateInlineProjection(*projection.Requirement, projection.Scenarios)
	default:
		return fmt.Errorf("projection.source: unsupported value %q", projection.Source)
	}
}

func validateInlineProjection(requirement RequirementInput, scenarios []ScenarioInput) error {
	if err := validateRequiredText("inline requirement.title", requirement.Title); err != nil {
		return err
	}
	if err := validateRequiredText("inline requirement.text", requirement.Text); err != nil {
		return err
	}
	if !normativePattern.MatchString(requirement.Text) {
		return errors.New("inline requirement.text must contain normative MUST or SHALL language")
	}
	if len(scenarios) > maxOperations {
		return fmt.Errorf("inline scenarios exceed %d items", maxOperations)
	}
	seen := map[string]int{}
	for index, scenario := range scenarios {
		for _, field := range []struct {
			name  string
			value string
		}{
			{name: "title", value: scenario.Title},
			{name: "when", value: scenario.When},
			{name: "then", value: scenario.Then},
		} {
			if err := validateRequiredText(fmt.Sprintf("inline scenarios[%d].%s", index, field.name), field.value); err != nil {
				return err
			}
		}
		if first, ok := seen[scenario.Title]; ok {
			return fmt.Errorf("inline scenarios[%d].title duplicates index %d", index, first)
		}
		seen[scenario.Title] = index
	}
	return nil
}

func validateTargetPath(value, capability, repositoryRoot string) error {
	if err := validateRequiredText("path", value); err != nil {
		return err
	}
	clean := path.Clean(value)
	if strings.Contains(value, `\`) || path.IsAbs(value) || clean != value || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path %q must be a clean repository-relative path", value)
	}
	canonical := path.Join("issue-spec", "specs", capability, "spec.md")
	legacy := path.Join("openspec", "specs", capability, "spec.md")
	switch value {
	case canonical:
		return nil
	case legacy:
		if strings.TrimSpace(repositoryRoot) == "" {
			return nil
		}
		return validateExistingLegacyTarget(repositoryRoot, value)
	default:
		return fmt.Errorf("path %q must be %q or the already-existing legacy path %q", value, canonical, legacy)
	}
}

func validateExistingLegacyTarget(root, relative string) error {
	root = filepath.Clean(root)
	target := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("legacy durable target %q does not already exist", relative)
		}
		return fmt.Errorf("inspect legacy durable target %q: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("legacy durable target %q is not a regular file", relative)
	}
	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	resolvedTarget, targetErr := filepath.EvalSymlinks(target)
	if rootErr == nil && targetErr == nil {
		rel, err := filepath.Rel(resolvedRoot, resolvedTarget)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("legacy durable target %q escapes the repository root", relative)
		}
	}
	return nil
}

func validateOptionalIdentity(name, value string) error {
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s must be one exact Requirement title", name)
	}
	return validateRequiredText(name, value)
}

func validateRequiredText(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not have surrounding whitespace", name)
	}
	if len(value) > maxTextBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxTextBytes)
	}
	return nil
}

func operationTarget(operation Operation) string {
	if operation.Kind == OperationAdded {
		return targetKey(operation.Path, operation.NewRequirement)
	}
	return targetKey(operation.Path, operation.CurrentRequirement)
}

func targetKey(targetPath, requirement string) string {
	return targetPath + "\x00" + requirement
}

func displayTarget(key string) string {
	return strings.Replace(key, "\x00", "#", 1)
}

func specRequirementTitles(body string) []string {
	var titles []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## Requirement:") {
			title := strings.TrimSpace(strings.TrimPrefix(trimmed, "## Requirement:"))
			if title != "" {
				titles = append(titles, title)
			}
		}
	}
	return titles
}

func trimBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func CanonicalEqual(left, right Intent, opts ValidationOptions) bool {
	leftJSON, leftErr := CanonicalJSON(left, opts)
	rightJSON, rightErr := CanonicalJSON(right, opts)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
