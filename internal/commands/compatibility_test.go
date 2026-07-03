package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

func TestCompatibilityInitJSONPreservesLegacyFieldsAndAddsBackend(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{
			Mode:            auth.GitHubBackendModeAuto,
			Name:            auth.GitHubBackendNameREST,
			Kind:            auth.GitHubBackendKindREST,
			Host:            "github.com",
			SelectionSource: "auto:token",
			TokenSource:     "env:ISSUE_SPEC_TOKEN",
			Token:           auth.Token{Value: "secret-token", Source: "env:ISSUE_SPEC_TOKEN", Host: "github.com"},
		}, nil
	}
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return fakeGitHubBackend{
			info:   github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			user:   github.User{Login: "octocat"},
			scopes: []string{"repo"},
		}, nil
	}

	code := app.runInit(context.Background(), []string{"--repo", "o/r", "--tools", "none", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if strings.Contains(out.String(), "secret-token") || strings.Contains(errOut.String(), "secret-token") {
		t.Fatalf("token leaked in init output: stdout=%q stderr=%q", out.String(), errOut.String())
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ok", "repo", "hostname", "auth", "backend", "config", "labels", "workflows"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("init JSON missing compatibility field %q in %s", key, out.String())
		}
	}

	var got struct {
		OK       bool                          `json:"ok"`
		Repo     string                        `json:"repo"`
		Hostname string                        `json:"hostname"`
		Auth     auth.Token                    `json:"auth"`
		Backend  auth.GitHubBackendDiagnostics `json:"backend"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Repo != "o/r" || got.Hostname != "github.com" {
		t.Fatalf("legacy init fields changed: %+v", got)
	}
	if got.Auth.Source != "env:ISSUE_SPEC_TOKEN" || got.Auth.User != "octocat" || got.Auth.Host != "github.com" || len(got.Auth.Scopes) != 1 || got.Auth.Scopes[0] != "repo" {
		t.Fatalf("legacy auth fields changed: %+v", got.Auth)
	}
	if got.Backend.Name != "rest" || got.Backend.Kind != "rest" || got.Backend.SelectionSource != "auto:token" || got.Backend.TokenSource != "env:ISSUE_SPEC_TOKEN" {
		t.Fatalf("backend diagnostics not additive REST metadata: %+v", got.Backend)
	}
}

func TestCompatibilityCommentListPreservesTypedCommentLinksWithGHBackend(t *testing.T) {
	const taskURL = "https://github.com/o/r/issues/14#issuecomment-4851241573"
	body, err := model.EnsureTypedBody("PROCESS", "PROCESS-008", "## Evidence\n\nCompatibility checks remain explicit.\n", model.BodyOptions{
		Agent:  "Compatibility Worker",
		Status: "in-progress",
		Scope:  "migration compatibility verification",
		Links:  map[string][]string{"Related Comments": []string{taskURL}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			listIssueComments: func(_ context.Context, repo string, issueNumber int) ([]github.Comment, error) {
				if repo != "o/r" || issueNumber != 16 {
					t.Fatalf("unexpected comment list args repo=%q issue=%d", repo, issueNumber)
				}
				return []github.Comment{{
					ID:      88,
					HTMLURL: "https://github.com/o/r/issues/16#issuecomment-88",
					URL:     "https://api.github.com/repos/o/r/issues/comments/88",
					Body:    body,
				}}, nil
			},
		}, nil
	}

	code := app.runCommentList(context.Background(), []string{"--repo", "o/r", "--issue", "16", "--type", "PROCESS", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	var got struct {
		OK       bool             `json:"ok"`
		Issue    int              `json:"issue"`
		Comments []model.Artifact `json:"comments"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Issue != 16 || len(got.Comments) != 1 {
		t.Fatalf("unexpected comment list output: %+v", got)
	}
	tc := got.Comments[0].Comment
	if tc.Marker.Type != "PROCESS" || tc.Marker.ID != "PROCESS-008" || tc.Type != "PROCESS" || tc.ID != "PROCESS-008" {
		t.Fatalf("typed marker/header changed: %+v", tc)
	}
	if tc.Agent != "Compatibility Worker" || tc.Status != "in-progress" || tc.Scope != "migration compatibility verification" || !tc.HasHead {
		t.Fatalf("typed header changed: %+v", tc)
	}
	if len(tc.Errors) != 0 {
		t.Fatalf("typed comment parse errors: %+v", tc.Errors)
	}
	if links := tc.Links["Related Comments"]; len(links) != 1 || links[0] != taskURL {
		t.Fatalf("related comments links changed: %+v", tc.Links)
	}
}

func TestCompatibilityArchiveCreatePRKeepsLocalGitOperations(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary is required for local archive compatibility test: %v", err)
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ghMarker := filepath.Join(root, "gh-called")
	fakeGH := filepath.Join(binDir, "gh")
	if err := os.WriteFile(fakeGH, []byte("#!/bin/sh\n: > \"$GH_MARKER\"\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_MARKER", ghMarker)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	runTestGit(t, "", "init", "--bare", origin)
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, seed, "init")
	runTestGit(t, seed, "config", "user.email", "issue-spec@example.test")
	runTestGit(t, seed, "config", "user.name", "Issue Spec Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, seed, "add", "README.md")
	runTestGit(t, seed, "commit", "-m", "seed")
	runTestGit(t, seed, "branch", "-M", "main")
	runTestGit(t, seed, "remote", "add", "origin", origin)
	runTestGit(t, seed, "push", "-u", "origin", "main")
	t.Chdir(seed)

	var gotPR github.CreatePullRequestOptions
	client := archiveCompatibilityClient{
		createPullRequest: func(_ context.Context, repo string, opts github.CreatePullRequestOptions) (github.PullRequest, error) {
			if repo != "o/r" {
				t.Fatalf("repo = %q, want o/r", repo)
			}
			gotPR = opts
			return github.PullRequest{Number: 42, HTMLURL: "https://github.com/o/r/pull/42"}, nil
		},
	}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	result, err := app.createDurableSpecPR(context.Background(), client, "o/r", "https://github.com/o/r/issues/9", []templates.SpecSource{{
		ID:   "SPEC-001",
		URL:  "https://github.com/o/r/issues/9#issuecomment-1",
		Body: "## Requirement: Durable archive compatibility\n\nThe archive command MUST keep local git operations local.\n\n### Scenario: Create durable archive PR\n\n- **WHEN** the coordinator creates the durable archive PR\n- **THEN** issue-spec uses local git for worktree changes.\n",
	}}, durableSpecPROptions{
		Capability: "compat",
		OutputPath: "openspec/specs/compat/spec.md",
		Branch:     "issue-spec/durable-spec-compat",
		Base:       "main",
		Title:      "docs: archive compat spec",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["changed"] != true || result["pr"] != 42 || result["branch"] != "issue-spec/durable-spec-compat" {
		t.Fatalf("unexpected durable PR result: %+v", result)
	}
	if gotPR.Head != "issue-spec/durable-spec-compat" || gotPR.Base != "main" || gotPR.Title != "docs: archive compat spec" {
		t.Fatalf("unexpected PR options: %+v", gotPR)
	}
	runTestGit(t, "", "--git-dir", origin, "rev-parse", "refs/heads/issue-spec/durable-spec-compat")
	if _, err := os.Stat(ghMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive path invoked gh; marker stat err=%v", err)
	}
}

func TestCompatibilityCLIAndSandboxCrossCompileWithoutCgo(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go binary is required for cross-compile compatibility test: %v", err)
	}
	root := filepath.Clean(filepath.Join("..", ".."))
	outDir := t.TempDir()
	targets := []struct {
		name string
		pkg  string
		goos string
	}{
		{name: "cli-linux", pkg: "./cmd/issue-spec", goos: "linux"},
		{name: "cli-darwin", pkg: "./cmd/issue-spec", goos: "darwin"},
		{name: "cli-windows", pkg: "./cmd/issue-spec", goos: "windows"},
		{name: "sandbox-linux", pkg: "./internal/sandbox", goos: "linux"},
		{name: "sandbox-darwin", pkg: "./internal/sandbox", goos: "darwin"},
		{name: "sandbox-windows", pkg: "./internal/sandbox", goos: "windows"},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			output := filepath.Join(outDir, target.name+".test")
			if target.goos == "windows" {
				output += ".exe"
			}
			cmd := exec.Command("go", "test", "-c", target.pkg, "-o", output)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOOS="+target.goos, "GOARCH=amd64", "CGO_ENABLED=0")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("cross compile %s for %s failed: %v\n%s", target.pkg, target.goos, err, strings.TrimSpace(string(out)))
			}
		})
	}

	linuxImpl, err := os.ReadFile(filepath.Join(root, "internal", "sandbox", "bwrap_linux.go"))
	if err != nil {
		t.Fatal(err)
	}
	unsupportedImpl, err := os.ReadFile(filepath.Join(root, "internal", "sandbox", "bwrap_unsupported.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(linuxImpl), "//go:build linux") || !strings.Contains(string(unsupportedImpl), "//go:build !linux") {
		t.Fatal("sandbox bwrap implementations must keep explicit Linux and non-Linux build tags")
	}
}

type archiveCompatibilityClient struct {
	fakeGitHubBackend
	createPullRequest func(context.Context, string, github.CreatePullRequestOptions) (github.PullRequest, error)
}

func (c archiveCompatibilityClient) CreatePullRequest(ctx context.Context, repo string, opts github.CreatePullRequestOptions) (github.PullRequest, error) {
	if c.createPullRequest != nil {
		return c.createPullRequest(ctx, repo, opts)
	}
	return github.PullRequest{}, errors.New("unused")
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}
