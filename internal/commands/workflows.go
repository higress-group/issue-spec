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
	Delivery        string   `json:"delivery"`
	Tools           []string `json:"tools"`
	SkillFiles      []string `json:"skillFiles,omitempty"`
	CommandFiles    []string `json:"commandFiles,omitempty"`
	CommandsSkipped []string `json:"commandsSkipped,omitempty"`
	WorkflowSource  string   `json:"workflowSource,omitempty"`
	WorkflowSchema  string   `json:"workflowSchema,omitempty"`
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

func writeWorkflowArtifactsResolved(root, repo, delivery string, tools []workflowTool, plan workflow.Plan) (workflowGenerationResult, error) {
	result := workflowGenerationResult{Delivery: delivery, WorkflowSource: string(plan.Source.Kind), WorkflowSchema: plan.Source.SchemaName}
	for _, tool := range tools {
		result.Tools = append(result.Tools, tool.ID)
	}
	if len(tools) == 0 {
		return result, nil
	}

	if delivery != workflowDeliveryCommands {
		for _, tool := range tools {
			skillsDir := filepath.Join(root, tool.SkillsDir, "skills")
			for _, skill := range workflowSkills(repo, plan) {
				path := filepath.Join(skillsDir, skill.Name, "SKILL.md")
				if err := writeTextFile(path, skill.Content); err != nil {
					return result, err
				}
				result.SkillFiles = append(result.SkillFiles, cleanGeneratedPath(path))
			}
		}
	}

	if delivery != workflowDeliverySkills {
		commands := workflowCommandContents(repo, plan)
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
	skills := templates.IssueSpecSkills(repo)
	notice := workflowNotice(plan)
	for i := range skills {
		if skills[i].Name == "issue-spec-github" {
			continue
		}
		skills[i].Content = strings.TrimRight(skills[i].Content, "\n") + "\n\n" + notice + "\n"
	}
	return skills
}

func workflowCommandContents(repo string, plan workflow.Plan) []templates.CommandContent {
	commands := templates.IssueSpecCommandContents(repo)
	notice := workflowNotice(plan)
	for i := range commands {
		commands[i].Body = strings.TrimRight(commands[i].Body, "\n") + "\n\n" + notice + "\n"
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
	case "codex":
		return &workflowCommandAdapter{
			FilePath: func(commandID string) (string, error) {
				home, err := codexHome()
				if err != nil {
					return "", err
				}
				return filepath.Join(home, "prompts", fmt.Sprintf("issue-spec-%s.md", commandID)), nil
			},
			Format: formatCodexCommand,
		}
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
