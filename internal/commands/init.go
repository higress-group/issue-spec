package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"gopkg.in/yaml.v3"
)

func (a *app) runInit(ctx context.Context, args []string) int {
	fs := newFlagSet("init", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	createLabels := fs.Bool("create-labels", false, "create issue-spec labels")
	tools := fs.String("tools", "", "generate workflow artifacts for AI tools: all, none, or comma-separated codex,claude")
	delivery := fs.String("delivery", "both", "workflow artifact delivery: both, skills, or commands")
	language := fs.String("language", "", "preferred natural language for generated workflow artifacts (e.g. zh, en, ja); writes rules.language to issue-spec/config.yaml")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
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
	config := map[string]string{"repo": *repoFlag, "hostname": auth.NormalizeHost(*host)}
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

	var languageConfigPath string
	if strings.TrimSpace(*language) != "" {
		languageConfigPath, err = writeWorkflowLanguageConfig(".", *language)
		if err != nil {
			a.errorf("write workflow language config: %v\n", err)
			return 1
		}
	}

	workflows, err := writeWorkflowArtifacts(".", *repoFlag, *tools, *delivery)
	if err != nil {
		a.errorf("generate workflow artifacts: %v\n", err)
		return 1
	}

	result := map[string]any{"ok": true, "repo": *repoFlag, "hostname": token.Host, "auth": token, "backend": token.Backend, "config": configPath, "labels": labels, "workflows": workflows}
	if languageConfigPath != "" {
		result["language"] = languageDisplay(*language)
		result["language_config"] = languageConfigPath
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
	}
	for _, label := range labels {
		if label.Created {
			fmt.Fprintf(a.out, "created label: %s\n", label.Name)
		} else if label.Skipped {
			fmt.Fprintf(a.out, "label exists: %s\n", label.Name)
		}
	}
	if len(workflows.Tools) > 0 {
		fmt.Fprintf(a.out, "workflow delivery: %s\n", workflows.Delivery)
		if len(workflows.SkillFiles) > 0 {
			fmt.Fprintf(a.out, "generated skills: %d\n", len(workflows.SkillFiles))
		}
		if len(workflows.CommandFiles) > 0 {
			fmt.Fprintf(a.out, "generated commands: %d\n", len(workflows.CommandFiles))
		}
		if len(workflows.CommandsSkipped) > 0 {
			fmt.Fprintf(a.out, "commands skipped for: %s (no adapter)\n", strings.Join(workflows.CommandsSkipped, ", "))
		}
		fmt.Fprintln(a.out, "restart your IDE for slash commands to take effect")
	}
	return 0
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
	rules["language_instructions"] = fmt.Sprintf("Write all natural-language content (issue titles, descriptions, rationale, design notes, and QUESTION/REVIEW/VERIFY/scenario prose) in %s. Keep canonical structural tokens in English so validation passes: the `## Requirement:` and `### Scenario:` headings, the `**WHEN**`/`**THEN**` scenario bullets, the MUST/SHALL normative keywords, and typed comment headers.", display)
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
