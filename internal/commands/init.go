package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
)

func (a *app) runInit(ctx context.Context, args []string) int {
	fs := newFlagSet("init", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	createLabels := fs.Bool("create-labels", false, "create issue-spec labels")
	tools := fs.String("tools", "", "generate workflow artifacts for AI tools: all, none, or comma-separated codex,claude")
	delivery := fs.String("delivery", "both", "workflow artifact delivery: both, skills, or commands")
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

	workflows, err := writeWorkflowArtifacts(".", *repoFlag, *tools, *delivery)
	if err != nil {
		a.errorf("generate workflow artifacts: %v\n", err)
		return 1
	}

	result := map[string]any{"ok": true, "repo": *repoFlag, "hostname": token.Host, "auth": token, "backend": token.Backend, "config": configPath, "labels": labels, "workflows": workflows}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "initialized issue-spec for %s on %s\nconfig: %s\nauthenticated as: %s (%s)\n", *repoFlag, token.Host, configPath, token.User, token.Source)
	if token.Backend != nil {
		fmt.Fprintf(a.out, "github backend: %s (%s)\n", token.Backend.Name, token.Backend.SelectionSource)
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
