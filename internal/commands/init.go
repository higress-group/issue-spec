package commands

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"gopkg.in/yaml.v3"
)

const workflowNeutralLanguageGuidance = "configure rules.language in the selected project workflow; for legacy OpenSpec, use openspec/config.yaml"

func (a *app) runInit(ctx context.Context, args []string) int {
	fs := newFlagSet("init", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	serverOrg := fs.String("server-org", "", "self-hosted server organization name or id (defaults to --repo owner)")
	serverRepo := fs.String("server-repo", "", "self-hosted server repository name (defaults to --repo name)")
	providerKey := fs.String("provider", "", "registered external code provider key")
	externalRepo := fs.String("external-repo", "", "stable external code repository identity")
	sourceRemote := fs.String("source-remote", "", "git remote name used for source discovery")
	sourceCloneURL := fs.String("source-clone-url", "", "credential-free canonical HTTPS clone URL")
	sourceWebURL := fs.String("source-web-url", "", "credential-free canonical HTTPS code web URL")
	defaultBranch := fs.String("default-branch", "", "server and source default branch (defaults to main)")
	createIfMissing := fs.Bool("create-if-missing", false, "allow an absent self-hosted repository to be registered")
	bindSource := fs.Bool("bind-source", false, "create or reuse a credential-free source binding")
	skipSourceBinding := fs.Bool("skip-source-binding", false, "disable profile source-binding automation for this invocation")
	yes := fs.Bool("yes", false, "approve the displayed self-hosted remote mutation plan")
	planOnly := fs.Bool("plan", false, "show the self-hosted init plan without mutations or local writes")
	createLabels := fs.Bool("create-labels", true, "ensure issue-spec labels")
	skipLabels := fs.Bool("skip-labels", false, "skip ensuring issue-spec labels")
	tools := fs.String("tools", "", "generate workflow artifacts for AI tools: all, none, or comma-separated codex,claude; explicit none leaves project workflow configuration untouched")
	delivery := fs.String("delivery", "both", "workflow artifact delivery: both, skills, or commands")
	installGlobalPrompts := fs.Bool("install-global-prompts", false, "install user-global Codex prompts (disabled by default; conflicts with explicit --tools none)")
	globalPromptsDir := fs.String("global-prompts-dir", "", "user-global Codex prompt directory; implies --install-global-prompts and conflicts with explicit --tools none")
	globalPromptsDryRun := fs.Bool("global-prompts-dry-run", false, "preview user-global Codex prompt paths without writing them; implies --install-global-prompts and conflicts with explicit --tools none")
	language := fs.String("language", "", "preferred natural language for generated workflow artifacts (e.g. zh, en, ja); not applied with explicit --tools none")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	workflowNeutral := strings.EqualFold(strings.TrimSpace(*tools), "none")
	globalPromptOptionSet := false
	fs.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "install-global-prompts", "global-prompts-dir", "global-prompts-dry-run":
			globalPromptOptionSet = true
		}
	})
	if workflowNeutral && globalPromptOptionSet {
		a.errorf("global Codex prompt options cannot be combined with explicit --tools none; select a workflow tool to generate prompts\n")
		return 2
	}
	if *skipLabels {
		*createLabels = false
	}
	globalPromptOptions, err := resolveGlobalPromptInstallOptions(*installGlobalPrompts, *globalPromptsDir, *globalPromptsDryRun, *delivery)
	if err != nil {
		a.errorf("configure global Codex prompts: %v\n", err)
		return 2
	}
	profile, _, err := auth.ResolveProfile(a.profileName, *host)
	if err != nil {
		a.errorf("resolve init profile: %v\n", err)
		return 1
	}
	hostedOptions := selfHostedInitOptions{Repo: *repoFlag, ServerOrg: *serverOrg, ServerRepo: *serverRepo,
		ProviderKey: *providerKey, ExternalRepo: *externalRepo, SourceRemote: *sourceRemote,
		SourceCloneURL: *sourceCloneURL, SourceWebURL: *sourceWebURL, DefaultBranch: *defaultBranch,
		CreateIfMissing: *createIfMissing, BindSource: *bindSource, SkipSourceBinding: *skipSourceBinding,
		Yes: *yes, PlanOnly: *planOnly, CreateLabels: *createLabels, Tools: *tools, Delivery: *delivery,
		Language: *language, InstallGlobalPrompts: *installGlobalPrompts, GlobalPromptsDir: *globalPromptsDir,
		GlobalPromptsDryRun: *globalPromptsDryRun, JSON: *jsonOut}
	if profile.Kind == auth.ProfileKindHosted {
		return a.runSelfHostedInit(ctx, profile, hostedOptions)
	}
	if hostedOptions.hasSelfHostedOnlyFlags() {
		a.errorf("self-hosted init flags require a self-hosted --profile\n")
		return 2
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	client, token, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for init on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	user, scopes, err := client.GetUser(ctx)
	if err != nil {
		a.errorf("validate auth: %v\n", err)
		return 1
	}
	token.User = user.Login
	token.Scopes = scopes

	configPath := filepath.Join(".issue-spec", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		a.errorf("create .issue-spec: %v\n", err)
		return 1
	}
	config := map[string]any{"version": 1, "repo": *repoFlag, "hostname": auth.NormalizeHost(*host), "profile": auth.DefaultProfileName}
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(configPath, append(data, '\n'), 0o644); err != nil {
		a.errorf("write %s: %v\n", configPath, err)
		return 1
	}

	var labels []github.LabelResult
	if *createLabels {
		for _, label := range issueSpecLabels() {
			result, err := client.CreateLabel(ctx, repo, label.name, label.color, label.description)
			if err != nil {
				a.errorf("create label %s: %v\n", label.name, err)
				return 1
			}
			labels = append(labels, result)
		}
	}

	languageRequested := strings.TrimSpace(*language) != ""
	var languageConfigPath string
	if languageRequested && !workflowNeutral {
		languageConfigPath, err = writeWorkflowLanguageConfig(".", *language)
		if err != nil {
			a.errorf("write workflow language config: %v\n", err)
			return 1
		}
	}

	workflows := workflowGenerationResult{Delivery: *delivery}
	if !workflowNeutral {
		workflows, err = writeWorkflowArtifacts(".", *repoFlag, *tools, *delivery)
		if err != nil {
			a.errorf("generate workflow artifacts: %v\n", err)
			return 1
		}
		if err := installGlobalCodexPrompts(".", *repoFlag, nil, globalPromptOptions, &workflows); err != nil {
			a.errorf("install global Codex prompts: %v\n", err)
			return 1
		}
	}

	result := map[string]any{"ok": true, "repo": *repoFlag, "hostname": token.Host, "auth": token, "backend": token.Backend, "config": configPath, "labels": labels, "workflows": workflows}
	if languageConfigPath != "" {
		result["language"] = languageDisplay(*language)
		result["language_config"] = languageConfigPath
	} else if languageRequested && workflowNeutral {
		result["language"] = languageDisplay(*language)
		result["language_applied"] = false
		result["language_guidance"] = workflowNeutralLanguageGuidance
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "initialized issue-spec for %s on %s\nconfig: %s\nauthenticated as: %s (%s)\n", *repoFlag, token.Host, configPath, token.User, token.Source)
	if token.Backend != nil {
		fmt.Fprintf(a.out, "github backend: %s (%s)\n", token.Backend.Name, token.Backend.SelectionSource)
	}
	if languageConfigPath != "" {
		fmt.Fprintf(a.out, "workflow language: %s (%s)\n", languageDisplay(*language), languageConfigPath)
	} else if languageRequested && workflowNeutral {
		fmt.Fprintf(a.out, "workflow language not applied: %s (--tools none); %s\n", languageDisplay(*language), workflowNeutralLanguageGuidance)
	}
	for _, label := range labels {
		if label.Created {
			fmt.Fprintf(a.out, "created label: %s\n", label.Name)
		} else if label.Skipped {
			fmt.Fprintf(a.out, "label exists: %s\n", label.Name)
		}
	}
	if len(workflows.Tools) > 0 {
		a.printWorkflowGeneration(workflows)
	} else if len(workflows.GlobalPromptFiles) > 0 {
		a.printWorkflowGeneration(workflows)
	}
	return 0
}

func resolveGlobalPromptInstallOptions(install bool, directory string, dryRun bool, delivery string) (globalPromptInstallOptions, error) {
	options := globalPromptInstallOptions{
		Enabled:   install || strings.TrimSpace(directory) != "" || dryRun,
		Directory: directory,
		DryRun:    dryRun,
	}
	if !options.Enabled {
		return options, nil
	}
	normalizedDelivery, err := normalizeWorkflowDelivery(delivery)
	if err != nil {
		return globalPromptInstallOptions{}, err
	}
	if normalizedDelivery == workflowDeliverySkills {
		return globalPromptInstallOptions{}, fmt.Errorf("--install-global-prompts requires --delivery both or commands")
	}
	return options, nil
}

func (a *app) printWorkflowGeneration(workflows workflowGenerationResult) {
	fmt.Fprintf(a.out, "workflow delivery: %s\n", workflows.Delivery)
	if len(workflows.SkillFiles) > 0 {
		fmt.Fprintf(a.out, "repository skills: %d\n", len(workflows.SkillFiles))
	}
	if len(workflows.SkillResourceFiles) > 0 {
		fmt.Fprintf(a.out, "repository skill resources: %d\n", len(workflows.SkillResourceFiles))
	}
	if len(workflows.SkillLinks) > 0 {
		fmt.Fprintf(a.out, "repository skill links:\n")
		for _, link := range workflows.SkillLinks {
			fmt.Fprintf(a.out, "  %s\n", link)
		}
	}
	if len(workflows.CommandFiles) > 0 {
		fmt.Fprintf(a.out, "repository commands: %d\n", len(workflows.CommandFiles))
	}
	if len(workflows.CommandsSkipped) > 0 {
		fmt.Fprintf(a.out, "repository commands skipped for: %s (no repository command adapter)\n", strings.Join(workflows.CommandsSkipped, ", "))
	}
	if len(workflows.GlobalPromptFiles) > 0 {
		label := "installed user-global prompts"
		if workflows.GlobalPromptsDryRun {
			label = "user-global prompt dry-run"
		}
		fmt.Fprintf(a.out, "%s:\n", label)
		for _, path := range workflows.GlobalPromptFiles {
			fmt.Fprintf(a.out, "  %s\n", path)
		}
	}
	if len(workflows.CommandFiles) > 0 || (len(workflows.GlobalPromptFiles) > 0 && !workflows.GlobalPromptsDryRun) {
		fmt.Fprintln(a.out, "restart your IDE for slash commands to take effect")
	}
}

// writeWorkflowLanguageConfig creates or updates issue-spec/config.yaml so that
// generated workflow artifacts instruct agents to author natural-language content
// in the requested language while keeping canonical structural tokens in English.
func writeWorkflowLanguageConfig(root, language string) (string, error) {
	display := languageDisplay(language)
	path := filepath.Join(root, "issue-spec", "config.yaml")

	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return "", fmt.Errorf("parse existing %s: %w", filepath.ToSlash(path), err)
		}
		if cfg == nil {
			cfg = map[string]any{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	rules, _ := cfg["rules"].(map[string]any)
	if rules == nil {
		rules = map[string]any{}
	}
	rules["language"] = display
	stagedTitleInstructions := "For Proposal, Design, and Implement issue titles, use the standardized English title family: `Proposal: <change-name>`, `Design: <change-name>`, and `Implement: <change-name>`."
	if display != "English" {
		stagedTitleInstructions = fmt.Sprintf("For Proposal, Design, and Implement issue titles, keep the English stage prefix (`Proposal:`, `Design:`, or `Implement:`) and write the subject after that prefix in %s; pass an explicit `--title` for these staged issues and do not rely on the CLI-derived title.", display)
	}
	rules["language_instructions"] = fmt.Sprintf("Write natural-language body content (descriptions, rationale, design notes, questions, decisions, task descriptions, and QUESTION/scenario prose) in %[1]s. %[2]s Ordinary (non-staged) issue titles use a descriptive %[1]s title. Keep canonical structural tokens in English so validation passes: the `## Requirement:` and `### Scenario:` headings, the `**WHEN**`/`**THEN**` scenario bullets, the MUST/SHALL normative keywords, and typed comment headers.", display, stagedTitleInstructions)
	cfg["rules"] = rules

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return "", err
	}
	return filepath.ToSlash(path), nil
}

// languageDisplay maps common language codes to a descriptive label while
// passing through any other value unchanged.
func languageDisplay(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "zh", "zh-cn", "zh_cn", "chinese", "中文", "简体中文":
		return "Simplified Chinese (简体中文)"
	case "zh-tw", "zh_tw", "traditional chinese", "繁體中文":
		return "Traditional Chinese (繁體中文)"
	case "en", "english":
		return "English"
	case "ja", "jp", "japanese", "日本語":
		return "Japanese (日本語)"
	case "ko", "korean", "한국어":
		return "Korean (한국어)"
	default:
		return strings.TrimSpace(value)
	}
}

type labelSpec struct {
	name        string
	color       string
	description string
}

func issueSpecLabels() []labelSpec {
	return []labelSpec{
		{"issue-spec/proposal", "0969da", "Issue-native proposal artifact"},
		{"issue-spec/design", "8250df", "Issue-native design artifact"},
		{"issue-spec/implement", "1a7f37", "Issue-native implementation coordination"},
		{"issue-spec/question", "fbca04", "Blocking or non-blocking workflow question"},
		{"issue-spec/review", "cf222e", "Review gate or finding"},
		{"issue-spec/verify", "57606a", "Verification evidence"},
	}
}
