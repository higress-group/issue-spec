package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
	"github.com/higress-group/issue-spec/internal/workflow"
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
	designFlag := fs.String("design", "", "design issue number or URL for --close-issues")
	implementFlag := fs.String("implement", "", "implement issue number or URL for --close-issues or self-hosted --create-pr")
	implementationPR := fs.Int("pr", 0, "merged implementation pull request number for --close-issues")
	implementationRevision := fs.String("revision", "", "expected implementation head revision for self-hosted evidence")
	archiveRevision := fs.String("archive-revision", "", "expected durable-spec head revision for self-hosted evidence")
	capability := fs.String("capability", "", "umbrella capability name; re-archiving accumulates new requirements into the existing spec by requirement title")
	output := fs.String("output", "", "output spec path")
	purpose := fs.String("purpose", "", "durable spec purpose text")
	createPR := fs.Bool("create-pr", false, "create a separate durable-spec PR from a temporary git worktree")
	closeIssues := fs.Bool("close-issues", false, "close proposal/design/implement issues after a merged implementation PR is archived")
	branch := fs.String("branch", "", "branch name for --create-pr")
	base := fs.String("base", "main", "base branch for --create-pr")
	title := fs.String("title", "", "pull request title for --create-pr")
	bodyFile := fs.String("body-file", "", "pull request body file for --create-pr")
	draft := fs.Bool("draft", false, "create PR as draft")
	commitMessage := fs.String("commit-message", "", "commit message for --create-pr")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
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
	profile, _, err := auth.ResolveProfile(a.profileName, *host)
	if err != nil {
		a.errorf("resolve archive profile: %v\n", err)
		return 1
	}
	selfHosted := profile.Kind == auth.ProfileKindHosted
	closeImplementFlag := *implementFlag
	if selfHosted && *createPR && !*closeIssues {
		// --implement selects the native code_change authority for creation; it is
		// not a close-set input until --close-issues is requested.
		closeImplementFlag = ""
	}
	closeSet, err := parseArchiveCloseIssueSetMode(*closeIssues, proposalIssue, *designFlag, closeImplementFlag, *implementationPR, !selfHosted)
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	client, token, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for archive durable-spec on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	var closePlan archiveCloseIssuePlan
	if selfHosted && closeSet.Enabled {
		if *implementationPR > 0 {
			a.errorf("--pr is not a self-hosted code authority; implementation and archive references are read from the implement issue\n")
			return 2
		}
		if *createPR {
			a.errorf("self-hosted --create-pr cannot close issues in the same invocation; merge the durable change and re-run with --close-issues\n")
			return 2
		}
		implementationGate, _, gateErr := a.externalGate(ctx, *host, token.Value, repo, closeSet.Implement,
			"code_change", *implementationRevision, coreevidence.GateMerge)
		mergePolicy, completionPolicy, policyErr := archiveImplementationReviewCompletionPolicy(implementationGate.Target)
		if policyErr != nil {
			a.errorf("implementation review completion policy: %v\n", policyErr)
			return 1
		}
		implementationGate.ReviewCompletionPolicy = completionPolicy
		validationNow := time.Now().UTC()
		if completionPolicy.Required && validateReviewCompletionTarget(implementationGate.Target) == nil &&
			validateExactSnapshotIdentity(implementationGate.Snapshot, implementationGate.Target) == nil {
			// Project the repository's review requirement onto the CLI-owned REVIEW
			// completion, then re-evaluate every remaining provider fact. This keeps
			// merge/check failures and open findings authoritative while permitting a
			// clean zero-finding review to use the workflow completion carrier.
			implementationGate = evaluateArchiveImplementationMerge(implementationGate, mergePolicy, validationNow)
			if implementationGate.Evaluation.Passed {
				gateErr = nil
			} else {
				gateErr = externalGateFailure("code_change", implementationGate.Evaluation)
			}
		}
		if gateErr != nil {
			a.errorf("implementation merge evidence: %v\n", gateErr)
			return 1
		}
		archiveGate, _, gateErr := a.externalGate(ctx, *host, token.Value, repo, closeSet.Implement,
			"archive_change", *archiveRevision, coreevidence.GateArchive)
		if gateErr != nil {
			a.errorf("durable archive evidence: %v\n", gateErr)
			return 1
		}
		if implementationGate.Target.Reference.ProviderKey != archiveGate.Target.Reference.ProviderKey ||
			implementationGate.Target.Reference.ExternalRepository != archiveGate.Target.Reference.ExternalRepository {
			a.errorf("implementation and archive references must use the same provider and external repository\n")
			return 1
		}
		artifacts, collectErr := collectArtifacts(ctx, client, repo, closeSet.Implement)
		if collectErr != nil {
			a.errorf("read implement issue comments: %v\n", collectErr)
			return 1
		}
		if !processArtifactsLinkPullRequest(artifacts, implementationGate.Target.CanonicalURL) {
			a.errorf("implement issue #%d has no active PROCESS linked to implementation change %s\n",
				closeSet.Implement, implementationGate.Target.CanonicalURL)
			return 1
		}
		if completionErr := validateArchiveImplementationReviewCompletion(artifacts, implementationGate, validationNow); completionErr != nil {
			a.errorf("implementation review completion: %v\n", completionErr)
			return 1
		}
		closePlan = archiveCloseIssuePlan{Enabled: true, Issues: archiveCloseIssuePlanRefs(archiveCloseIssueRefs(closeSet)),
			ImplementationEvidence: &implementationGate.Consumption, ArchiveEvidence: &archiveGate.Consumption}
	} else {
		closePlan, err = validateArchiveCloseIssuePlan(ctx, client, repo, closeSet)
		if err != nil {
			a.errorf("%v\n", err)
			return 1
		}
	}
	issue, specs, err := fetchDurableSpecSources(ctx, client, repo, proposalIssue)
	if err != nil {
		a.errorf("%v\n", err)
		return 1
	}
	outputPath := strings.TrimSpace(*output)
	outputSelection := workflow.SelectArchivePath(".", *capability, outputPath)
	outputPath = outputSelection.Path
	if *createPR {
		var externalCreate func(context.Context, string, string, string, string) (codereview.MutationResult, error)
		if selfHosted {
			implementIssue, parseErr := parseIssueFlag(*implementFlag, "implement")
			if parseErr != nil {
				a.errorf("%v\n", parseErr)
				return 2
			}
			target, provider, native, _, preflightErr := a.externalMutationTarget(ctx, *host, token.Value, repo,
				implementIssue, "code_change", *implementationRevision, codereview.CapabilityChangeCreate)
			if preflightErr != nil {
				a.errorf("archive change capability preflight: %v\n", preflightErr)
				return 1
			}
			externalCreate = func(callCtx context.Context, changeTitle, changeBody, headRevision, baseRevision string) (codereview.MutationResult, error) {
				return createExternalArchiveChange(callCtx, provider, native, target, codereview.MutationRequest{Kind: codereview.MutationCreateChange,
					Reference: codereview.Reference{ProviderKey: target.Reference.ProviderKey,
						ExternalRepository: target.Reference.ExternalRepository}, Title: changeTitle, Body: changeBody,
					BaseRevision: baseRevision, HeadRevision: headRevision,
					Metadata: map[string]any{"kind": "durable_spec", "capability": *capability, "implement_issue": implementIssue}})
			}
		}
		prResult, err := a.createDurableSpecPR(ctx, client, repo, issue.HTMLURL, specs, durableSpecPROptions{
			Capability:     *capability,
			OutputPath:     outputPath,
			Purpose:        *purpose,
			Branch:         *branch,
			Base:           *base,
			Title:          *title,
			BodyFile:       *bodyFile,
			Draft:          *draft,
			CommitMessage:  *commitMessage,
			ExternalCreate: externalCreate,
		})
		if err != nil {
			a.errorf("create durable spec PR: %v\n", err)
			return 1
		}
		if err := applyArchiveCloseIssuePlan(ctx, client, repo, closePlan, prResult); err != nil {
			a.errorf("%v\n", err)
			return 1
		}
		if *jsonOut {
			prResult["output_source"] = outputSelection.Source
			prResult["legacy_output"] = outputSelection.Legacy
			return a.outputJSON(prResult)
		}
		if selfHosted {
			fmt.Fprintf(a.out, "created durable spec external change: %s\n", prResult["change_url"])
		} else {
			fmt.Fprintf(a.out, "created durable spec PR: %s\n", prResult["pr_url"])
		}
		if outputSelection.Legacy {
			fmt.Fprintf(a.out, "legacy durable spec path selected: %s\n", outputPath)
		}
		printArchiveClosedIssues(a.out, closePlan)
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
	result := map[string]any{"ok": true, "capability": *capability, "output": outputPath, "output_source": outputSelection.Source, "legacy_output": outputSelection.Legacy, "proposal": issue.HTMLURL, "spec_count": len(specs)}
	if err := applyArchiveCloseIssuePlan(ctx, client, repo, closePlan, result); err != nil {
		a.errorf("%v\n", err)
		return 1
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "wrote durable spec draft for %s to %s\n", *capability, outputPath)
	if outputSelection.Legacy {
		fmt.Fprintf(a.out, "legacy durable spec path selected: %s\n", outputPath)
	}
	printArchiveClosedIssues(a.out, closePlan)
	return 0
}

func archiveImplementationReviewCompletionPolicy(target coreevidence.NativeTarget) (coreevidence.Policy, ReviewCompletionPolicy, error) {
	plan, err := workflow.Resolve(".")
	if err != nil {
		return coreevidence.Policy{}, ReviewCompletionPolicy{}, err
	}
	policy, err := mergedEvidencePolicy(plan.Config.ExternalCode, target.Policy)
	if err != nil {
		return coreevidence.Policy{}, ReviewCompletionPolicy{}, err
	}
	completion := projectReviewCompletionPolicy(&policy)
	return policy, completion, nil
}

func evaluateArchiveImplementationMerge(implementationGate externalGateResult, policy coreevidence.Policy,
	validationNow time.Time) externalGateResult {
	implementationGate.Evaluation = coreevidence.Evaluate(implementationGate.Snapshot, policy, coreevidence.Target{
		Gate: coreevidence.GateMerge, Reference: implementationGate.Target.Reference,
		SubjectRevision: implementationGate.Target.SubjectRevision, Now: validationNow,
	})
	implementationGate.Consumption.EvidenceIDs = append([]string(nil), implementationGate.Evaluation.EvidenceIDs...)
	implementationGate.Consumption.Bindings = nil
	if implementationGate.Evaluation.Passed {
		if bindings, err := authoritativeExternalEvidenceBindings(implementationGate.Snapshot,
			implementationGate.Consumption); err == nil {
			implementationGate.Consumption.Bindings = bindings
		}
	}
	return implementationGate
}

// validateArchiveImplementationReviewCompletion is deliberately read-only and
// accepts only the implementation code_change gate. The archive_change gate is
// never passed here, so a durable-change identity cannot borrow implementation
// REVIEW completion and archive cannot create or refresh REVIEW state.
func validateArchiveImplementationReviewCompletion(artifacts []model.Artifact, implementationGate externalGateResult,
	validationNow time.Time) error {
	if !implementationGate.ReviewCompletionPolicy.Required {
		return nil
	}
	inputs := buildProcessEvidenceInputsWithExternalReview(artifacts, "", nil, reviewSyncReport{}, nil,
		&implementationGate, validationNow)
	for _, input := range inputs {
		if model.ParseProcessExecutionClass(input.Process.Comment.ID, input.Process.URL,
			input.Process.Comment.Body).Class != model.ProcessExecutionReview {
			continue
		}
		report := gates.EvaluateProcessEvidence(input, gates.TargetArchive, gates.ModeAuthoritative)
		if report.CarrierRevision.Trusted && report.CarrierRevision.Revision == implementationGate.Target.SubjectRevision {
			for _, satisfied := range report.Satisfied {
				if satisfied == "review evidence" {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("required exact-current independent REVIEW completion is missing for implementation revision %s",
		implementationGate.Target.SubjectRevision)
}

type durableSpecPROptions struct {
	Capability     string
	OutputPath     string
	Purpose        string
	Branch         string
	Base           string
	Title          string
	BodyFile       string
	Draft          bool
	CommitMessage  string
	ExternalCreate func(context.Context, string, string, string, string) (codereview.MutationResult, error)
}

type archiveCloseIssueSet struct {
	Enabled   bool
	Proposal  int
	Design    int
	Implement int
	PR        int
}

type archiveCloseIssuePlan struct {
	Enabled                bool
	PR                     github.PullRequest
	Issues                 []archiveCloseIssueRef
	ImplementationEvidence *externalEvidenceConsumption
	ArchiveEvidence        *externalEvidenceConsumption
}

type archiveCloseIssueRef struct {
	Kind   string
	Number int
}

type closedArchiveIssue struct {
	Kind   string `json:"kind"`
	Number int    `json:"number"`
	URL    string `json:"url,omitempty"`
	State  string `json:"state,omitempty"`
}

func parseArchiveCloseIssueSet(enabled bool, proposalIssue int, designFlag, implementFlag string, prNumber int) (archiveCloseIssueSet, error) {
	return parseArchiveCloseIssueSetMode(enabled, proposalIssue, designFlag, implementFlag, prNumber, true)
}

func parseArchiveCloseIssueSetMode(enabled bool, proposalIssue int, designFlag, implementFlag string, prNumber int, requirePR bool) (archiveCloseIssueSet, error) {
	hasCloseFlagInput := strings.TrimSpace(designFlag) != "" || strings.TrimSpace(implementFlag) != "" || prNumber != 0
	if !enabled {
		if hasCloseFlagInput {
			return archiveCloseIssueSet{}, fmt.Errorf("--design, --implement, and --pr require --close-issues")
		}
		return archiveCloseIssueSet{}, nil
	}
	designIssue, err := parseIssueFlag(designFlag, "design")
	if err != nil {
		return archiveCloseIssueSet{}, err
	}
	implementIssue, err := parseIssueFlag(implementFlag, "implement")
	if err != nil {
		return archiveCloseIssueSet{}, err
	}
	if requirePR && prNumber <= 0 {
		return archiveCloseIssueSet{}, fmt.Errorf("--pr must be a positive pull request number")
	}
	return archiveCloseIssueSet{
		Enabled:   true,
		Proposal:  proposalIssue,
		Design:    designIssue,
		Implement: implementIssue,
		PR:        prNumber,
	}, nil
}

func validateArchiveCloseIssuePlan(ctx context.Context, client github.Operations, repo string, closeSet archiveCloseIssueSet) (archiveCloseIssuePlan, error) {
	if !closeSet.Enabled {
		return archiveCloseIssuePlan{}, nil
	}
	pr, err := client.GetPullRequest(ctx, repo, closeSet.PR)
	if err != nil {
		return archiveCloseIssuePlan{}, fmt.Errorf("read implementation PR #%d: %w", closeSet.PR, err)
	}
	if !pr.Merged {
		return archiveCloseIssuePlan{}, fmt.Errorf("implementation PR #%d must be merged before closing issue-spec issues", closeSet.PR)
	}
	if strings.TrimSpace(pr.HTMLURL) == "" {
		return archiveCloseIssuePlan{}, fmt.Errorf("implementation PR #%d is missing html_url", closeSet.PR)
	}
	refs := archiveCloseIssueRefs(closeSet)
	if err := model.VerifyIssueClosureBlock(pr.Body, refs); err != nil {
		return archiveCloseIssuePlan{}, fmt.Errorf("implementation PR #%d issue closing links: %w", closeSet.PR, err)
	}
	artifacts, err := collectArtifacts(ctx, client, repo, closeSet.Implement)
	if err != nil {
		return archiveCloseIssuePlan{}, fmt.Errorf("read implement issue #%d comments: %w", closeSet.Implement, err)
	}
	if !processArtifactsLinkPullRequest(artifacts, pr.HTMLURL) {
		return archiveCloseIssuePlan{}, fmt.Errorf("implement issue #%d has no active PROCESS linked to implementation PR %s", closeSet.Implement, pr.HTMLURL)
	}
	return archiveCloseIssuePlan{
		Enabled: true,
		PR:      pr,
		Issues:  archiveCloseIssuePlanRefs(refs),
	}, nil
}

func archiveCloseIssueRefs(closeSet archiveCloseIssueSet) []model.IssueClosureRef {
	return []model.IssueClosureRef{
		{Kind: "proposal", Number: closeSet.Proposal},
		{Kind: "design", Number: closeSet.Design},
		{Kind: "implement", Number: closeSet.Implement},
	}
}

func archiveCloseIssuePlanRefs(refs []model.IssueClosureRef) []archiveCloseIssueRef {
	issueRefs := make([]archiveCloseIssueRef, 0, len(refs))
	for _, ref := range refs {
		issueRefs = append(issueRefs, archiveCloseIssueRef{Kind: ref.Kind, Number: ref.Number})
	}
	return issueRefs
}

func processArtifactsLinkPullRequest(artifacts []model.Artifact, prURL string) bool {
	activity := selectProcessActivity(artifacts)
	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.Type != "PROCESS" || !activity.active[tc.ID] {
			continue
		}
		if linkValuesContain(tc.Links["PR"], prURL) {
			return true
		}
	}
	return false
}

func applyArchiveCloseIssuePlan(ctx context.Context, client github.Operations, repo string, plan archiveCloseIssuePlan, result map[string]any) error {
	if !plan.Enabled {
		return nil
	}
	state := "closed"
	closed := make([]closedArchiveIssue, 0, len(plan.Issues))
	for _, issueRef := range plan.Issues {
		issue, err := client.UpdateIssue(ctx, repo, issueRef.Number, github.UpdateIssueOptions{State: &state})
		if err != nil {
			return fmt.Errorf("close %s issue #%d: %w", issueRef.Kind, issueRef.Number, err)
		}
		closed = append(closed, closedArchiveIssue{
			Kind:   issueRef.Kind,
			Number: issueRef.Number,
			URL:    issue.HTMLURL,
			State:  issue.State,
		})
	}
	result["closed_issues"] = closed
	if plan.ImplementationEvidence != nil && plan.ArchiveEvidence != nil {
		result["implementation_evidence"] = plan.ImplementationEvidence
		result["archive_evidence"] = plan.ArchiveEvidence
	} else {
		result["implementation_pr"] = plan.PR.Number
		result["implementation_pr_url"] = plan.PR.HTMLURL
	}
	return nil
}

func printArchiveClosedIssues(out interface{ Write([]byte) (int, error) }, plan archiveCloseIssuePlan) {
	if !plan.Enabled || len(plan.Issues) == 0 {
		return
	}
	parts := make([]string, 0, len(plan.Issues))
	for _, issueRef := range plan.Issues {
		parts = append(parts, fmt.Sprintf("%s #%d", issueRef.Kind, issueRef.Number))
	}
	if plan.ImplementationEvidence != nil && plan.ArchiveEvidence != nil {
		fmt.Fprintf(out, "closed issue-spec issues after implementation revision %s and archive revision %s: %s\n",
			plan.ImplementationEvidence.SubjectRevision, plan.ArchiveEvidence.SubjectRevision, strings.Join(parts, ", "))
		return
	}
	fmt.Fprintf(out, "closed issue-spec issues after merged PR %s: %s\n", plan.PR.HTMLURL, strings.Join(parts, ", "))
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
	var malformed []string
	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.Type != "SPEC" || tc.Status == "superseded" {
			continue
		}
		// Recompute canonical validity from the remote body and block archive
		// before rendering when an active SPEC source is malformed.
		for _, d := range model.ValidateArtifact(artifact) {
			url := d.URL
			if url == "" {
				url = "N/A"
			}
			malformed = append(malformed, fmt.Sprintf("%s (%s): %s", d.ID, url, d.Message))
		}
		specs = append(specs, templates.SpecSource{ID: tc.ID, URL: artifact.URL, Body: tc.Body})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	if len(specs) == 0 {
		return github.Issue{}, nil, fmt.Errorf("proposal issue #%d has no active SPEC comments", proposalIssue)
	}
	if len(malformed) > 0 {
		sort.Strings(malformed)
		return github.Issue{}, nil, fmt.Errorf("proposal issue #%d has malformed active SPEC comments; regenerate or supersede them before archive:\n- %s", proposalIssue, strings.Join(malformed, "\n- "))
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
	// Push first so an external provider always receives an immutable revision it
	// can read. A later mutation failure deliberately leaves only the branch: no
	// archive_change reference is written until the provider confirms creation.
	if err := runGit(ctx, tmp, "push", "-u", "origin", opts.Branch, "--force-with-lease"); err != nil {
		return nil, err
	}
	if opts.ExternalCreate != nil {
		headRevision, err := gitOutput(ctx, tmp, "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		baseRevision, err := gitOutput(ctx, tmp, "rev-parse", "origin/"+opts.Base)
		if err != nil {
			return nil, err
		}
		created, err := opts.ExternalCreate(ctx, opts.Title, body, headRevision, baseRevision)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "changed": true, "branch": opts.Branch, "base": opts.Base,
			"output": opts.OutputPath, "change_id": created.Reference.ChangeID, "change_url": created.CanonicalURL,
			"provider_key": created.Reference.ProviderKey, "external_repository": created.Reference.ExternalRepository,
			"head_revision": headRevision, "base_revision": baseRevision}, nil
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

func createExternalArchiveChange(ctx context.Context, provider codereview.MutationProvider, native nativeEvidenceProvider,
	target coreevidence.NativeTarget, request codereview.MutationRequest) (codereview.MutationResult, error) {
	created, err := codereview.Mutate(ctx, provider, request)
	if err != nil {
		return codereview.MutationResult{}, err
	}
	if err := native.UpsertArchiveReference(ctx, target, created.Reference, created.CanonicalURL,
		request.HeadRevision, request.BaseRevision); err != nil {
		return codereview.MutationResult{}, err
	}
	return created, nil
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

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}
