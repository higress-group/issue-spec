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
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

func TestCompatibilityInitJSONPreservesLegacyFieldsAndAddsBackend(t *testing.T) {
	chdirGitHubProfileProject(t)
	var labelNames []string
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return fakeGitHubBackend{
			info:   github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			user:   github.User{Login: "octocat"},
			scopes: []string{"repo"},
			createLabel: func(_ context.Context, repo, name, _, _ string) (github.LabelResult, error) {
				if repo != "o/r" {
					t.Fatalf("label repo = %q", repo)
				}
				labelNames = append(labelNames, name)
				return github.LabelResult{Name: name, Created: true}, nil
			},
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
	if got.Backend.Name != "rest" || got.Backend.Kind != "rest" || got.Backend.SelectionSource != "profile:project" || got.Backend.TokenSource != "env:ISSUE_SPEC_TOKEN" {
		t.Fatalf("backend diagnostics not additive REST metadata: %+v", got.Backend)
	}
	if len(labelNames) != len(issueSpecLabels()) {
		t.Fatalf("default label calls = %v, want %d", labelNames, len(issueSpecLabels()))
	}
}

func TestCompatibilityInitCanDisableDefaultLabels(t *testing.T) {
	for _, flag := range []string{"--skip-labels", "--create-labels=false"} {
		t.Run(flag, func(t *testing.T) {
			chdirGitHubProfileProject(t)
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
				return fakeGitHubBackend{user: github.User{Login: "octocat"}}, nil
			}

			if code := app.runInit(context.Background(), []string{"--repo", "o/r", "--tools", "none", flag}); code != 0 {
				t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
			}
		})
	}
}

func TestProjectGitHubProfilePreservesInjectedBackendSelectors(t *testing.T) {
	chdirGitHubProfileProject(t)
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	var commandCalls int
	app.selectGitHubBackend = func(_ context.Context, host string) (auth.GitHubBackendSelection, error) {
		commandCalls++
		return auth.GitHubBackendSelection{
			Mode:            auth.GitHubBackendModeAuto,
			Name:            auth.GitHubBackendNameGH,
			Kind:            auth.GitHubBackendKindCLI,
			Host:            host,
			SelectionSource: "test:command",
		}, nil
	}
	selection, err := app.selectBackend(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	if commandCalls != 1 || selection.ProfileSource != "project" || selection.SelectionSource != "profile:project" || selection.Token.Profile != auth.DefaultProfileName {
		t.Fatalf("command selection = %+v calls=%d", selection, commandCalls)
	}

	var runnerCalls int
	app.selectRunnerBackend = func(_ context.Context, host string, mode auth.GitHubBackendMode) (auth.GitHubBackendSelection, error) {
		runnerCalls++
		return auth.GitHubBackendSelection{
			Mode:            mode,
			Name:            auth.GitHubBackendNameGH,
			Kind:            auth.GitHubBackendKindCLI,
			Host:            host,
			SelectionSource: "test:runner",
		}, nil
	}
	selection, err = app.selectBackendForRunner(context.Background(), commentrunner.Config{
		Hostname:      "github.com",
		GitHubBackend: auth.GitHubBackendModeAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runnerCalls != 1 || selection.ProfileSource != "project" || selection.SelectionSource != "profile:project" || selection.Token.Profile != auth.DefaultProfileName {
		t.Fatalf("runner selection = %+v calls=%d", selection, runnerCalls)
	}
}

func chdirGitHubProfileProject(t *testing.T) {
	t.Helper()
	t.Setenv(auth.ProfileEnv, "")
	t.Setenv(auth.GitHubBackendAPIURLEnv, "")
	t.Setenv(auth.GitHubBackendEnv, "")
	t.Setenv("ISSUE_SPEC_TOKEN", "secret-token")
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	if err := auth.SaveProfile(auth.Profile{
		Name: "team", Kind: auth.ProfileKindHosted, Hostname: "issues.example.test",
		APIURL: "https://issues.example.test/api/github", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "team-instance",
	}, true); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, ".issue-spec")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := []byte("{\n  \"version\": 1,\n  \"repo\": \"o/r\",\n  \"profile\": \"github\",\n  \"hostname\": \"github.com\"\n}\n")
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), config, 0o644); err != nil {
		t.Fatal(err)
	}
	profile, source, err := auth.ResolveProfileAt("", "github.com", root)
	if err != nil {
		t.Fatal(err)
	}
	if source != "project" || profile.Name != auth.DefaultProfileName {
		t.Fatalf("project profile = %q from %q, want %q from project", profile.Name, source, auth.DefaultProfileName)
	}
	t.Chdir(root)
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

func TestCompatibilityArchiveDefaultPathUsesIssueSpecSpecs(t *testing.T) {
	t.Chdir(t.TempDir())
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-001", `## Requirement: Default archive path

The archive command MUST write durable specs under issue-spec/specs by default.

### Scenario: Render durable spec

- **WHEN** archive durable-spec runs without --output
- **THEN** it writes issue-spec/specs/<capability>/spec.md.
`, model.BodyOptions{
		Agent:  "Compatibility Worker",
		Status: "confirmed",
		Scope:  "archive path",
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
			getIssue: func(_ context.Context, repo string, issueNumber int) (github.Issue, error) {
				if repo != "o/r" || issueNumber != 9 {
					t.Fatalf("unexpected issue lookup repo=%q issue=%d", repo, issueNumber)
				}
				return github.Issue{Number: 9, HTMLURL: "https://github.com/o/r/issues/9"}, nil
			},
			listIssueComments: func(_ context.Context, repo string, issueNumber int) ([]github.Comment, error) {
				if repo != "o/r" || issueNumber != 9 {
					t.Fatalf("unexpected comment list repo=%q issue=%d", repo, issueNumber)
				}
				return []github.Comment{{
					ID:      1,
					HTMLURL: "https://github.com/o/r/issues/9#issuecomment-1",
					URL:     "https://api.github.com/repos/o/r/issues/comments/1",
					Body:    specBody,
				}}, nil
			},
		}, nil
	}

	code := app.runArchive(context.Background(), []string{"durable-spec", "--repo", "o/r", "--proposal", "9", "--capability", "compat"})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join("issue-spec", "specs", "compat", "spec.md")); err != nil {
		t.Fatalf("default durable spec path missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join("openspec", "specs", "compat", "spec.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old openspec path should not be written, stat err=%v", err)
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
		OutputPath: "issue-spec/specs/compat/spec.md",
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
		{name: "process-workspace-linux", pkg: "./internal/processworkspace", goos: "linux"},
		{name: "process-workspace-windows", pkg: "./internal/processworkspace", goos: "windows"},
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

	unixLock, err := os.ReadFile(filepath.Join(root, "internal", "processworkspace", "store_lock_unix.go"))
	if err != nil {
		t.Fatal(err)
	}
	windowsLock, err := os.ReadFile(filepath.Join(root, "internal", "processworkspace", "store_lock_windows.go"))
	if err != nil {
		t.Fatal(err)
	}
	unsupportedLock, err := os.ReadFile(filepath.Join(root, "internal", "processworkspace", "store_lock_unsupported.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unixLock), "//go:build") || !strings.Contains(string(windowsLock), "//go:build windows") ||
		!strings.Contains(string(windowsLock), "windows.LockFileEx") || !strings.Contains(string(windowsLock), "windows.UnlockFileEx") ||
		!strings.Contains(string(unsupportedLock), "errRegistryLockUnsupported") {
		t.Fatal("process workspace registry locking must keep real platform locks and fail closed when unsupported")
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
