package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/templates"
	"github.com/higress-group/issue-spec/internal/workflow"
)

const (
	workflowDeliveryBoth     = "both"
	workflowDeliverySkills   = "skills"
	workflowDeliveryCommands = "commands"
)

type workflowTool struct {
	ID        string
	SkillsDir string
}

type workflowCommandAdapter struct {
	FilePath func(commandID string) (string, error)
	Format   func(templates.CommandContent) string
}

type workflowGenerationResult struct {
	Delivery            string   `json:"delivery"`
	Tools               []string `json:"tools"`
	SkillFiles          []string `json:"skillFiles,omitempty"`
	CommandFiles        []string `json:"commandFiles,omitempty"`
	PrunedFiles         []string `json:"prunedFiles,omitempty"`
	CommandsSkipped     []string `json:"commandsSkipped,omitempty"`
	GlobalPromptFiles   []string `json:"globalPromptFiles,omitempty"`
	GlobalPromptsDryRun bool     `json:"globalPromptsDryRun,omitempty"`
	WorkflowSource      string   `json:"workflowSource,omitempty"`
	WorkflowSchema      string   `json:"workflowSchema,omitempty"`
}

type globalPromptInstallOptions struct {
	Enabled   bool
	Directory string
	DryRun    bool
}

var workflowTools = []workflowTool{
	{ID: "codex", SkillsDir: ".agents"},
	{ID: "claude", SkillsDir: ".claude"},
}

func writeWorkflowArtifacts(root, repo, toolsArg, delivery string) (workflowGenerationResult, error) {
	delivery, tools, err := resolveWorkflowGenerationOptions(root, toolsArg, delivery)
	if err != nil {
		return workflowGenerationResult{}, err
	}
	result := workflowGenerationResult{Delivery: delivery}
	for _, tool := range tools {
		result.Tools = append(result.Tools, tool.ID)
	}
	if len(tools) == 0 {
		return result, nil
	}
	plan, err := workflow.Resolve(root)
	if err != nil {
		return workflowGenerationResult{}, err
	}
	return writeWorkflowArtifactsResolved(root, repo, delivery, tools, plan)
}

func writeWorkflowArtifactsWithPlan(root, repo, toolsArg, delivery string, plan workflow.Plan) (workflowGenerationResult, error) {
	delivery, tools, err := resolveWorkflowGenerationOptions(root, toolsArg, delivery)
	if err != nil {
		return workflowGenerationResult{}, err
	}
	return writeWorkflowArtifactsResolved(root, repo, delivery, tools, plan)
}

func writeWorkflowArtifactsWithProvider(root, repo, toolsArg, delivery string, provider workflow.ProviderPlan) (workflowGenerationResult, error) {
	delivery, tools, err := resolveWorkflowGenerationOptions(root, toolsArg, delivery)
	if err != nil {
		return workflowGenerationResult{}, err
	}
	if len(tools) == 0 {
		result := workflowGenerationResult{Delivery: delivery}
		return result, nil
	}
	plan, err := workflow.Resolve(root)
	if err != nil {
		return workflowGenerationResult{}, err
	}
	return writeWorkflowArtifactsResolvedWithProvider(root, repo, delivery, tools, plan, &provider)
}

func writeWorkflowArtifactsResolved(root, repo, delivery string, tools []workflowTool, plan workflow.Plan) (workflowGenerationResult, error) {
	return writeWorkflowArtifactsResolvedWithProvider(root, repo, delivery, tools, plan, nil)
}

func writeWorkflowArtifactsResolvedWithProvider(root, repo, delivery string, tools []workflowTool, plan workflow.Plan, provider *workflow.ProviderPlan) (workflowGenerationResult, error) {
	result := workflowGenerationResult{Delivery: delivery, WorkflowSource: string(plan.Source.Kind), WorkflowSchema: plan.Source.SchemaName}
	for _, tool := range tools {
		result.Tools = append(result.Tools, tool.ID)
	}
	if len(tools) == 0 {
		return result, nil
	}
	pruned, err := pruneManagedArchiveWorkflowAssets(root, delivery, tools)
	if err != nil {
		return result, err
	}
	result.PrunedFiles = pruned

	if delivery != workflowDeliveryCommands {
		for _, tool := range tools {
			skillsDir := filepath.Join(root, tool.SkillsDir, "skills")
			for _, skill := range workflowSkillsWithProvider(repo, plan, provider) {
				path := filepath.Join(skillsDir, skill.Name, "SKILL.md")
				if err := writeTextFile(path, skill.Content); err != nil {
					return result, err
				}
				result.SkillFiles = append(result.SkillFiles, cleanGeneratedPath(path))
			}
		}
	}

	if delivery != workflowDeliverySkills {
		commands := workflowCommandContentsWithProvider(repo, plan, provider)
		for _, tool := range tools {
			adapter := commandAdapterForTool(tool.ID)
			if adapter == nil {
				result.CommandsSkipped = append(result.CommandsSkipped, tool.ID)
				continue
			}
			for _, command := range commands {
				path, err := adapter.FilePath(command.ID)
				if err != nil {
					return result, err
				}
				if !filepath.IsAbs(path) {
					path = filepath.Join(root, path)
				}
				if err := writeTextFile(path, adapter.Format(command)); err != nil {
					return result, err
				}
				result.CommandFiles = append(result.CommandFiles, cleanGeneratedPath(path))
			}
		}
	}

	return result, nil
}

func pruneManagedArchiveWorkflowAssets(root, delivery string, tools []workflowTool) ([]string, error) {
	var candidates []string
	if delivery != workflowDeliveryCommands {
		for _, tool := range tools {
			candidates = append(candidates, filepath.Join(root, tool.SkillsDir, "skills", "issue-spec-archive", "SKILL.md"))
		}
	}
	if delivery != workflowDeliverySkills {
		for _, tool := range tools {
			if tool.ID == "claude" {
				candidates = append(candidates, filepath.Join(root, ".claude", "commands", "issue-spec", "archive.md"))
			}
		}
	}
	var pruned []string
	for _, path := range candidates {
		body, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect managed archive workflow asset %s: %w", path, err)
		}
		content := string(body)
		managedSkill := strings.Contains(content, `generatedBy: "issue-spec"`) && strings.Contains(content, "name: issue-spec-archive\n")
		managedCommand := strings.Contains(content, `name: "Issue Spec: Archive"`) &&
			strings.Contains(content, `category: "Workflow"`) && strings.Contains(content, "# Issue Spec Archive\n")
		if !managedSkill && !managedCommand {
			continue
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("prune managed archive workflow asset %s: %w", path, err)
		}
		pruned = append(pruned, cleanGeneratedPath(path))
	}
	return pruned, nil
}

func installGlobalCodexPrompts(root, repo string, provider *workflow.ProviderPlan, options globalPromptInstallOptions, result *workflowGenerationResult) error {
	if !options.Enabled {
		return nil
	}
	if result == nil {
		return fmt.Errorf("global prompt installation requires a workflow generation result")
	}
	if result.Delivery == workflowDeliverySkills {
		return fmt.Errorf("--install-global-prompts requires --delivery both or commands")
	}

	plan, err := workflow.Resolve(root)
	if err != nil {
		return err
	}
	if result.WorkflowSource == "" {
		result.WorkflowSource = string(plan.Source.Kind)
		result.WorkflowSchema = plan.Source.SchemaName
	}
	directory := strings.TrimSpace(options.Directory)
	if directory == "" {
		home, err := codexHome()
		if err != nil {
			return err
		}
		directory = filepath.Join(home, "prompts")
	} else if !filepath.IsAbs(directory) {
		directory = filepath.Join(root, directory)
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("resolve global prompt directory: %w", err)
	}

	result.GlobalPromptsDryRun = options.DryRun
	for _, command := range workflowCommandContentsWithProvider(repo, plan, provider) {
		path := filepath.Join(directory, fmt.Sprintf("issue-spec-%s.md", command.ID))
		result.GlobalPromptFiles = append(result.GlobalPromptFiles, filepath.ToSlash(path))
		if options.DryRun {
			continue
		}
		if err := writeTextFile(path, formatCodexCommand(command)); err != nil {
			return err
		}
	}
	return nil
}

func resolveWorkflowGenerationOptions(root, toolsArg, delivery string) (string, []workflowTool, error) {
	delivery, err := normalizeWorkflowDelivery(delivery)
	if err != nil {
		return "", nil, err
	}
	tools, err := resolveWorkflowTools(root, toolsArg)
	if err != nil {
		return "", nil, err
	}
	return delivery, tools, nil
}

func workflowSkills(repo string, plan workflow.Plan) []templates.RenderedSkill {
	return workflowSkillsWithProvider(repo, plan, nil)
}

func workflowSkillsWithProvider(repo string, plan workflow.Plan, provider *workflow.ProviderPlan) []templates.RenderedSkill {
	skills := templates.IssueSpecSkills(repo)
	notice := workflowNotice(plan)
	for i := range skills {
		if skills[i].Name == "issue-spec-github" {
			continue
		}
		skills[i].Content = strings.TrimRight(skills[i].Content, "\n") + "\n\n" + strings.TrimRight(notice, "\n") + "\n"
	}
	if provider != nil {
		providerNotice := templates.ProviderWorkflowNotice(*provider)
		for i := range skills {
			if skills[i].Name == "issue-spec-github" {
				continue
			}
			skills[i].Content = strings.TrimRight(skills[i].Content, "\n") + "\n\n" + strings.TrimRight(providerNotice, "\n") + "\n"
		}
		skills = append(skills, templates.IssueSpecProviderSkill(repo, *provider))
	}
	return skills
}

func workflowCommandContents(repo string, plan workflow.Plan) []templates.CommandContent {
	return workflowCommandContentsWithProvider(repo, plan, nil)
}

func workflowCommandContentsWithProvider(repo string, plan workflow.Plan, provider *workflow.ProviderPlan) []templates.CommandContent {
	commands := templates.IssueSpecCommandContents(repo)
	notice := workflowNotice(plan)
	for i := range commands {
		commands[i].Body = strings.TrimRight(commands[i].Body, "\n") + "\n\n" + strings.TrimRight(notice, "\n") + "\n"
		if provider != nil {
			commands[i].Body = strings.TrimRight(commands[i].Body, "\n") + "\n\n" + strings.TrimRight(templates.ProviderWorkflowNotice(*provider), "\n") + "\n"
		}
	}
	return commands
}

func normalizeWorkflowDelivery(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return workflowDeliveryBoth, nil
	}
	switch value {
	case workflowDeliveryBoth, workflowDeliverySkills, workflowDeliveryCommands:
		return value, nil
	default:
		return "", fmt.Errorf("invalid --delivery %q; valid values: both, skills, commands", value)
	}
}

func resolveWorkflowTools(root, toolsArg string) ([]workflowTool, error) {
	toolsArg = strings.TrimSpace(toolsArg)
	if toolsArg == "" {
		return detectWorkflowTools(root), nil
	}

	available := map[string]workflowTool{}
	for _, tool := range workflowTools {
		available[tool.ID] = tool
	}

	lower := strings.ToLower(toolsArg)
	if lower == "none" {
		return nil, nil
	}
	if lower == "all" {
		return append([]workflowTool(nil), workflowTools...), nil
	}

	var tools []workflowTool
	seen := map[string]bool{}
	for _, token := range strings.Split(toolsArg, ",") {
		id := strings.ToLower(strings.TrimSpace(token))
		if id == "" {
			continue
		}
		if id == "all" || id == "none" {
			return nil, fmt.Errorf("cannot combine %q with specific tool IDs", id)
		}
		tool, ok := available[id]
		if !ok {
			return nil, fmt.Errorf("invalid --tools value %q; valid values: all, none, %s", token, workflowToolList())
		}
		if !seen[id] {
			tools = append(tools, tool)
			seen[id] = true
		}
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("--tools requires all, none, or a comma-separated list of tool IDs")
	}
	return tools, nil
}

func detectWorkflowTools(root string) []workflowTool {
	var tools []workflowTool
	for _, tool := range workflowTools {
		if _, err := os.Stat(filepath.Join(root, tool.SkillsDir)); err == nil {
			tools = append(tools, tool)
		}
	}
	return tools
}

func workflowToolList() string {
	names := make([]string, 0, len(workflowTools))
	for _, tool := range workflowTools {
		names = append(names, tool.ID)
	}
	return strings.Join(names, ", ")
}

func commandAdapterForTool(toolID string) *workflowCommandAdapter {
	switch toolID {
	case "claude":
		return &workflowCommandAdapter{
			FilePath: func(commandID string) (string, error) {
				return filepath.Join(".claude", "commands", "issue-spec", fmt.Sprintf("%s.md", commandID)), nil
			},
			Format: formatClaudeCommand,
		}
	default:
		return nil
	}
}

func codexHome() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

func formatCodexCommand(command templates.CommandContent) string {
	return fmt.Sprintf(`---
description: %s
argument-hint: command arguments
---

%s`, yamlString(command.Description), strings.TrimSpace(command.Body)+"\n")
}

func formatClaudeCommand(command templates.CommandContent) string {
	return fmt.Sprintf(`---
name: %s
description: %s
category: %s
tags: %s
---

%s`, yamlString(command.Name), yamlString(command.Description), yamlString(command.Category), yamlStringList(command.Tags), strings.TrimSpace(command.Body)+"\n")
}

func yamlString(value string) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(data)
}

func yamlStringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, yamlString(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func writeTextFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func cleanGeneratedPath(path string) string {
	clean := filepath.Clean(path)
	if rel, err := filepath.Rel(".", clean); err == nil && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(clean)
}
