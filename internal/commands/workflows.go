package commands

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/buildinfo"
	"github.com/higress-group/issue-spec/internal/templates"
	"github.com/higress-group/issue-spec/internal/workflow"
)

const (
	workflowDeliveryBoth     = "both"
	workflowDeliverySkills   = "skills"
	workflowDeliveryCommands = "commands"
)

type workflowTool struct {
	ID      string
	RootDir string
}

type workflowCommandAdapter struct {
	FilePath func(commandID string) (string, error)
	Format   func(templates.CommandContent) string
}

type workflowGenerationResult struct {
	Delivery            string   `json:"delivery"`
	Tools               []string `json:"tools"`
	SkillFiles          []string `json:"skillFiles,omitempty"`
	SkillResourceFiles  []string `json:"skillResourceFiles,omitempty"`
	SkillLinks          []string `json:"skillLinks,omitempty"`
	CommandFiles        []string `json:"commandFiles,omitempty"`
	PrunedFiles         []string `json:"prunedFiles,omitempty"`
	CommandsSkipped     []string `json:"commandsSkipped,omitempty"`
	GlobalPromptFiles   []string `json:"globalPromptFiles,omitempty"`
	GlobalPromptsDryRun bool     `json:"globalPromptsDryRun,omitempty"`
	WorkflowSource      string   `json:"workflowSource,omitempty"`
	WorkflowSchema      string   `json:"workflowSchema,omitempty"`
}

const workflowReleaseManifestSchema = "issue-spec.generated-workflow/v1"

type workflowReleaseFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type workflowReleaseManifest struct {
	Schema                     string                `json:"schema"`
	Release                    string                `json:"release"`
	SourceRevision             string                `json:"source_revision"`
	RequirementsSkillContentID string                `json:"requirements_skill_content_id"`
	ContentDigest              string                `json:"content_digest"`
	Files                      []workflowReleaseFile `json:"files"`
}

type globalPromptInstallOptions struct {
	Enabled   bool
	Directory string
	DryRun    bool
}

var workflowTools = []workflowTool{
	{ID: "codex", RootDir: ".agents"},
	{ID: "claude", RootDir: ".claude"},
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
	if delivery != workflowDeliveryCommands && workflowToolSelected(tools, "claude") {
		if err := validateClaudeSkillsLinkMigration(root); err != nil {
			return result, err
		}
	}
	pruned, err := pruneManagedWorkflowAssets(root, delivery, tools, provider != nil)
	if err != nil {
		return result, err
	}
	result.PrunedFiles = pruned
	htmlReviewPruned, err := pruneManagedHTMLReviewReference(root, delivery, plan.HTMLReviewEnabled())
	if err != nil {
		return result, err
	}
	result.PrunedFiles = append(result.PrunedFiles, htmlReviewPruned...)

	if delivery != workflowDeliveryCommands {
		skillsDir := filepath.Join(root, ".agents", "skills")
		for _, skill := range workflowSkillsWithProvider(repo, plan, provider) {
			skillDir := filepath.Join(skillsDir, skill.Name)
			path := filepath.Join(skillDir, "SKILL.md")
			if err := writeTextFile(path, skill.Content); err != nil {
				return result, err
			}
			result.SkillFiles = append(result.SkillFiles, cleanGeneratedPath(path))
			for _, resource := range skill.Resources {
				resourcePath, err := skillResourcePath(skillDir, resource.Path)
				if err != nil {
					return result, fmt.Errorf("render skill %s resource: %w", skill.Name, err)
				}
				if err := writeTextFile(resourcePath, resource.Content); err != nil {
					return result, err
				}
				result.SkillResourceFiles = append(result.SkillResourceFiles, cleanGeneratedPath(resourcePath))
			}
		}
		if workflowToolSelected(tools, "claude") {
			link, err := installClaudeSkillsLink(root)
			if err != nil {
				return result, err
			}
			result.SkillLinks = append(result.SkillLinks, link)
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
	if delivery != workflowDeliveryCommands {
		manifestPath := filepath.Join(root, ".agents", "skills", "issue-spec-workflow", "release.json")
		manifest, err := buildWorkflowReleaseManifest(root, result)
		if err != nil {
			return result, err
		}
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return result, err
		}
		if err := writeTextFile(manifestPath, string(append(data, '\n'))); err != nil {
			return result, err
		}
		result.SkillResourceFiles = append(result.SkillResourceFiles, cleanGeneratedPath(manifestPath))
	}

	return result, nil
}

func buildWorkflowReleaseManifest(root string, result workflowGenerationResult) (workflowReleaseManifest, error) {
	paths := append([]string(nil), result.SkillFiles...)
	paths = append(paths, result.SkillResourceFiles...)
	paths = append(paths, result.CommandFiles...)
	sort.Strings(paths)
	files := make([]workflowReleaseFile, 0, len(paths))
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return workflowReleaseManifest{}, fmt.Errorf("resolve workflow root: %w", err)
	}
	for _, generatedPath := range paths {
		path := filepath.FromSlash(generatedPath)
		if !filepath.IsAbs(path) {
			path, err = filepath.Abs(path)
			if err != nil {
				return workflowReleaseManifest{}, fmt.Errorf("resolve generated workflow asset %s: %w", generatedPath, err)
			}
		}
		relative, err := filepath.Rel(absoluteRoot, path)
		if err != nil || !filepath.IsLocal(relative) {
			return workflowReleaseManifest{}, fmt.Errorf("generated workflow asset %s is outside workflow root", generatedPath)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return workflowReleaseManifest{}, fmt.Errorf("read generated workflow asset %s: %w", relative, err)
		}
		digest := sha256.Sum256(body)
		files = append(files, workflowReleaseFile{Path: filepath.ToSlash(relative), SHA256: fmt.Sprintf("%x", digest[:])})
	}
	identity := buildinfo.Current()
	manifest := workflowReleaseManifest{Schema: workflowReleaseManifestSchema, Release: identity.Version,
		SourceRevision: identity.Revision, RequirementsSkillContentID: identity.RequirementsSkillContentID, Files: files}
	manifest.ContentDigest = workflowReleaseContentDigest(files)
	return manifest, nil
}

func workflowReleaseContentDigest(files []workflowReleaseFile) string {
	hash := sha256.New()
	for _, file := range files {
		fmt.Fprintf(hash, "%s\x00%s\n", file.Path, file.SHA256)
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func readWorkflowReleaseManifest(root, path string) (workflowReleaseManifest, error) {
	if strings.TrimSpace(path) == "" {
		path = filepath.Join(root, ".agents", "skills", "issue-spec-workflow", "release.json")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return workflowReleaseManifest{}, fmt.Errorf("read generated workflow release manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var manifest workflowReleaseManifest
	if err := decoder.Decode(&manifest); err != nil {
		return workflowReleaseManifest{}, fmt.Errorf("decode generated workflow release manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return workflowReleaseManifest{}, fmt.Errorf("decode generated workflow release manifest: trailing content")
	}
	if manifest.Schema != workflowReleaseManifestSchema || strings.TrimSpace(manifest.Release) == "" ||
		strings.TrimSpace(manifest.SourceRevision) == "" || len(manifest.Files) == 0 {
		return workflowReleaseManifest{}, fmt.Errorf("generated workflow release manifest identity is incomplete")
	}
	seen := map[string]bool{}
	for _, file := range manifest.Files {
		if file.Path == "" || filepath.IsAbs(file.Path) || !filepath.IsLocal(filepath.FromSlash(file.Path)) || seen[file.Path] {
			return workflowReleaseManifest{}, fmt.Errorf("generated workflow release manifest contains an invalid path")
		}
		seen[file.Path] = true
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.Path)))
		if err != nil {
			return workflowReleaseManifest{}, fmt.Errorf("read generated workflow asset %s: %w", file.Path, err)
		}
		digest := sha256.Sum256(body)
		if file.SHA256 != fmt.Sprintf("%x", digest[:]) {
			return workflowReleaseManifest{}, fmt.Errorf("generated workflow asset %s digest mismatch", file.Path)
		}
	}
	if manifest.ContentDigest != workflowReleaseContentDigest(manifest.Files) {
		return workflowReleaseManifest{}, fmt.Errorf("generated workflow release manifest content digest mismatch")
	}
	return manifest, nil
}

func skillResourcePath(skillDir, relative string) (string, error) {
	relative = filepath.FromSlash(strings.TrimSpace(relative))
	if relative == "" || filepath.IsAbs(relative) || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("resource path %q must stay inside the skill directory", relative)
	}
	return filepath.Join(skillDir, relative), nil
}

func workflowToolSelected(tools []workflowTool, id string) bool {
	for _, tool := range tools {
		if tool.ID == id {
			return true
		}
	}
	return false
}

func validateClaudeSkillsLinkMigration(root string) error {
	claudeSkills := filepath.Join(root, ".claude", "skills")
	info, err := os.Lstat(claudeSkills)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Claude skills path %s: %w", claudeSkills, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		matches, target, err := symlinkMatchesPath(claudeSkills, filepath.Join(root, ".agents", "skills"))
		if err != nil {
			return fmt.Errorf("inspect Claude skills link %s: %w", claudeSkills, err)
		}
		if !matches {
			return fmt.Errorf("Claude skills link %s targets %q; expected ../.agents/skills", claudeSkills, target)
		}
		return nil
	}
	if !info.IsDir() {
		return fmt.Errorf("Claude skills path %s must be a directory or a symlink to ../.agents/skills", claudeSkills)
	}

	entries, err := os.ReadDir(claudeSkills)
	if err != nil {
		return fmt.Errorf("inspect Claude skills directory %s: %w", claudeSkills, err)
	}
	agentsSkills := filepath.Join(root, ".agents", "skills")
	for _, entry := range entries {
		source := filepath.Join(claudeSkills, entry.Name())
		target := filepath.Join(agentsSkills, entry.Name())
		if !entry.IsDir() {
			return fmt.Errorf("Claude skills directory contains non-directory entry %s; move it before init can create the shared skills link", source)
		}
		if managedIssueSpecSkillDirectory(source) {
			continue
		}
		covered, err := directoryTreeCoveredByTarget(source, target)
		if err != nil {
			return fmt.Errorf("compare Claude skill %s with canonical skills: %w", source, err)
		}
		if !covered {
			return fmt.Errorf("Claude skill %s differs from or is missing in .agents/skills; reconcile it manually before init can replace .claude/skills with a symlink", source)
		}
	}
	return nil
}

func installClaudeSkillsLink(root string) (string, error) {
	if err := validateClaudeSkillsLinkMigration(root); err != nil {
		return "", err
	}
	claudeDir := filepath.Join(root, ".claude")
	claudeSkills := filepath.Join(claudeDir, "skills")
	linkTarget := filepath.Join("..", ".agents", "skills")
	info, err := os.Lstat(claudeSkills)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return workflowLinkDescription(root, claudeSkills, linkTarget)
	}
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect Claude skills path %s: %w", claudeSkills, err)
	}
	if err == nil {
		if err := os.RemoveAll(claudeSkills); err != nil {
			return "", fmt.Errorf("remove safely migrated Claude skills directory %s: %w", claudeSkills, err)
		}
	}
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return "", fmt.Errorf("create Claude workflow directory %s: %w", claudeDir, err)
	}
	if err := os.Symlink(linkTarget, claudeSkills); err != nil {
		return "", fmt.Errorf("link Claude skills %s to %s: %w", claudeSkills, linkTarget, err)
	}
	return workflowLinkDescription(root, claudeSkills, linkTarget)
}

func workflowLinkDescription(root, linkPath, target string) (string, error) {
	relative, err := filepath.Rel(root, linkPath)
	if err != nil {
		return "", fmt.Errorf("describe workflow link %s: %w", linkPath, err)
	}
	return filepath.ToSlash(relative) + " -> " + filepath.ToSlash(target), nil
}

func symlinkMatchesPath(linkPath, expectedPath string) (bool, string, error) {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return false, "", err
	}
	resolved := target
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(linkPath), resolved)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return false, target, err
	}
	expectedPath, err = filepath.Abs(expectedPath)
	if err != nil {
		return false, target, err
	}
	return filepath.Clean(resolved) == filepath.Clean(expectedPath), target, nil
}

func managedIssueSpecSkillDirectory(path string) bool {
	body, err := os.ReadFile(filepath.Join(path, "SKILL.md"))
	if err != nil {
		return false
	}
	return strings.Contains(string(body), `generatedBy: "issue-spec"`)
}

func directoryTreeCoveredByTarget(source, target string) (bool, error) {
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	}
	covered := true
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !covered {
			return filepath.SkipAll
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		counterpart := filepath.Join(target, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			covered = false
			return nil
		}
		if entry.IsDir() {
			info, err := os.Stat(counterpart)
			if err != nil || !info.IsDir() {
				if err != nil && !os.IsNotExist(err) {
					return err
				}
				covered = false
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			covered = false
			return nil
		}
		sourceBody, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		targetBody, err := os.ReadFile(counterpart)
		if os.IsNotExist(err) {
			covered = false
			return nil
		}
		if err != nil {
			return err
		}
		if string(sourceBody) != string(targetBody) {
			covered = false
		}
		return nil
	})
	return covered, err
}

func pruneManagedWorkflowAssets(root, delivery string, tools []workflowTool, keepProvider bool) ([]string, error) {
	type candidate struct {
		path string
		name string
	}
	retired := []string{"archive", "review", "verify"}
	if !keepProvider {
		retired = append(retired, "code-provider")
	}
	var candidates []candidate
	if delivery != workflowDeliveryCommands {
		for _, name := range retired {
			candidates = append(candidates, candidate{path: filepath.Join(root, ".agents", "skills", "issue-spec-"+name, "SKILL.md"), name: name})
			if workflowToolSelected(tools, "claude") {
				candidates = append(candidates, candidate{path: filepath.Join(root, ".claude", "skills", "issue-spec-"+name, "SKILL.md"), name: name})
			}
		}
	}
	if delivery != workflowDeliverySkills {
		for _, tool := range tools {
			if tool.ID == "claude" {
				for _, name := range retired {
					candidates = append(candidates, candidate{path: filepath.Join(root, ".claude", "commands", "issue-spec", name+".md"), name: name})
				}
			}
		}
	}
	var pruned []string
	for _, candidate := range candidates {
		body, err := os.ReadFile(candidate.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect managed retired workflow asset %s: %w", candidate.path, err)
		}
		content := string(body)
		title := strings.ToUpper(candidate.name[:1]) + candidate.name[1:]
		managedSkill := strings.Contains(content, `generatedBy: "issue-spec"`) && strings.Contains(content, "name: issue-spec-"+candidate.name+"\n")
		managedCommand := strings.Contains(content, `name: "Issue Spec: `+title+`"`) &&
			strings.Contains(content, `category: "Workflow"`) && strings.Contains(content, "# Issue Spec "+title+"\n")
		if !managedSkill && !managedCommand {
			continue
		}
		if err := os.Remove(candidate.path); err != nil {
			return nil, fmt.Errorf("prune managed retired workflow asset %s: %w", candidate.path, err)
		}
		pruned = append(pruned, cleanGeneratedPath(candidate.path))
	}
	return pruned, nil
}

func pruneManagedHTMLReviewReference(root, delivery string, enabled bool) ([]string, error) {
	if enabled || delivery == workflowDeliveryCommands {
		return nil, nil
	}
	skillDir := filepath.Join(root, ".agents", "skills", "issue-spec-workflow")
	path := filepath.Join(skillDir, "references", "human-review-projections.md")
	if !managedIssueSpecSkillDirectory(skillDir) {
		return nil, nil
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect managed HTML review reference %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("prune managed HTML review reference %s: %w", path, err)
	}
	return []string{cleanGeneratedPath(path)}, nil
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
	options := templates.WorkflowAuthoringOptions{HTMLReviewEnabled: plan.HTMLReviewEnabled()}
	skills := templates.IssueSpecSkillsWithOptions(repo, options)
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
	options := templates.WorkflowAuthoringOptions{HTMLReviewEnabled: plan.HTMLReviewEnabled()}
	commands := templates.IssueSpecCommandContentsWithOptions(repo, options)
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
		if _, err := os.Stat(filepath.Join(root, tool.RootDir)); err == nil {
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
