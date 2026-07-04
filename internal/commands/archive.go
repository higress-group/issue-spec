package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/templates"
)

func (a *app) runArchive(ctx context.Context, args []string) int {
	if len(args) < 1 {
		a.errorf("usage: issue-spec archive durable-spec ...\n")
		return 2
	}
	switch args[0] {
	case "durable-spec":
		return a.runArchiveDurableSpec(ctx, args[1:])
	default:
		a.errorf("unknown archive command %q\n", args[0])
		return 2
	}
}

func (a *app) runArchiveDurableSpec(ctx context.Context, args []string) int {
	fs := newFlagSet("archive durable-spec", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	proposalFlag := fs.String("proposal", "", "proposal issue number or URL")
	capability := fs.String("capability", "", "durable spec capability name")
	output := fs.String("output", "", "output spec path")
	purpose := fs.String("purpose", "", "durable spec purpose text")
	createPR := fs.Bool("create-pr", false, "create a separate durable-spec PR from a temporary git worktree")
	branch := fs.String("branch", "", "branch name for --create-pr")
	base := fs.String("base", "main", "base branch for --create-pr")
	title := fs.String("title", "", "pull request title for --create-pr")
	bodyFile := fs.String("body-file", "", "pull request body file for --create-pr")
	draft := fs.Bool("draft", false, "create PR as draft")
	commitMessage := fs.String("commit-message", "", "commit message for --create-pr")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	if strings.TrimSpace(*capability) == "" {
		a.errorf("--capability is required\n")
		return 2
	}
	proposalIssue, err := parseIssueFlag(*proposalFlag, "proposal")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for archive durable-spec on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	issue, specs, err := fetchDurableSpecSources(ctx, client, repo, proposalIssue)
	if err != nil {
		a.errorf("%v\n", err)
		return 1
	}
	outputPath := strings.TrimSpace(*output)
	if outputPath == "" {
		outputPath = filepath.Join("issue-spec", "specs", *capability, "spec.md")
	}
	if *createPR {
		prResult, err := a.createDurableSpecPR(ctx, client, repo, issue.HTMLURL, specs, durableSpecPROptions{
			Capability:    *capability,
			OutputPath:    outputPath,
			Purpose:       *purpose,
			Branch:        *branch,
			Base:          *base,
			Title:         *title,
			BodyFile:      *bodyFile,
			Draft:         *draft,
			CommitMessage: *commitMessage,
		})
		if err != nil {
			a.errorf("create durable spec PR: %v\n", err)
			return 1
		}
		if *jsonOut {
			return a.outputJSON(prResult)
		}
		fmt.Fprintf(a.out, "created durable spec PR: %s\n", prResult["pr_url"])
		return 0
	}
	existing, err := os.ReadFile(outputPath)
	if err != nil && !os.IsNotExist(err) {
		a.errorf("read existing durable spec %s: %v\n", outputPath, err)
		return 1
	}
	rendered, err := templates.DurableSpec(templates.DurableSpecOptions{
		Capability:        *capability,
		Purpose:           *purpose,
		ProposalIssueURL:  issue.HTMLURL,
		ExistingSpecBody:  string(existing),
		SpecificationList: specs,
	})
	if err != nil {
		a.errorf("render durable spec: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		a.errorf("create durable spec directory: %v\n", err)
		return 1
	}
	if err := os.WriteFile(outputPath, []byte(rendered), 0o644); err != nil {
		a.errorf("write durable spec %s: %v\n", outputPath, err)
		return 1
	}
	result := map[string]any{"ok": true, "capability": *capability, "output": outputPath, "proposal": issue.HTMLURL, "spec_count": len(specs)}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "wrote durable spec draft for %s to %s\n", *capability, outputPath)
	return 0
}

type durableSpecPROptions struct {
	Capability    string
	OutputPath    string
	Purpose       string
	Branch        string
	Base          string
	Title         string
	BodyFile      string
	Draft         bool
	CommitMessage string
}

func fetchDurableSpecSources(ctx context.Context, client github.Operations, repo string, proposalIssue int) (github.Issue, []templates.SpecSource, error) {
	issue, err := client.GetIssue(ctx, repo, proposalIssue)
	if err != nil {
		return github.Issue{}, nil, fmt.Errorf("read proposal issue #%d: %w", proposalIssue, err)
	}
	artifacts, err := collectArtifacts(ctx, client, repo, proposalIssue)
	if err != nil {
		return github.Issue{}, nil, fmt.Errorf("read proposal comments: %w", err)
	}
	var specs []templates.SpecSource
	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.Type != "SPEC" || tc.Status == "superseded" {
			continue
		}
		specs = append(specs, templates.SpecSource{ID: tc.ID, URL: artifact.URL, Body: tc.Body})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	if len(specs) == 0 {
		return github.Issue{}, nil, fmt.Errorf("proposal issue #%d has no active SPEC comments", proposalIssue)
	}
	return issue, specs, nil
}

func (a *app) createDurableSpecPR(ctx context.Context, client github.Operations, repo, proposalURL string, specs []templates.SpecSource, opts durableSpecPROptions) (map[string]any, error) {
	if filepath.IsAbs(opts.OutputPath) {
		return nil, fmt.Errorf("--output must be repository-relative when --create-pr is set")
	}
	if strings.TrimSpace(opts.Base) == "" {
		opts.Base = "main"
	}
	if strings.TrimSpace(opts.Branch) == "" {
		opts.Branch = "issue-spec/durable-spec-" + opts.Capability
	}
	if strings.TrimSpace(opts.Title) == "" {
		opts.Title = "docs: archive durable spec for " + opts.Capability
	}
	if strings.TrimSpace(opts.CommitMessage) == "" {
		opts.CommitMessage = opts.Title
	}
	body := fmt.Sprintf("Archive durable spec for `%s` from proposal %s.\n", opts.Capability, proposalURL)
	if strings.TrimSpace(opts.BodyFile) != "" {
		data, err := os.ReadFile(opts.BodyFile)
		if err != nil {
			return nil, err
		}
		body = string(data)
	}
	tmp, err := os.MkdirTemp("", "issue-spec-archive-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := runGit(ctx, "", "fetch", "origin", opts.Base); err != nil {
		return nil, err
	}
	if err := runGit(ctx, "", "worktree", "add", "-B", opts.Branch, tmp, "origin/"+opts.Base); err != nil {
		return nil, err
	}
	defer runGit(context.Background(), "", "worktree", "remove", "--force", tmp)
	outPath := filepath.Join(tmp, filepath.FromSlash(opts.OutputPath))
	existing, err := os.ReadFile(outPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	rendered, err := templates.DurableSpec(templates.DurableSpecOptions{
		Capability:        opts.Capability,
		Purpose:           opts.Purpose,
		ProposalIssueURL:  proposalURL,
		ExistingSpecBody:  string(existing),
		SpecificationList: specs,
	})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(outPath, []byte(rendered), 0o644); err != nil {
		return nil, err
	}
	if err := runGit(ctx, tmp, "add", opts.OutputPath); err != nil {
		return nil, err
	}
	if err := runGit(ctx, tmp, "diff", "--cached", "--quiet"); err == nil {
		return map[string]any{"ok": true, "changed": false, "branch": opts.Branch, "output": opts.OutputPath}, nil
	}
	if err := runGit(ctx, tmp, "commit", "-s", "-m", opts.CommitMessage); err != nil {
		return nil, err
	}
	if err := runGit(ctx, tmp, "push", "-u", "origin", opts.Branch, "--force-with-lease"); err != nil {
		return nil, err
	}
	pr, err := client.CreatePullRequest(ctx, repo, github.CreatePullRequestOptions{
		Title: opts.Title,
		Head:  opts.Branch,
		Base:  opts.Base,
		Body:  body,
		Draft: opts.Draft,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "changed": true, "branch": opts.Branch, "base": opts.Base, "output": opts.OutputPath, "pr": pr.Number, "pr_url": pr.HTMLURL}, nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
