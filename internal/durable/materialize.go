package durable

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/reconcile/filecas"
)

type SourceInput struct {
	ID                    string
	URL                   string
	RepresentationVersion int64
	RepresentationDigest  string
	Body                  string
	Intent                Intent
	IntentFound           bool
	CanonicalError        string
	IntentError           string
}

type BaselineFile struct {
	Exists bool
	Body   string
}

type CompileInput struct {
	Repository       string
	Proposal         int
	ProposalURL      string
	BaselineRevision string
	Workflow         WorkflowAuthority
	Sources          []SourceInput
	BaselineFiles    map[string]BaselineFile
}

type operationInput struct {
	Source SourceInput
	Value  Operation
}

type requirementBlock struct {
	Title string
	Body  string
}

type durableDocument struct {
	Capability string
	Prefix     string
	Blocks     []requirementBlock
}

var durableRequirementHeading = regexp.MustCompile(`(?m)^### Requirement: ([^\r\n]+)$`)

func CompilePlan(input CompileInput) (Plan, error) {
	if !planRepository.MatchString(strings.TrimSpace(input.Repository)) || input.Proposal <= 0 ||
		!planRevision.MatchString(strings.TrimSpace(input.BaselineRevision)) {
		return Plan{}, errors.New("compile durable plan requires repository, proposal, and exact lowercase baseline revision")
	}
	input.Repository = strings.TrimSpace(input.Repository)
	input.BaselineRevision = strings.TrimSpace(input.BaselineRevision)
	plan := Plan{Version: PlanVersion, Repository: input.Repository, Proposal: input.Proposal,
		BaselineRevision: input.BaselineRevision, Workflow: input.Workflow}
	var findings []Finding
	mode, modeErr := NormalizeMode(input.Workflow.Mode)
	if modeErr != nil || mode != ModeRepository {
		message := "durable preview requires durable_specs.mode: repository"
		if modeErr != nil {
			message = modeErr.Error()
		}
		findings = append(findings, Finding{Code: BlockWorkflowMode, Message: message})
	}
	if strings.TrimSpace(input.Workflow.ConfigPath) == "" || !isPlanDigest(input.Workflow.ConfigDigest) {
		findings = append(findings, Finding{Code: BlockWorkflowConfig, Message: "selected workflow config identity is incomplete"})
	}

	sources := append([]SourceInput(nil), input.Sources...)
	sort.Slice(sources, func(i, j int) bool { return sourceInputKey(sources[i]) < sourceInputKey(sources[j]) })
	bySourceID := map[string][]int{}
	for index, source := range sources {
		source.ID = strings.TrimSpace(source.ID)
		source.URL = strings.TrimSpace(source.URL)
		sources[index] = source
		bySourceID[source.ID] = append(bySourceID[source.ID], index)
		plan.Sources = append(plan.Sources, SourceAuthority{ID: source.ID, URL: source.URL,
			RepresentationVersion: source.RepresentationVersion, RepresentationDigest: source.RepresentationDigest, Intent: source.Intent})
	}
	for id, indexes := range bySourceID {
		if id == "" || len(indexes) != 1 {
			findings = append(findings, Finding{Code: BlockSourceAmbiguous, SourceSpecID: id,
				Message: fmt.Sprintf("confirmed source SPEC %q has %d exact representations", id, len(indexes))})
		}
	}
	if len(sources) == 0 {
		findings = append(findings, Finding{Code: BlockSourceAmbiguous, Message: "proposal has no exact confirmed source SPEC representations"})
	}

	var operations []operationInput
	for sourcePosition, source := range sources {
		if source.ID == "" || source.URL == "" || source.RepresentationVersion < 0 || !isPlanDigest(source.RepresentationDigest) {
			findings = append(findings, Finding{Code: BlockSourceInvalid, SourceSpecID: source.ID,
				Message: fmt.Sprintf("source SPEC %q representation identity is incomplete", source.ID)})
			continue
		}
		if source.CanonicalError != "" {
			findings = append(findings, Finding{Code: BlockSourceInvalid, SourceSpecID: source.ID, Message: source.CanonicalError})
			continue
		}
		if source.IntentError != "" {
			code := BlockSourceInvalid
			if strings.Contains(source.IntentError, "clean repository-relative path") || strings.Contains(source.IntentError, "legacy durable target") {
				code = BlockUnsafeTargetPath
			}
			findings = append(findings, Finding{Code: code, SourceSpecID: source.ID, Message: source.IntentError})
			continue
		}
		if !source.IntentFound {
			findings = append(findings, Finding{Code: BlockSourceInvalid, SourceSpecID: source.ID,
				Message: fmt.Sprintf("confirmed source SPEC %s has no durable intent", source.ID)})
			continue
		}
		requirement, requirementErr := currentSpecRequirement(source.Body)
		if requirementErr != nil && source.Intent.Kind == IntentOperations {
			findings = append(findings, Finding{Code: BlockSourceInvalid, SourceSpecID: source.ID, Message: requirementErr.Error()})
			continue
		}
		normalized, normalizeErr := NormalizeIntent(source.Intent, ValidationOptions{SpecID: source.ID, SpecRequirement: requirement})
		if normalizeErr != nil {
			code := BlockSourceInvalid
			if strings.Contains(normalizeErr.Error(), "clean repository-relative path") || strings.Contains(normalizeErr.Error(), "legacy durable target") {
				code = BlockUnsafeTargetPath
			}
			findings = append(findings, Finding{Code: code, SourceSpecID: source.ID, Message: normalizeErr.Error()})
			continue
		}
		plan.Sources[sourcePosition].Intent = normalized
		for _, operation := range normalized.Operations {
			operations = append(operations, operationInput{Source: source, Value: operation})
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operationInputKey(operations[i]) < operationInputKey(operations[j]) })
	findings = append(findings, collisionFindings(operations)...)

	byPath := map[string][]operationInput{}
	for _, item := range operations {
		operation := item.Value
		plan.Operations = append(plan.Operations, PlannedOperation{ID: operation.ID, SourceSpecID: item.Source.ID,
			SourceSpecURL: item.Source.URL, Kind: operation.Kind, Path: operation.Path,
			CurrentRequirement: operation.CurrentRequirement, NewRequirement: operation.NewRequirement})
		byPath[operation.Path] = append(byPath[operation.Path], item)
	}
	paths := make([]string, 0, len(byPath))
	for targetPath := range byPath {
		paths = append(paths, targetPath)
	}
	sort.Strings(paths)
	for _, targetPath := range paths {
		items := byPath[targetPath]
		baseline := input.BaselineFiles[targetPath]
		if strings.HasPrefix(targetPath, "openspec/specs/") && !baseline.Exists {
			findings = append(findings, Finding{Code: BlockUnsafeTargetPath, OperationID: items[0].Value.ID,
				Path: targetPath, Message: "legacy durable target does not exist at the exact baseline"})
		}
		document, documentErr := baselineDocument(baseline, items[0].Value.Capability, input.ProposalURL)
		if documentErr != nil {
			findings = append(findings, Finding{Code: BlockTargetFileInvalid, OperationID: items[0].Value.ID,
				Path: targetPath, Message: documentErr.Error()})
			continue
		}
		for _, duplicate := range duplicateRequirementTitles(document.Blocks) {
			findings = append(findings, Finding{Code: BlockTargetFileInvalid, OperationID: items[0].Value.ID,
				Path: targetPath, Requirement: duplicate, Message: fmt.Sprintf("durable target has duplicate Requirement %q", duplicate)})
		}
		preimage := filecas.MissingFileImage()
		if baseline.Exists {
			preimage = filecas.ImageForContent([]byte(baseline.Body))
			preimage.Content = ""
		}
		for _, item := range items {
			operationFindings := applyOperationToDocument(&document, item, &plan)
			findings = append(findings, operationFindings...)
		}
		postimageBody := renderDurableDocument(document)
		plan.Files = append(plan.Files, filecas.FileMutation{ID: "durable-file:" + targetPath, Path: targetPath,
			Preimage: preimage, Postimage: filecas.ImageForContent([]byte(postimageBody))})
	}

	sort.Slice(plan.Operations, func(i, j int) bool {
		return plannedOperationKey(plan.Operations[i]) < plannedOperationKey(plan.Operations[j])
	})
	sort.Slice(findings, func(i, j int) bool { return findingKey(findings[i]) < findingKey(findings[j]) })
	plan.Findings = findings
	plan.Blockers = summarizeFindings(findings)
	if len(plan.Blockers) != 0 {
		plan.Files = nil
	} else {
		ordered, err := filecas.ValidateFileMutations(plan.Files)
		if err != nil {
			return Plan{}, err
		}
		plan.Files = ordered
	}
	digest, err := DigestPlan(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.PlanDigest = digest
	if err := ValidatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func sourceInputKey(source SourceInput) string {
	return strings.TrimSpace(source.ID) + "\x00" + strings.TrimSpace(source.URL) + "\x00" + source.RepresentationDigest
}

func operationInputKey(item operationInput) string {
	return item.Value.ID + "\x00" + item.Source.ID + "\x00" + item.Value.Path
}

func collisionFindings(operations []operationInput) []Finding {
	var findings []Finding
	ids, targets, renameEndpoints := map[string][]operationInput{}, map[string][]operationInput{}, map[string][]operationInput{}
	for _, item := range operations {
		operation := item.Value
		ids[operation.ID] = append(ids[operation.ID], item)
		title := operation.CurrentRequirement
		if operation.Kind == OperationAdded {
			title = operation.NewRequirement
		}
		targets[operation.Path+"\x00"+title] = append(targets[operation.Path+"\x00"+title], item)
		if operation.Kind == OperationRenamed {
			renameEndpoints[operation.Path+"\x00"+operation.NewRequirement] = append(renameEndpoints[operation.Path+"\x00"+operation.NewRequirement], item)
		}
	}
	for id, items := range ids {
		if len(items) > 1 {
			for _, item := range items {
				findings = append(findings, Finding{Code: BlockOperationCollision, OperationID: item.Value.ID,
					SourceSpecID: item.Source.ID, Message: fmt.Sprintf("operation id %s is declared %d times", id, len(items))})
			}
		}
	}
	for key, items := range targets {
		if len(items) > 1 {
			for _, item := range items {
				findings = append(findings, Finding{Code: BlockTargetCollision, OperationID: item.Value.ID,
					SourceSpecID: item.Source.ID, Path: item.Value.Path, Message: fmt.Sprintf("target %s is declared %d times", displayTarget(key), len(items))})
			}
		}
	}
	for endpoint, items := range renameEndpoints {
		owners := append([]operationInput(nil), items...)
		owners = append(owners, targets[endpoint]...)
		ownerIDs := map[string]bool{}
		for _, owner := range owners {
			ownerIDs[owner.Value.ID+"\x00"+owner.Source.ID] = true
		}
		if len(ownerIDs) > 1 {
			for _, item := range items {
				findings = append(findings, Finding{Code: BlockRenameCollision, OperationID: item.Value.ID,
					SourceSpecID: item.Source.ID, Path: item.Value.Path, Message: "rename endpoint collides with another operation target"})
			}
		}
	}
	return findings
}

func baselineDocument(file BaselineFile, capability, proposalURL string) (durableDocument, error) {
	if !file.Exists {
		return newDurableDocument(capability, proposalURL), nil
	}
	return parseDurableDocument(file.Body, capability)
}

func newDurableDocument(capability, proposalURL string) durableDocument {
	var prefix strings.Builder
	fmt.Fprintf(&prefix, "# %s\n\n## Purpose\n\nDefine the long-lived behavior contract for this capability.\n", capability)
	if strings.TrimSpace(proposalURL) != "" {
		fmt.Fprintf(&prefix, "\nProposal Issues:\n- %s\n", strings.TrimSpace(proposalURL))
	}
	prefix.WriteString("\n## Requirements\n\n")
	return durableDocument{Capability: capability, Prefix: prefix.String()}
}

func parseDurableDocument(body, capability string) (durableDocument, error) {
	if strings.Contains(body, "\r") || !strings.HasSuffix(body, "\n") {
		return durableDocument{}, errors.New("durable target must use LF newlines and one trailing newline")
	}
	header := "# " + capability + "\n"
	if !strings.HasPrefix(body, header) {
		return durableDocument{}, fmt.Errorf("durable target heading does not match capability %q", capability)
	}
	marker := "## Requirements\n\n"
	if strings.Count(body, marker) != 1 || strings.Count(body, "\n## Purpose\n") != 1 {
		return durableDocument{}, errors.New("durable target must contain one canonical Purpose and Requirements section")
	}
	index := strings.Index(body, marker)
	prefix, section := body[:index+len(marker)], body[index+len(marker):]
	section = strings.TrimSuffix(section, "\n")
	if strings.TrimSpace(section) == "" {
		return durableDocument{Capability: capability, Prefix: prefix}, nil
	}
	locations := durableRequirementHeading.FindAllStringIndex(section, -1)
	if len(locations) == 0 || strings.TrimSpace(section[:locations[0][0]]) != "" {
		return durableDocument{}, errors.New("durable target Requirements section contains content outside Requirement blocks")
	}
	document := durableDocument{Capability: capability, Prefix: prefix}
	for index, location := range locations {
		end := len(section)
		if index+1 < len(locations) {
			end = locations[index+1][0]
		}
		block := strings.TrimSpace(section[location[0]:end])
		match := durableRequirementHeading.FindStringSubmatch(block)
		if len(match) != 2 || strings.TrimSpace(match[1]) != match[1] || match[1] == "" {
			return durableDocument{}, errors.New("durable target has an invalid Requirement identity")
		}
		document.Blocks = append(document.Blocks, requirementBlock{Title: match[1], Body: block})
	}
	return document, nil
}

func duplicateRequirementTitles(blocks []requirementBlock) []string {
	counts := map[string]int{}
	for _, block := range blocks {
		counts[block.Title]++
	}
	var result []string
	for title, count := range counts {
		if count > 1 {
			result = append(result, title)
		}
	}
	sort.Strings(result)
	return result
}

func renderDurableDocument(document durableDocument) string {
	if len(document.Blocks) == 0 {
		return document.Prefix
	}
	blocks := make([]string, len(document.Blocks))
	for index, block := range document.Blocks {
		blocks[index] = strings.TrimSpace(block.Body)
	}
	return document.Prefix + strings.Join(blocks, "\n\n") + "\n"
}

func applyOperationToDocument(document *durableDocument, item operationInput, plan *Plan) []Finding {
	operation, source := item.Value, item.Source
	planned := findPlannedOperation(plan.Operations, operation.ID, source.ID)
	if document.Capability != operation.Capability {
		return []Finding{{Code: BlockTargetFileInvalid, OperationID: operation.ID, SourceSpecID: source.ID,
			Path: operation.Path, Message: "operation capability differs from target document"}}
	}
	currentTitle := operation.CurrentRequirement
	if operation.Kind == OperationAdded {
		currentTitle = operation.NewRequirement
	}
	indexes := requirementIndexes(document.Blocks, currentTitle)
	if operation.Kind == OperationAdded {
		if len(indexes) != 0 {
			return []Finding{{Code: BlockTargetExists, OperationID: operation.ID, SourceSpecID: source.ID,
				Path: operation.Path, Requirement: currentTitle, Message: fmt.Sprintf("Requirement %q already exists at baseline", currentTitle)}}
		}
		block, err := projectedRequirementBlock(operation, source)
		if err != nil {
			return []Finding{{Code: BlockProjectionInvalid, OperationID: operation.ID, SourceSpecID: source.ID, Path: operation.Path, Message: err.Error()}}
		}
		document.Blocks = append(document.Blocks, requirementBlock{Title: operation.NewRequirement, Body: block})
		planned.BlockPostimageDigest = filecas.FileDigest([]byte(block))
		return nil
	}
	if len(indexes) == 0 {
		return []Finding{{Code: BlockTargetMissing, OperationID: operation.ID, SourceSpecID: source.ID,
			Path: operation.Path, Requirement: currentTitle, Message: fmt.Sprintf("Requirement %q is missing at baseline", currentTitle)}}
	}
	if len(indexes) != 1 {
		return []Finding{{Code: BlockTargetAmbiguous, OperationID: operation.ID, SourceSpecID: source.ID,
			Path: operation.Path, Requirement: currentTitle, Message: fmt.Sprintf("Requirement %q is ambiguous at baseline", currentTitle)}}
	}
	index := indexes[0]
	planned.BlockPreimageDigest = filecas.FileDigest([]byte(document.Blocks[index].Body))
	switch operation.Kind {
	case OperationModified:
		block, err := projectedRequirementBlock(operation, source)
		if err != nil {
			return []Finding{{Code: BlockProjectionInvalid, OperationID: operation.ID, SourceSpecID: source.ID, Path: operation.Path, Message: err.Error()}}
		}
		document.Blocks[index] = requirementBlock{Title: currentTitle, Body: block}
		planned.BlockPostimageDigest = filecas.FileDigest([]byte(block))
	case OperationRemoved:
		document.Blocks = append(document.Blocks[:index], document.Blocks[index+1:]...)
	case OperationRenamed:
		if len(requirementIndexes(document.Blocks, operation.NewRequirement)) != 0 {
			return []Finding{{Code: BlockRenameCollision, OperationID: operation.ID, SourceSpecID: source.ID,
				Path: operation.Path, Requirement: operation.NewRequirement, Message: fmt.Sprintf("rename endpoint Requirement %q already exists", operation.NewRequirement)}}
		}
		block := renameRequirementBlock(document.Blocks[index].Body, operation.NewRequirement, source.URL)
		document.Blocks[index] = requirementBlock{Title: operation.NewRequirement, Body: block}
		planned.BlockPostimageDigest = filecas.FileDigest([]byte(block))
	}
	return nil
}

func findPlannedOperation(operations []PlannedOperation, id, sourceID string) *PlannedOperation {
	for index := range operations {
		if operations[index].ID == id && operations[index].SourceSpecID == sourceID {
			return &operations[index]
		}
	}
	return &PlannedOperation{}
}

func requirementIndexes(blocks []requirementBlock, title string) []int {
	var indexes []int
	for index, block := range blocks {
		if block.Title == title {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func projectedRequirementBlock(operation Operation, source SourceInput) (string, error) {
	if operation.Projection == nil {
		return "", errors.New("projection is required")
	}
	var block string
	switch operation.Projection.Source {
	case ProjectionCurrentSpec:
		projected, err := projectCurrentSpec(source.Body)
		if err != nil {
			return "", err
		}
		block = projected
	case ProjectionInline:
		projection := operation.Projection
		if projection.Requirement == nil {
			return "", errors.New("inline projection Requirement is missing")
		}
		var builder strings.Builder
		fmt.Fprintf(&builder, "### Requirement: %s\n\n%s", projection.Requirement.Title, projection.Requirement.Text)
		for _, scenario := range projection.Scenarios {
			fmt.Fprintf(&builder, "\n\n#### Scenario: %s\n\n- **WHEN** %s\n- **THEN** %s", scenario.Title, scenario.When, scenario.Then)
		}
		block = builder.String()
	default:
		return "", fmt.Errorf("unsupported projection source %q", operation.Projection.Source)
	}
	return normalizeSourceMetadata(block, source.URL), nil
}

func currentSpecRequirement(body string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var titles []string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "## Requirement:") {
			titles = append(titles, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "## Requirement:")))
		}
	}
	if len(titles) != 1 || titles[0] == "" {
		return "", fmt.Errorf("source SPEC must contain exactly one canonical Requirement, found %d", len(titles))
	}
	return titles[0], nil
}

func projectCurrentSpec(body string) (string, error) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	start := strings.Index(body, "## Requirement:")
	if start < 0 {
		return "", errors.New("current-spec projection has no canonical Requirement")
	}
	body = body[start:]
	if end := strings.Index(body, "\n## Durable Intent"); end >= 0 {
		body = body[:end]
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## Requirement:") {
			lines[index] = strings.Replace(line, "## Requirement:", "### Requirement:", 1)
		} else if strings.HasPrefix(trimmed, "### Scenario:") {
			lines[index] = strings.Replace(line, "### Scenario:", "#### Scenario:", 1)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), nil
}

func normalizeSourceMetadata(block, sourceURL string) string {
	lines := strings.Split(strings.TrimSpace(block), "\n")
	cut := len(lines)
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "Source SPEC comments:" || strings.HasPrefix(trimmed, "Source SPEC comment:") {
			cut = index
			break
		}
	}
	lines = lines[:cut]
	return strings.TrimSpace(strings.Join(lines, "\n")) + "\n\nSource SPEC comments:\n- " + strings.TrimSpace(sourceURL)
}

func renameRequirementBlock(block, newTitle, sourceURL string) string {
	lines := strings.Split(strings.TrimSpace(block), "\n")
	if len(lines) > 0 {
		lines[0] = "### Requirement: " + newTitle
	}
	return normalizeSourceMetadata(strings.Join(lines, "\n"), sourceURL)
}
