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
	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

type app struct {
	in          io.Reader
	out         io.Writer
	err         io.Writer
	profileName string

	selectGitHubBackend             func(context.Context, string) (auth.GitHubBackendSelection, error)
	selectRunnerBackend             func(context.Context, string, auth.GitHubBackendMode) (auth.GitHubBackendSelection, error)
	newGitHubBackend                func(context.Context, auth.GitHubBackendSelection) (github.Backend, error)
	gitHubBackendToken              func(context.Context, auth.GitHubBackendSelection) (string, error)
	runnerPreflight                 func(context.Context, commentrunner.Config) commentrunner.PreflightReport
	runnerIntake                    func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error)
	newRunnerNotificationBackend    func(context.Context, commentrunner.Config) (runnerNotificationBackend, error)
	runnerReconcile                 func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error)
	runnerDispatch                  func(context.Context, commentrunner.Config) (jobs.Result, error)
	runnerCancellationDrain         func(context.Context, commentrunner.Config) (jobs.Result, error)
	runnerDiagnostics               *runnerLogger
	newNativeEvidenceProvider       func(auth.Profile, string) (nativeEvidenceProvider, error)
	newNativeSearchProvider         func(auth.Profile, string) (nativeSearchProvider, error)
	newNativeQuestionAnswerProvider func(auth.Profile, string) (github.NativeQuestionAnswerOperations, error)
	newNativeCodeChangeBackend      func(auth.Profile, string) (nativeCodeChangeBackend, error)
	lookupOperatorProvider          func(context.Context, auth.Profile, string) (codereview.Provider, error)
	doctorAgentProbe                func(context.Context, capability.Request) (capability.Report, error)
	newRequirementsAPI              func(auth.Profile, string) (requirementsAPI, error)
	saveRequirementsProfile         func(auth.Profile, bool) error
	storeRequirementsToken          func(context.Context, auth.Profile, string, bool) (string, error)
	resolveRequirementsToken        func(context.Context, auth.Profile) (auth.Token, error)
	readRequirementsSecret          func(io.Reader, io.Writer) (string, error)
	stdinIsTerminal                 func(io.Reader) bool
	resolveFinalizationBaseline     func(context.Context, string, string) (string, error)
	openWorkspace                   func(context.Context, string, string, processworkspace.ManagerOptions) (workspaceService, error)
}

type commandFunc func(context.Context, []string) int

func Execute(args []string, in io.Reader, out io.Writer, errOut io.Writer) int {
	a := newApp(in, out, errOut)
	profileName, args, err := extractGlobalProfile(args)
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	a.profileName = profileName
	ctx := context.Background()
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		a.printUsage()
		return 0
	}
	switch args[0] {
	case "version", "--version":
		return a.runVersion(ctx, args[1:])
	case "auth":
		return a.runAuth(ctx, args[1:])
	case "doctor":
		return a.runDoctor(ctx, args[1:])
	case "init":
		return a.runInit(ctx, args[1:])
	case "issue":
		if len(args) > 1 && args[1] == "close-change" {
			return a.runIssueCloseChange(ctx, args[2:])
		}
		return a.runIssue(ctx, args[1:])
	case "comment":
		return a.runComment(ctx, args[1:])
	case "question":
		return a.runQuestion(ctx, args[1:])
	case "projection":
		return a.runProjection(ctx, args[1:])
	case "review":
		return a.runReview(ctx, args[1:])
	case "pr":
		return a.runPR(ctx, args[1:])
	case "archive":
		return a.runArchive(ctx, args[1:])
	case "durable-spec":
		return a.runDurableSpec(ctx, args[1:])
	case "workflow":
		return a.runWorkflow(ctx, args[1:])
	case "link":
		return a.runLink(ctx, args[1:])
	case "status":
		return a.runStatus(ctx, args[1:])
	case "verify":
		return a.runVerify(ctx, args[1:])
	case "verify-links":
		return a.runVerifyLinks(ctx, args[1:])
	case "read":
		return a.runRead(ctx, args[1:])
	case "search":
		return a.runSearch(ctx, args[1:])
	case "code-change":
		return a.runCodeChange(ctx, args[1:])
	case "runner":
		return a.runRunner(ctx, args[1:])
	case "requirements":
		return a.runRequirements(ctx, args[1:])
	case "finalize":
		return a.runFinalize(ctx, args[1:])
	default:
		a.errorf("unknown command %q\n", args[0])
		a.printUsage()
		return 2
	}
}

func extractGlobalProfile(args []string) (string, []string, error) {
	clean := make([]string, 0, len(args))
	var profile string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		var value string
		switch {
		case argument == "--profile":
			if index+1 >= len(args) {
				return "", nil, errors.New("--profile requires a value")
			}
			index++
			value = args[index]
		case strings.HasPrefix(argument, "--profile="):
			value = strings.TrimPrefix(argument, "--profile=")
		default:
			clean = append(clean, argument)
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", nil, errors.New("--profile requires a non-empty value")
		}
		if profile != "" && profile != value {
			return "", nil, errors.New("--profile may be specified only once")
		}
		profile = value
	}
	return profile, clean, nil
}

func newApp(in io.Reader, out io.Writer, errOut io.Writer) *app {
	return &app{
		in:                              in,
		out:                             out,
		err:                             errOut,
		newRequirementsAPI:              defaultNewRequirementsAPI,
		saveRequirementsProfile:         auth.SaveProfile,
		storeRequirementsToken:          auth.StoreProfileToken,
		resolveRequirementsToken:        auth.ResolveProfileToken,
		readRequirementsSecret:          readHiddenRequirementsSecret,
		stdinIsTerminal:                 requirementsInputIsTerminal,
		selectGitHubBackend:             defaultSelectGitHubBackend,
		selectRunnerBackend:             defaultSelectRunnerBackend,
		newGitHubBackend:                defaultNewGitHubBackend,
		gitHubBackendToken:              defaultGitHubBackendToken,
		newNativeEvidenceProvider:       defaultNewNativeEvidenceProvider,
		newNativeSearchProvider:         defaultNewNativeSearchProvider,
		newNativeQuestionAnswerProvider: defaultNewNativeQuestionAnswerProvider,
		newNativeCodeChangeBackend:      defaultNewNativeCodeChangeBackend,
		lookupOperatorProvider:          defaultResolveOperatorProvider,
		resolveFinalizationBaseline:     defaultResolveFinalizationBaseline,
		openWorkspace:                   defaultOpenWorkspace,
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
  issue-spec [--profile name] <command> [options]
	issue-spec version [--json]
	issue-spec --version
  issue-spec auth status|login|logout|token
  issue-spec doctor agent --repo owner/repo --operation issue.read [--operation pr.read]
  issue-spec init --repo owner/repo [--skip-labels] [--tools codex,claude|all|none] [--delivery both|skills|commands] [--install-global-prompts]
	issue-spec issue create simple --repo owner/repo --title title --body-file file.md
	issue-spec issue create proposal|design|implement --repo owner/repo --change name [--body-file file.md] [--title title]
  issue-spec issue update --repo owner/repo --issue N [--title title] [--body-file file.md] [--summary "what changed"]
  issue-spec issue list --repo owner/repo [--state open|closed|all] --json
  issue-spec issue close|reopen --repo owner/repo --issue N [--json]
  issue-spec issue close-change --repo owner/repo --proposal N --design N --implement N --revision REV [--json]
  issue-spec comment create --repo owner/repo --issue N --body-file reply.md [--json]
  issue-spec comment edit --repo owner/repo --comment-id N --body-file reply.md [--json]
  issue-spec comment delete --repo owner/repo --comment-id N [--json]
  issue-spec comment generate --type SPEC --id SPEC-1001 --input-file spec.json [--status confirmed] [--scope "..."]
  issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-1001 --body-file file.md [--allow-noncanonical]
  issue-spec comment transition --repo owner/repo --issue 2 --id TASK-2001 --to done [--expected-version N|--expected-digest SHA256]
  issue-spec comment list --repo owner/repo --issue N [--type SPEC] [--json]
  issue-spec question create --repo owner/repo --issue 1 --id QUESTION-1001 --question "..."
  issue-spec question answer --repo owner/repo --issue 1 --id ANSWER-1001 --question-id QUESTION-1001 (--select option-id|--custom "answer")
  issue-spec question resolve --repo owner/repo --issue 1 --id QUESTION-1001 --resolution-file file.md
    new typed IDs use <TYPE>-<issue><three-digit sequence>; allocate the sequence within that Issue and type
  issue-spec projection upsert --repo owner/repo --issue N --phase proposal-choice-brief --source-digest SHA256 --body-file file.md [--allow-nonatomic --expected-absence|--allow-nonatomic --expected-digest SHA256]
    non-atomic first create uses --expected-absence; replacement uses the observed-body --expected-digest SHA256
  issue-spec pr rationale --repo owner/repo --pr 4 --path file.go --line 42 --process PROCESS-3001 --spec SPEC-1001 --spec-url URL --body "why"
  issue-spec pr link-process --repo owner/repo --issue 3 --process PROCESS-3001 --pr 4
  issue-spec pr link-issues --repo owner/repo --pr N --proposal N --design N --implement N
  issue-spec pr verify-closure --repo owner/repo --pr N --proposal N --design N --implement N [--json]
  issue-spec review finding --repo owner/repo --pr 4 --path file.go --line 42 --id FINDING-001 --severity P1 --process PROCESS-3001 --spec SPEC-1001 --spec-url URL --body "what to fix"
  issue-spec review reply --repo owner/repo --pr 4 --comment-id COMMENT_ID --finding FINDING-001 --process PROCESS-3001 --status resolved --body "fixed"
  issue-spec review sync --repo owner/repo --pr 4 --implement 3 --id REVIEW-3001
  issue-spec durable-spec preview|apply|check|detail --repo owner/repo --proposal N [options]
  issue-spec workflow validate --repo owner/repo [--json]
  issue-spec workflow which --repo owner/repo [--schema name] [--json]
  issue-spec workflow workspace prepare|inspect|complete|integrate|reconcile|cleanup --repo owner/repo --issue N --process PROCESS-001
  issue-spec link --repo owner/repo --targets-file relationships.json [--allow-nonatomic --expected-digest SHA256]
  issue-spec link --repo owner/repo --from SPEC-001 --from-issue N --to TASK-001 --to-issue M [--allow-nonatomic --expected-digest SHA256]
  issue-spec status --repo owner/repo --proposal N [--design N] [--implement N]
  issue-spec verify --repo owner/repo --proposal N --design N --implement N
  issue-spec verify-links --repo owner/repo --proposal N --design N --implement N
  issue-spec read issue --repo owner/repo --issue N [--comments] [--typed-only]
  issue-spec read pr --repo owner/repo --pr N [--comments] [--typed-only]
  issue-spec search issues --repo owner/repo --query TEXT [--state all|open|closed] [--source all|issue|comments|change] [--stage proposal|design|implement] [--limit 10]
  issue-spec code-change attach --repo owner/repo --implement N --change-id ID --revision REV [--refresh --expected-version N] [--json]
  issue-spec code-change link-process --repo owner/repo --implement N --process PROCESS-001 --expected-version N [--json]
	issue-spec code-change rationale --repo owner/repo --implement N --process PROCESS-001 --spec SPEC-001 --spec-url URL --body "why" --agent Worker [--json]
	issue-spec finalize preview --repo owner/repo --proposal N --design N --implement N --pr N --intent-file file.json --plan-out /absolute/plan.json [--json]
	issue-spec finalize apply --plan /absolute/plan.json --checkpoint /absolute/checkpoint.json [--allow-nonatomic] [--json]
	issue-spec finalize detail --plan /absolute/plan.json [--json]
  issue-spec requirements setup --server URL [--token-stdin] [--yes] [--json]
  issue-spec requirements status [--repo owner/repo] [--json]
  issue-spec runner poll --repo owner/repo --runner login --once --dry-run
  issue-spec runner serve --profile self-hosted --repo owner/repo --runner login --subscription-id UUID --secret-file FILE (--git-credential-command /absolute/provider|--allow-host-ssh)
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

func (a *app) clientForProfile(ctx context.Context, host, profile string) (github.Backend, auth.Token, error) {
	previous := a.profileName
	a.profileName = strings.TrimSpace(profile)
	defer func() { a.profileName = previous }()
	return a.clientFor(ctx, host)
}

func (a *app) selectBackendForProfile(ctx context.Context, host, profile string) (auth.GitHubBackendSelection, error) {
	previous := a.profileName
	a.profileName = strings.TrimSpace(profile)
	defer func() { a.profileName = previous }()
	return a.selectBackend(ctx, host)
}

func (a *app) selectBackend(ctx context.Context, host string) (auth.GitHubBackendSelection, error) {
	profileName := strings.TrimSpace(a.profileName)
	profile, source, profileErr := auth.ResolveProfile(profileName, host)
	if profileErr != nil {
		return auth.GitHubBackendSelection{}, profileErr
	}
	if (source == "builtin" || source == "project") && auth.IsBuiltinGitHubProfile(profile) {
		var selection auth.GitHubBackendSelection
		var err error
		if a.selectGitHubBackend != nil {
			selection, err = a.selectGitHubBackend(ctx, profile.Hostname)
		} else {
			selection, err = defaultSelectGitHubBackend(ctx, profile.Hostname)
		}
		if source == "project" {
			selection.SelectionSource = "profile:project"
		}
		return selection.WithProfile(profile, source), err
	}
	if source != "builtin" || profileName != "" || auth.ProfileNameFromEnv() != "" || strings.TrimSpace(os.Getenv(auth.GitHubBackendAPIURLEnv)) != "" {
		return auth.SelectProfileBackendWithOptions(ctx, profileName, host, auth.GitHubBackendSelectionOptions{GHAuthenticated: ghAuthenticated})
	}
	if a.selectGitHubBackend != nil {
		return a.selectGitHubBackend(ctx, host)
	}
	return defaultSelectGitHubBackend(ctx, host)
}

func (a *app) selectBackendForRunner(ctx context.Context, cfg commentrunner.Config) (auth.GitHubBackendSelection, error) {
	cfg = cfg.Normalized()
	profile, source, profileErr := auth.ResolveProfile(cfg.Profile, cfg.Hostname)
	if profileErr != nil {
		return auth.GitHubBackendSelection{}, profileErr
	}
	if (source == "builtin" || source == "project") && auth.IsBuiltinGitHubProfile(profile) {
		var selection auth.GitHubBackendSelection
		var err error
		if a.selectRunnerBackend != nil {
			selection, err = a.selectRunnerBackend(ctx, profile.Hostname, cfg.GitHubBackend)
		} else {
			selection, err = defaultSelectRunnerBackend(ctx, profile.Hostname, cfg.GitHubBackend)
		}
		if source == "project" {
			selection.SelectionSource = "profile:project"
		}
		return selection.WithProfile(profile, source), err
	}
	if source != "builtin" || cfg.Profile != "" || strings.TrimSpace(os.Getenv(auth.GitHubBackendAPIURLEnv)) != "" {
		mode := cfg.GitHubBackend
		return auth.SelectProfileBackendWithOptions(ctx, cfg.Profile, cfg.Hostname, auth.GitHubBackendSelectionOptions{GHAuthenticated: ghAuthenticated, Mode: &mode})
	}
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
		if selection.Profile.Name != "" {
			return github.NewClientWithOptions(github.ClientOptions{
				Host: selection.Host, BaseURL: selection.Profile.APIURL, Token: selection.Token.Value, CAFile: selection.Profile.CAFile,
			})
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
			if !model.IsLikelyTyped(comment.Body) && !model.IsLikelyCodeChangeRationale(comment.Body) {
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
	return findArtifactByIDIn(comments, issueNumber, id)
}

// findArtifactByIDIn resolves an ID against an already-fetched comment snapshot,
// so callers resolving several IDs on one issue list its comments once.
func findArtifactByIDIn(comments []github.Comment, issueNumber int, id string) (model.Artifact, string, error) {
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

// upsertTypedComment creates or updates a typed comment. On update it preserves
// every Related Comments link already on the existing comment by merging them into
// the regenerated body (idempotent via AddRelatedCommentLink), so a content-only
// regenerate never silently drops traceability links. The returned slice lists any
// links that were on the existing comment but are still absent from the written
// body; callers surface it as a link-drop warning.
func upsertTypedComment(ctx context.Context, client github.Operations, repo string, issueNumber int, commentType, id, body string) (string, github.Comment, []string, error) {
	comments, err := client.ListIssueComments(ctx, repo, issueNumber)
	if err != nil {
		return "", github.Comment{}, nil, err
	}
	for _, comment := range comments {
		tc := model.ParseTypedComment(comment.Body)
		if tc.Type == strings.ToUpper(commentType) && tc.ID == id {
			existing := model.RelatedCommentURLs(tc)
			merged := body
			for _, url := range existing {
				next, _, addErr := model.AddRelatedCommentLink(merged, url)
				if addErr != nil {
					continue
				}
				merged = next
			}
			dropped := droppedRelatedLinks(existing, model.RelatedCommentURLs(model.ParseTypedComment(merged)))
			updated, err := client.UpdateComment(ctx, repo, comment.ID, merged)
			return "updated", updated, dropped, err
		}
	}
	created, err := client.CreateComment(ctx, repo, issueNumber, body)
	return "created", created, nil, err
}

// droppedRelatedLinks returns the Related Comments URLs in before that are absent
// from after, compared by normalized URL.
func droppedRelatedLinks(before, after []string) []string {
	have := map[string]bool{}
	for _, url := range after {
		have[model.NormalizeURL(url)] = true
	}
	var dropped []string
	for _, url := range before {
		if !have[model.NormalizeURL(url)] {
			dropped = append(dropped, url)
		}
	}
	return dropped
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
