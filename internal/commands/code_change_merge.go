package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/mergeauthority"
)

func (a *app) runCodeChangeMerge(ctx context.Context, args []string) int {
	fs := newFlagSet("code-change merge", a.err)
	repoFlag := fs.String("repo", "", "issue repository owner/name")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	issueFlag := fs.String("issue", "", "simple Issue number or URL")
	proposalFlag := fs.String("proposal", "", "Proposal Issue number or URL")
	designFlag := fs.String("design", "", "optional Design Issue number or URL")
	implementFlag := fs.String("implement", "", "optional Implement Issue number or URL")
	externalRepository := fs.String("external-repository", "", "provider repository identity (defaults to --repo)")
	changeID := fs.String("change-id", "", "provider code-change identity")
	expectedHead := fs.String("expected-head", "", "caller-observed exact code-change head")
	pr := fs.Int("pr", 0, "GitHub pull request number used to resolve change identity")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	if strings.TrimSpace(*expectedHead) == "" {
		a.errorf("--expected-head is required\n")
		return 2
	}
	scope, err := parseMergeChangeScope(*issueFlag, *proposalFlag, *designFlag, *implementFlag)
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	engine, request, err := a.prepareMergeAuthority(ctx, mergeCommandSelection{Repo: repo, Hostname: *host, Scope: scope,
		ExternalRepository: *externalRepository, ChangeID: *changeID, ExpectedHead: *expectedHead, PR: *pr})
	if err != nil {
		return a.outputMergeCommandError(*jsonOut, "authority_preflight", err, nil)
	}
	result, err := engine.Merge(ctx, request)
	if err != nil {
		code := "conditional_merge_rejected"
		if errors.Is(err, mergeauthority.ErrNotReady) {
			code = "merge_not_ready"
		} else {
			var post *mergeauthority.PostMergeError
			if errors.As(err, &post) {
				code = "post_merge_reconciliation"
			}
		}
		return a.outputMergeCommandError(*jsonOut, code, err, result)
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	if result.Merge == nil {
		if result.AlreadyMerged {
			fmt.Fprintf(a.out, "change at %s was already merged; reconciliation completed\n", result.Decision.ExpectedHead)
			return 0
		}
		return a.outputMergeCommandError(false, "conditional_merge_invalid", errors.New("provider returned no merge result"), result)
	}
	fmt.Fprintf(a.out, "merged %s at %s (%s)\n", result.Merge.CanonicalURL, result.Merge.MergedRevision, result.Merge.MergeID)
	if result.Reconciliation != nil {
		fmt.Fprintf(a.out, "reconciled %d selected issue(s)\n", len(result.Reconciliation.Issues))
	}
	return 0
}
