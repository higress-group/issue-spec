package commands

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

type app struct {
	in  io.Reader
	out io.Writer
	err io.Writer

	selectGitHubBackend          func(context.Context, string) (auth.GitHubBackendSelection, error)
	selectRunnerBackend          func(context.Context, string, auth.GitHubBackendMode) (auth.GitHubBackendSelection, error)
	newGitHubBackend             func(context.Context, auth.GitHubBackendSelection) (github.Backend, error)
	gitHubBackendToken           func(context.Context, auth.GitHubBackendSelection) (string, error)
	runnerPreflight              func(context.Context, commentrunner.Config) commentrunner.PreflightReport
	runnerIntake                 func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error)
	newRunnerNotificationBackend func(context.Context, commentrunner.Config) (runnerNotificationBackend, error)
	runnerReconcile              func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error)
	runnerDispatch               func(context.Context, commentrunner.Config) (jobs.Result, error)
}

type commandFunc func(context.Context, []string) int

func Execute(args []string, in io.Reader, out io.Writer, errOut io.Writer) int {
	a := newApp(in, out, errOut)
	ctx := context.Background()
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		a.printUsage()
		return 0
	}
	switch args[0] {
	case "auth":
		return a.runAuth(ctx, args[1:])
	case "init":
		return a.runInit(ctx, args[1:])
	case "issue":
		return a.runIssue(ctx, args[1:])
	case "comment":
		return a.runComment(ctx, args[1:])
	case "question":
		return a.runQuestion(ctx, args[1:])
	case "review":
		return a.runReview(ctx, args[1:])
	case "pr":
		return a.runPR(ctx, args[1:])
	case "archive":
		return a.runArchive(ctx, args[1:])
	case "link":
		return a.runLink(ctx, args[1:])
	case "status":
		return a.runStatus(ctx, args[1:])
	case "verify":
		return a.runVerify(ctx, args[1:])
	case "verify-links":
		return a.runVerifyLinks(ctx, args[1:])
	case "runner":
		return a.runRunner(ctx, args[1:])
	default:
		a.errorf("unknown command %q\n", args[0])
		a.printUsage()
		return 2
	}
}

func newApp(in io.Reader, out io.Writer, errOut io.Writer) *app {
	return &app{
		in:                  in,
		out:                 out,
		err:                 errOut,
		selectGitHubBackend: defaultSelectGitHubBackend,
		selectRunnerBackend: defaultSelectRunnerBackend,
		newGitHubBackend:    defaultNewGitHubBackend,
		gitHubBackendToken:  defaultGitHubBackendToken,
	}
}

var ghAuthenticated = github.GHAuthenticated
var ghAuthToken = github.GHAuthToken
var ghLookPath = exec.LookPath

func defaultSelectGitHubBackend(ctx context.Context, host string) (auth.GitHubBackendSelection, error) {
	return auth.SelectGitHubBackendWithOptions(ctx, host, auth.GitHubBackendSelectionOptions{
		GHAuthenticated: ghAuthenticated,
	})
}

func defaultSelectRunnerBackend(ctx context.Context, host string, mode auth.GitHubBackendMode) (auth.GitHubBackendSelection, error) {
	return auth.SelectGitHubBackendWithOptions(ctx, host, auth.GitHubBackendSelectionOptions{
		GHAuthenticated: ghAuthenticated,
		Mode:            &mode,
	})
}

func (a *app) printUsage() {
	fmt.Fprintln(a.out, `issue-spec manages issue-native OpenSpec artifacts.

Usage:
  issue-spec auth status|login|logout|token
  issue-spec init --repo owner/repo [--create-labels] [--tools codex,claude|all|none] [--delivery both|skills|commands]
  issue-spec issue create proposal|design|implement --repo owner/repo --change name [--body-file file.md] [--title title]
  issue-spec issue update --repo owner/repo --issue N [--title title] [--body-file file.md] [--summary "what changed"]
  issue-spec comment generate --type SPEC --id SPEC-001 --input-file spec.json [--status confirmed] [--scope "..."]
  issue-spec comment upsert --repo owner/repo --issue N --type SPEC --id SPEC-001 --body-file file.md [--allow-noncanonical]
  issue-spec comment list --repo owner/repo --issue N [--type SPEC] [--json]
  issue-spec question create --repo owner/repo --issue N --id QUESTION-001 --question "..."
  issue-spec question resolve --repo owner/repo --issue N --id QUESTION-001 --resolution-file file.md
  issue-spec pr rationale --repo owner/repo --pr N --path file.go --line 42 --process PROCESS-001 --spec SPEC-001 --spec-url URL --body "why"
  issue-spec pr link-process --repo owner/repo --issue N --process PROCESS-001 --pr N
  issue-spec pr link-issues --repo owner/repo --pr N --proposal N --design N --implement N
  issue-spec review finding --repo owner/repo --pr N --path file.go --line 42 --id FINDING-001 --severity P1 --process PROCESS-001 --spec SPEC-001 --spec-url URL --body "what to fix"
  issue-spec review reply --repo owner/repo --pr N --comment-id COMMENT_ID --finding FINDING-001 --process PROCESS-001 --status resolved --body "fixed"
  issue-spec review sync --repo owner/repo --pr N --implement N --id REVIEW-001
  issue-spec archive durable-spec --repo owner/repo --proposal N --design N --implement N --pr N --capability my-capability --close-issues
  issue-spec link --repo owner/repo --from SPEC-001 --from-issue N --to TASK-001 --to-issue M
  issue-spec status --repo owner/repo --proposal N [--design N] [--implement N]
  issue-spec verify --repo owner/repo --proposal N --design N --implement N [--durable-spec path]
  issue-spec verify-links --repo owner/repo --proposal N --design N --implement N
  issue-spec runner poll --repo owner/repo --runner login --once --dry-run
  issue-spec runner preflight --repo owner/repo --runner login`)
}

func newFlagSet(name string, errOut io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		printFlagSetUsage(fs.Output(), name, fs)
	}
	return fs
}

func (a *app) parseFlagSet(fs *flag.FlagSet, args []string) (bool, int) {
	if argsContainHelp(args) {
		fs.SetOutput(a.out)
		fs.Usage()
		return false, 0
	}
	if err := fs.Parse(args); err != nil {
		return false, 2
	}
	return true, 0
}

func printFlagSetUsage(out io.Writer, name string, fs *flag.FlagSet) {
	fmt.Fprintf(out, "Usage:\n  issue-spec %s [options]\n\nOptions:\n", name)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fs.VisitAll(func(f *flag.Flag) {
		valueName, usage := flag.UnquoteUsage(f)
		name := "--" + f.Name
		if valueName != "" {
			name += " " + valueName
		}
		fmt.Fprintf(tw, "  %s\t%s (default: %s)\n", name, usage, flagDefaultValue(f))
	})
	_ = tw.Flush()
}

func flagDefaultValue(f *flag.Flag) string {
	if f == nil || f.DefValue == "" {
		return `""`
	}
	return f.DefValue
}

func argsContainHelp(args []string) bool {
	for _, arg := range args {
		if isHelpArg(arg) {
			return true
		}
	}
	return false
}

func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "--help"
}

func (a *app) clientFor(ctx context.Context, host string) (github.Backend, auth.Token, error) {
	host = auth.NormalizeHost(host)
	selection, err := a.selectBackend(ctx, host)
	if err != nil {
		return nil, selection.TokenWithDiagnostics(), err
	}
	backend, err := a.backendForSelection(ctx, selection)
	if err != nil {
		return nil, selection.TokenWithDiagnostics(), err
	}
	token := selection.TokenWithDiagnostics()
	if info := backend.BackendInfo(); info.Name != "" {
		token.Backend.Name = info.Name
		token.Backend.Kind = info.Kind
		token.Backend.Host = info.Host
	}
	return backend, token, nil
}

func (a *app) selectBackend(ctx context.Context, host string) (auth.GitHubBackendSelection, error) {
	if a.selectGitHubBackend != nil {
		return a.selectGitHubBackend(ctx, host)
	}
	return defaultSelectGitHubBackend(ctx, host)
}

func (a *app) selectBackendForRunner(ctx context.Context, cfg commentrunner.Config) (auth.GitHubBackendSelection, error) {
	cfg = cfg.Normalized()
	if a.selectRunnerBackend != nil {
		return a.selectRunnerBackend(ctx, cfg.Hostname, cfg.GitHubBackend)
	}
	return defaultSelectRunnerBackend(ctx, cfg.Hostname, cfg.GitHubBackend)
}

func (a *app) backendForSelection(ctx context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
	if a.newGitHubBackend != nil {
		return a.newGitHubBackend(ctx, selection)
	}
	return defaultNewGitHubBackend(ctx, selection)
}

func (a *app) tokenForSelection(ctx context.Context, selection auth.GitHubBackendSelection) (string, error) {
	if a.gitHubBackendToken != nil {
		return a.gitHubBackendToken(ctx, selection)
	}
	return defaultGitHubBackendToken(ctx, selection)
}

func defaultNewGitHubBackend(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
	switch selection.Name {
	case auth.GitHubBackendNameREST:
		if strings.TrimSpace(selection.Token.Value) == "" {
			return nil, fmt.Errorf("rest GitHub backend selected without a token")
		}
		return github.NewClient(selection.Host, selection.Token.Value), nil
	case auth.GitHubBackendNameGH:
		return github.NewGHBackend(github.GHBackendOptions{
			Host: selection.Host,
			CLIOptions: github.GHCLIOptions{
				Redactor: defaultGHBackendRedactor(selection),
			},
		})
	default:
		return nil, fmt.Errorf("unsupported GitHub backend %q", selection.Name)
	}
}

func defaultGHBackendRedactor(selection auth.GitHubBackendSelection) github.ExternalCLIRedactor {
	values := []string{selection.Token.Value}
	for _, envName := range []string{"ISSUE_SPEC_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		values = append(values, os.Getenv(envName))
	}
	return github.NewExternalCLIRedactor(values...)
}

func defaultGitHubBackendToken(ctx context.Context, selection auth.GitHubBackendSelection) (string, error) {
	switch selection.Name {
	case auth.GitHubBackendNameREST:
		if strings.TrimSpace(selection.Token.Value) == "" {
			return "", fmt.Errorf("rest GitHub backend selected without a token")
		}
		return selection.Token.Value, nil
	case auth.GitHubBackendNameGH:
		return ghAuthToken(ctx, selection.Host)
	default:
		return "", fmt.Errorf("unsupported GitHub backend %q", selection.Name)
	}
}

func (a *app) validateRepo(repo string) (string, bool) {
	parsed, err := github.ParseRepo(repo)
	if err != nil {
		a.errorf("%v\n", err)
		return "", false
	}
	return parsed, true
}

func (a *app) readBodyFile(path string) (string, bool) {
	return a.readFlagFile(path, "body-file")
}

func (a *app) readFlagFile(path, name string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		a.errorf("--%s is required\n", name)
		return "", false
	}
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(a.in)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		a.errorf("read %s %s: %v\n", name, path, err)
		return "", false
	}
	return string(data), true
}

func (a *app) outputJSON(value any) int {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		a.errorf("write JSON: %v\n", err)
		return 1
	}
	return 0
}

func (a *app) errorf(format string, args ...any) {
	fmt.Fprintf(a.err, format, args...)
}

func issueNumberFlag(value string) (int, error) {
	return github.ParseIssueNumber(value)
}

func parseIssueFlag(value string, name string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("--%s is required", name)
	}
	return issueNumberFlag(value)
}

func collectArtifacts(ctx context.Context, client github.Operations, repo string, issueNumbers ...int) ([]model.Artifact, error) {
	var artifacts []model.Artifact
	for _, issueNumber := range issueNumbers {
		if issueNumber == 0 {
			continue
		}
		comments, err := client.ListIssueComments(ctx, repo, issueNumber)
		if err != nil {
			return nil, err
		}
		for _, comment := range comments {
			if !model.IsLikelyTyped(comment.Body) {
				continue
			}
			tc := model.ParseTypedComment(comment.Body)
			artifacts = append(artifacts, model.Artifact{
				Issue:     issueNumber,
				CommentID: comment.ID,
				URL:       comment.HTMLURL,
				APIURL:    comment.URL,
				Comment:   tc,
			})
		}
	}
	return artifacts, nil
}

func findArtifactByID(ctx context.Context, client github.Operations, repo string, issueNumber int, id string) (model.Artifact, string, error) {
	comments, err := client.ListIssueComments(ctx, repo, issueNumber)
	if err != nil {
		return model.Artifact{}, "", err
	}
	for _, comment := range comments {
		tc := model.ParseTypedComment(comment.Body)
		if tc.ID == id {
			return model.Artifact{
				Issue:     issueNumber,
				CommentID: comment.ID,
				URL:       comment.HTMLURL,
				APIURL:    comment.URL,
				Comment:   tc,
			}, comment.Body, nil
		}
	}
	return model.Artifact{}, "", fmt.Errorf("typed comment %s not found on issue %d", id, issueNumber)
}

func upsertTypedComment(ctx context.Context, client github.Operations, repo string, issueNumber int, commentType, id, body string) (string, github.Comment, error) {
	comments, err := client.ListIssueComments(ctx, repo, issueNumber)
	if err != nil {
		return "", github.Comment{}, err
	}
	for _, comment := range comments {
		tc := model.ParseTypedComment(comment.Body)
		if tc.Type == strings.ToUpper(commentType) && tc.ID == id {
			updated, err := client.UpdateComment(ctx, repo, comment.ID, body)
			return "updated", updated, err
		}
	}
	created, err := client.CreateComment(ctx, repo, issueNumber, body)
	return "created", created, err
}

func hasBlockedQuestion(artifacts []model.Artifact) bool {
	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.Type == "QUESTION" && tc.Status == "blocked" {
			return true
		}
	}
	return false
}

func countType(artifacts []model.Artifact, commentType string) int {
	count := 0
	for _, artifact := range artifacts {
		if artifact.Comment.Type == commentType {
			count++
		}
	}
	return count
}

func parseIntFlag(value string, name string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("--%s is required", name)
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("--%s must be a positive integer", name)
	}
	return n, nil
}

func isNoToken(err error) bool {
	return errors.Is(err, auth.ErrNoToken)
}
