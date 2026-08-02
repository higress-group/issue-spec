package commands

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/mergeauthority"
	"github.com/higress-group/issue-spec/internal/mergecheck"
	"github.com/higress-group/issue-spec/internal/workflow"
)

type mergeCommandSelection struct {
	Repo               string
	Hostname           string
	Scope              mergecheck.ChangeScope
	ExternalRepository string
	ChangeID           string
	ExpectedHead       string
	PR                 int
}

func (a *app) runMergeCheck(ctx context.Context, args []string) int {
	fs := newFlagSet("merge-check", a.err)
	repoFlag := fs.String("repo", "", "issue repository owner/name")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	issueFlag := fs.String("issue", "", "simple Issue number or URL")
	proposalFlag := fs.String("proposal", "", "Proposal Issue number or URL")
	designFlag := fs.String("design", "", "optional Design Issue number or URL")
	implementFlag := fs.String("implement", "", "optional Implement Issue number or URL")
	externalRepository := fs.String("external-repository", "", "provider repository identity (defaults to --repo)")
	changeID := fs.String("change-id", "", "provider code-change identity")
	head := fs.String("head", "", "expected current code-change head")
	pr := fs.Int("pr", 0, "GitHub pull request number used to resolve change identity and current head")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	scope, err := parseMergeChangeScope(*issueFlag, *proposalFlag, *designFlag, *implementFlag)
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	engine, request, err := a.prepareMergeAuthority(ctx, mergeCommandSelection{Repo: repo, Hostname: *host, Scope: scope,
		ExternalRepository: *externalRepository, ChangeID: *changeID, ExpectedHead: *head, PR: *pr})
	if err != nil {
		return a.outputMergeCommandError(*jsonOut, "authority_preflight", err, nil)
	}
	result, err := engine.Check(ctx, request)
	if err != nil {
		return a.outputMergeCommandError(*jsonOut, "authority_collection", err, nil)
	}
	if *jsonOut {
		_ = a.outputJSON(result)
	} else {
		printMergeDecision(a, result.Decision)
	}
	if !result.Decision.Ready {
		return 1
	}
	return 0
}

func parseMergeChangeScope(issueRaw, proposalRaw, designRaw, implementRaw string) (mergecheck.ChangeScope, error) {
	var result mergecheck.ChangeScope
	if strings.TrimSpace(issueRaw) != "" {
		value, err := issueNumberFlag(issueRaw)
		if err != nil {
			return result, fmt.Errorf("--issue: %w", err)
		}
		result.SimpleIssue = &value
	}
	if strings.TrimSpace(proposalRaw) != "" {
		value, err := issueNumberFlag(proposalRaw)
		if err != nil {
			return result, fmt.Errorf("--proposal: %w", err)
		}
		result.ProposalIssue = &value
	}
	if strings.TrimSpace(designRaw) != "" {
		value, err := issueNumberFlag(designRaw)
		if err != nil {
			return result, fmt.Errorf("--design: %w", err)
		}
		result.DesignIssue = &value
	}
	if strings.TrimSpace(implementRaw) != "" {
		value, err := issueNumberFlag(implementRaw)
		if err != nil {
			return result, fmt.Errorf("--implement: %w", err)
		}
		result.ImplementIssue = &value
	}
	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

func (a *app) prepareMergeAuthority(ctx context.Context, selection mergeCommandSelection) (*mergeauthority.Engine, mergeauthority.Request, error) {
	plan, err := workflow.Resolve(".")
	if err != nil {
		return nil, mergeauthority.Request{}, fmt.Errorf("resolve workflow: %w", err)
	}
	providerKey, checks, err := plan.MergeAuthorityConfiguration()
	if err != nil {
		return nil, mergeauthority.Request{}, err
	}
	backend, token, err := a.clientFor(ctx, selection.Hostname)
	if err != nil {
		return nil, mergeauthority.Request{}, fmt.Errorf("resolve issue backend: %w", err)
	}
	profile, _, err := auth.ResolveProfile(a.profileName, selection.Hostname)
	if err != nil {
		return nil, mergeauthority.Request{}, fmt.Errorf("resolve profile: %w", err)
	}
	reference, head, err := a.resolveMergeCodeSubject(ctx, selection, providerKey, profile, token.Value, backend)
	if err != nil {
		return nil, mergeauthority.Request{}, err
	}
	provider, err := a.resolveOperatorProvider(ctx, profile, providerKey)
	if err != nil {
		return nil, mergeauthority.Request{}, fmt.Errorf("resolve operator merge provider: %w", err)
	}
	authority, ok := provider.(codereview.MergeAuthorityProvider)
	if !ok {
		return nil, mergeauthority.Request{}, fmt.Errorf("%w: provider %s does not implement merge authority", codereview.ErrCapabilityMissing, providerKey)
	}
	scopeAuthority := &commandScopeAuthority{backend: backend, repo: selection.Repo}
	engine, err := mergeauthority.New(authority, scopeAuthority)
	if err != nil {
		return nil, mergeauthority.Request{}, err
	}
	request := mergeauthority.Request{Scope: selection.Scope, Reference: reference, ExpectedHead: head, RequiredChecks: checks}
	if err := request.Validate(); err != nil {
		return nil, mergeauthority.Request{}, err
	}
	return engine, request, nil
}

func (a *app) resolveMergeCodeSubject(ctx context.Context, selection mergeCommandSelection, providerKey string,
	profile auth.Profile, token string, backend github.Backend) (codereview.Reference, string, error) {
	externalRepository := strings.TrimSpace(selection.ExternalRepository)
	if externalRepository == "" {
		externalRepository = selection.Repo
	}
	changeID, expectedHead := strings.TrimSpace(selection.ChangeID), strings.TrimSpace(selection.ExpectedHead)
	if selection.PR < 0 {
		return codereview.Reference{}, "", errors.New("--pr must not be negative")
	}
	if selection.PR > 0 {
		if changeID != "" {
			return codereview.Reference{}, "", errors.New("--pr and --change-id are mutually exclusive")
		}
		pr, err := backend.GetPullRequest(ctx, externalRepository, selection.PR)
		if err != nil {
			return codereview.Reference{}, "", fmt.Errorf("read pull request head: %w", err)
		}
		if pr.Number != selection.PR || strings.TrimSpace(pr.Head.SHA) == "" {
			return codereview.Reference{}, "", errors.New("pull request response is incomplete or mismatched")
		}
		changeID = strconv.Itoa(selection.PR)
		if expectedHead != "" && expectedHead != strings.TrimSpace(pr.Head.SHA) {
			return codereview.Reference{}, "", errors.New("caller head differs from the freshly observed pull request head")
		}
		expectedHead = strings.TrimSpace(pr.Head.SHA)
	}
	if changeID == "" {
		if selection.Scope.ImplementIssue == nil || profile.Kind != auth.ProfileKindHosted || a.newNativeCodeChangeBackend == nil {
			return codereview.Reference{}, "", errors.New("--change-id or --pr is required when no self-hosted active Implement reference is available")
		}
		native, err := a.newNativeCodeChangeBackend(profile, token)
		if err != nil {
			return codereview.Reference{}, "", fmt.Errorf("configure native code-change backend: %w", err)
		}
		scope, issueID, err := native.ResolveNativeIssue(ctx, selection.Repo, *selection.Scope.ImplementIssue)
		if err != nil {
			return codereview.Reference{}, "", fmt.Errorf("resolve Implement Issue: %w", err)
		}
		references, err := native.ListNativeReferences(ctx, scope, issueID)
		if err != nil {
			return codereview.Reference{}, "", fmt.Errorf("read active code-change reference: %w", err)
		}
		active, observedHead, err := uniqueActiveCodeChangeIdentity(references)
		if err != nil {
			return codereview.Reference{}, "", err
		}
		if active.ProviderKey != providerKey {
			return codereview.Reference{}, "", errors.New("active code-change provider differs from workflow configuration")
		}
		if expectedHead != "" && expectedHead != observedHead {
			return codereview.Reference{}, "", errors.New("caller head differs from the active code-change head")
		}
		return codereview.Reference{ProviderKey: active.ProviderKey, ExternalRepository: active.ExternalRepositoryID,
			ChangeID: active.ExternalID}, observedHead, nil
	}
	if expectedHead == "" {
		return codereview.Reference{}, "", errors.New("--head or --expected-head is required for an explicit code-change identity")
	}
	return codereview.Reference{ProviderKey: providerKey, ExternalRepository: externalRepository, ChangeID: changeID}, expectedHead, nil
}

type commandScopeAuthority struct {
	backend github.IssueBackend
	repo    string
}

var mergeIssueMarker = regexp.MustCompile(`^<!--\s*issue-spec:issue=([a-z]+)\s+change=([^\s>]+)(?:\s+[^>]*)?-->$`)

func (a *commandScopeAuthority) Validate(ctx context.Context, scope mergecheck.ChangeScope) error {
	if a == nil || a.backend == nil || strings.TrimSpace(a.repo) == "" || scope.Validate() != nil {
		return errors.New("selected scope backend is invalid")
	}
	issues := map[int]github.Issue{}
	for _, number := range scope.IssueNumbers() {
		issue, err := a.backend.GetIssue(ctx, a.repo, number)
		if err != nil {
			return err
		}
		if issue.Number != number || issue.PullRequest != nil {
			return fmt.Errorf("issue #%d response is incomplete or is a pull request", number)
		}
		issues[number] = issue
	}
	if scope.SimpleIssue != nil {
		if hasIssueBodyMarker(issues[*scope.SimpleIssue].Body) {
			return errors.New("simple root must not be a Proposal/Design/Implement issue")
		}
		return nil
	}
	change, err := exactMergeIssueMarker(issues[*scope.ProposalIssue].Body, "proposal")
	if err != nil {
		return fmt.Errorf("proposal #%d: %w", *scope.ProposalIssue, err)
	}
	if scope.DesignIssue != nil {
		if designChange, err := exactMergeIssueMarker(issues[*scope.DesignIssue].Body, "design"); err != nil || designChange != change {
			return fmt.Errorf("design #%d does not belong to the selected proposal", *scope.DesignIssue)
		}
		if err := requireIssuePredecessor(issues[*scope.DesignIssue].Body, "Proposal Issue", *scope.ProposalIssue); err != nil {
			return err
		}
	}
	if scope.ImplementIssue != nil {
		if implementChange, err := exactMergeIssueMarker(issues[*scope.ImplementIssue].Body, "implement"); err != nil || implementChange != change {
			return fmt.Errorf("implement #%d does not belong to the selected proposal", *scope.ImplementIssue)
		}
		if scope.DesignIssue != nil {
			if err := requireIssuePredecessor(issues[*scope.ImplementIssue].Body, "Design Issue", *scope.DesignIssue); err != nil {
				return err
			}
		} else if err := requireIssuePredecessor(issues[*scope.ImplementIssue].Body, "Proposal Issue", *scope.ProposalIssue); err != nil {
			return err
		}
	}
	return nil
}

func (a *commandScopeAuthority) Reconcile(ctx context.Context, scope mergecheck.ChangeScope,
	observed codereview.MergeSnapshot) (mergeauthority.Reconciliation, error) {
	result := mergeauthority.Reconciliation{Issues: []mergeauthority.ReconciledIssue{}}
	if observed.ChangeState != codereview.ChangeMerged {
		return result, errors.New("provider merge is not freshly observed")
	}
	for _, number := range scope.IssueNumbers() {
		issue, err := a.backend.GetIssue(ctx, a.repo, number)
		if err != nil {
			return result, fmt.Errorf("observe issue #%d: %w", number, err)
		}
		if issue.Number != number {
			return result, fmt.Errorf("observe issue #%d: mismatched response", number)
		}
		if strings.EqualFold(issue.State, "closed") {
			result.Issues = append(result.Issues, mergeauthority.ReconciledIssue{Issue: number, AlreadyClosed: true})
			continue
		}
		closed := "closed"
		updated, err := a.backend.UpdateIssue(ctx, a.repo, number, github.UpdateIssueOptions{State: &closed})
		if err != nil {
			return result, fmt.Errorf("close issue #%d: %w", number, err)
		}
		if updated.Number != number || !strings.EqualFold(updated.State, "closed") {
			return result, fmt.Errorf("close issue #%d: response is incomplete or mismatched", number)
		}
		result.Issues = append(result.Issues, mergeauthority.ReconciledIssue{Issue: number, Closed: true})
	}
	return result, nil
}

func exactMergeIssueMarker(body, kind string) (string, error) {
	var matches [][]string
	for _, line := range strings.Split(body, "\n") {
		if match := mergeIssueMarker.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			matches = append(matches, match)
		}
	}
	if len(matches) != 1 || matches[0][1] != kind || strings.TrimSpace(matches[0][2]) == "" {
		return "", errors.New("issue marker is missing, duplicate, or has the wrong phase")
	}
	return matches[0][2], nil
}

func requireIssuePredecessor(body, label string, number int) error {
	pattern := regexp.MustCompile(`(?i)^\s*-\s*` + regexp.QuoteMeta(label) + `:\s*(\S+)\s*$`)
	var values []string
	for _, line := range strings.Split(body, "\n") {
		if match := pattern.FindStringSubmatch(line); match != nil {
			values = append(values, match[1])
		}
	}
	if len(values) != 1 {
		return fmt.Errorf("%s predecessor is missing or ambiguous", label)
	}
	got, err := issueNumberFlag(values[0])
	if err != nil || got != number {
		return fmt.Errorf("%s predecessor does not select issue #%d", label, number)
	}
	return nil
}

func printMergeDecision(a *app, decision mergecheck.Decision) {
	if decision.Ready {
		fmt.Fprintf(a.out, "ready to merge expected head %s\n", decision.ExpectedHead)
		return
	}
	fmt.Fprintf(a.out, "not ready to merge expected head %s\n", decision.ExpectedHead)
	for _, blocker := range decision.Blockers {
		fmt.Fprintf(a.out, "- %s: %s\n", blocker.Code, blocker.Diagnostics)
	}
}

func (a *app) outputMergeCommandError(jsonOut bool, code string, err error, result any) int {
	message := code
	if err != nil {
		message = err.Error()
	}
	if jsonOut {
		_ = a.outputJSON(map[string]any{"ok": false, "code": code, "message": message, "result": result})
	} else {
		a.errorf("%s: %s\n", code, message)
	}
	return 1
}
