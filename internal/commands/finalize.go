package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	changegraph "github.com/higress-group/issue-spec/internal/change"
	"github.com/higress-group/issue-spec/internal/finalization"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/reconcile"
)

func (a *app) runFinalize(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec finalize preview|apply|detail ...\n")
		return 2
	}
	switch args[0] {
	case "preview":
		return a.runFinalizePreview(ctx, args[1:])
	case "apply":
		return a.runFinalizeApply(ctx, args[1:])
	case "detail":
		return a.runFinalizeDetail(args[1:])
	default:
		a.errorf("unknown finalize command %q\n", args[0])
		return 2
	}
}

type finalizeCompact struct {
	OK               bool                   `json:"ok"`
	PlanDigest       string                 `json:"plan_digest"`
	PlanPath         string                 `json:"plan_path,omitempty"`
	SubjectRevision  string                 `json:"subject_revision"`
	BaselineRevision string                 `json:"baseline_revision"`
	GraphDigest      string                 `json:"graph_digest"`
	ActiveProcessIDs []string               `json:"active_process_ids"`
	OperationCount   int                    `json:"operation_count"`
	BlockerCount     int                    `json:"blocker_count"`
	Blockers         []finalization.Blocker `json:"blockers,omitempty"`
}

func (a *app) runFinalizePreview(ctx context.Context, args []string) int {
	fs := newFlagSet("finalize preview", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	proposalFlag := fs.String("proposal", "", "Proposal Issue number or URL")
	designFlag := fs.String("design", "", "Design Issue number or URL")
	implementFlag := fs.String("implement", "", "Implement Issue number or URL")
	prFlag := fs.String("pr", "", "pull request number or URL")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	intentPath := fs.String("intent-file", "", "version-1 finalization intent JSON")
	planPath := fs.String("plan-out", "", "absolute output path for the frozen plan")
	jsonOut := fs.Bool("json", false, "write compact JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	proposal, err := parseIssueFlag(*proposalFlag, "proposal")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	design, err := parseIssueFlag(*designFlag, "design")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	implement, err := parseIssueFlag(*implementFlag, "implement")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	pr, err := parseIssueFlag(*prFlag, "pr")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	if err := validateFinalizationPath(*planPath, true); err != nil {
		a.errorf("--plan-out: %v\n", err)
		return 2
	}
	intent, err := readFinalizationIntent(*intentPath)
	if err != nil {
		a.errorf("read finalization intent: %v\n", err)
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("finalize preview backend: %v\n", err)
		return 1
	}
	located, err := changegraph.LocateFromImplement(ctx, client, repo, implement)
	if err != nil {
		a.errorf("locate finalization change: %v\n", err)
		return 1
	}
	if located.Proposal.Number != proposal || located.Design.Number != design || located.Implement.Number != implement {
		a.errorf("finalization issue identities differ from canonical change: proposal=%d design=%d implement=%d\n",
			located.Proposal.Number, located.Design.Number, located.Implement.Number)
		return 2
	}
	facts, err := collectPullRequestGateFacts(ctx, client, repo, pr)
	if err != nil {
		a.errorf("observe pull request authority: %v\n", err)
		return 1
	}
	providerBase := strings.TrimSpace(facts.PullRequest.Base.SHA)
	if providerBase == "" {
		a.errorf("resolve exact pull request baseline: provider base revision is missing\n")
		return 1
	}
	baseline, err := a.finalizationBaseline(ctx, facts.PullRequest.Head.SHA, providerBase)
	if err != nil {
		a.errorf("resolve exact pull request baseline: %v\n", err)
		return 1
	}
	evidenceDigest, err := finalizationEvidenceDigest(facts)
	if err != nil {
		a.errorf("digest provider evidence: %v\n", err)
		return 1
	}
	observations, err := observeFinalizationComments(ctx, client, repo, proposal, design, implement)
	if err != nil {
		a.errorf("observe finalization snapshot: %v\n", err)
		return 1
	}
	projected, selection, err := finalization.ProjectIntentForImplement(intent, implement, observations)
	if err != nil {
		a.errorf("compile explicit finalization intent: %v\n", err)
		return 2
	}
	projected, err = finalization.ProjectLifecycle(projected, selection)
	if err != nil {
		a.errorf("project finalization lifecycle: %v\n", err)
		return 1
	}
	report, err := buildFinalVerifyReport(projected, located.Proposal.URL, finalVerifyOptions{
		ImplementIssue: implement,
		PR:             pr, PRURL: facts.PullRequest.HTMLURL, ExpectedRevision: facts.PullRequest.Head.SHA, RationaleRequired: true,
		RationaleComments: facts.ReviewComments, PRStatus: facts.Status, PRCheckRuns: facts.CheckRuns, PRCommits: facts.Commits,
	})
	if err != nil {
		a.errorf("evaluate frozen finalization evidence: %v\n", err)
		return 1
	}
	blockers := finalizationGateBlockers(report.Gate.Diagnostics)
	plan, err := finalization.Compile(finalization.CompileInput{Repository: repo, Hostname: *host,
		Proposal: proposal, Design: design, Implement: implement,
		Subject: finalization.Subject{PullRequest: pr, URL: facts.PullRequest.HTMLURL, SubjectRevision: strings.TrimSpace(facts.PullRequest.Head.SHA),
			ProviderBaseRevision: providerBase, BaselineRevision: baseline, ProviderEvidenceDigest: evidenceDigest},
		Intent: intent, Observations: observations, LifecycleReady: len(blockers) == 0, LifecycleBlocks: blockers})
	if err != nil {
		a.errorf("compile finalization plan: %v\n", err)
		return 1
	}
	data, err := finalization.CanonicalJSON(plan)
	if err != nil {
		a.errorf("encode finalization plan: %v\n", err)
		return 1
	}
	if err := writeAtomicFinalizationPlan(*planPath, data); err != nil {
		a.errorf("write finalization plan: %v\n", err)
		return 1
	}
	compact := compactFinalization(plan, *planPath)
	if *jsonOut {
		if code := a.outputJSON(compact); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(a.out, "finalize preview %s: operations=%d blockers=%d active=%d plan=%s\n",
			plan.PlanDigest, len(plan.Reconcile.Operations), len(plan.Blockers), len(plan.Selection.ActiveProcessIDs), *planPath)
		for _, blocker := range plan.Blockers {
			fmt.Fprintf(a.out, "- %s %s: %s\n", blocker.Code, blocker.ArtifactID, blocker.Message)
		}
	}
	// Preview succeeds when it produced a valid plan, including blocker-only
	// plans; blocker state is data consumed by detail/apply, not a preview error.
	return 0
}

func finalizationGateBlockers(diagnostics []gates.Diagnostic) []finalization.Blocker {
	var result []finalization.Blocker
	for _, diagnostic := range diagnostics {
		if !diagnostic.Blocking || diagnostic.Code == gates.CodeProcessNotDone || diagnostic.Code == gates.CodeTaskNotDone {
			continue
		}
		result = append(result, finalization.Blocker{Code: diagnostic.Code, ArtifactID: diagnostic.Artifact.ID, Message: diagnostic.Message})
	}
	return result
}

func readFinalizationIntent(path string) (finalization.Intent, error) {
	if strings.TrimSpace(path) == "" || path == "-" {
		return finalization.Intent{}, errors.New("--intent-file must name a file")
	}
	file, err := os.Open(path)
	if err != nil {
		return finalization.Intent{}, err
	}
	defer file.Close()
	return finalization.ReadIntent(file)
}

func observeFinalizationComments(ctx context.Context, backend github.Backend, repo string, issues ...int) ([]finalization.Observation, error) {
	var result []finalization.Observation
	for _, issue := range issues {
		comments, err := backend.ListIssueComments(ctx, repo, issue)
		if err != nil {
			return nil, fmt.Errorf("list issue %d comments: %w", issue, err)
		}
		for _, comment := range comments {
			if !model.IsLikelyTyped(comment.Body) && !model.IsLikelyCodeChangeRationale(comment.Body) {
				continue
			}
			exact, version, err := observeExactFinalizationComment(ctx, backend, repo, comment)
			if err != nil {
				return nil, fmt.Errorf("observe comment %d: %w", comment.ID, err)
			}
			result = append(result, finalization.Observation{Issue: issue, CommentID: exact.ID, URL: exact.HTMLURL,
				APIURL: exact.URL, Body: exact.Body, RepresentationVersion: version})
		}
	}
	return result, nil
}

func observeExactFinalizationComment(ctx context.Context, backend github.Backend, repo string, listed github.Comment) (github.Comment, int64, error) {
	if conditional, ok := any(backend).(github.ConditionalCommentBackend); ok {
		observed, err := conditional.GetCommentRepresentation(ctx, repo, listed.ID)
		if err == nil {
			if observed.Comment.ID != listed.ID || observed.Comment.Body == "" {
				return github.Comment{}, 0, errors.New("conditional representation identity is incomplete")
			}
			return observed.Comment, observed.RepresentationVersion, nil
		}
		if !errors.Is(err, github.ErrConditionalCommentMutationUnsupported) {
			return github.Comment{}, 0, err
		}
	}
	if observer, ok := any(backend).(github.IssueCommentObserver); ok {
		observed, err := observer.ObserveIssueComment(ctx, repo, listed.ID)
		if err != nil {
			return github.Comment{}, 0, err
		}
		if observed.Comment.ID != listed.ID || observed.Comment.Body == "" {
			return github.Comment{}, 0, errors.New("provider observation identity is incomplete")
		}
		return observed.Comment, observed.RepresentationVersion, nil
	}
	return listed, 0, nil
}

func (a *app) finalizationBaseline(ctx context.Context, subject, providerBase string) (string, error) {
	resolver := a.resolveFinalizationBaseline
	if resolver == nil {
		resolver = defaultResolveFinalizationBaseline
	}
	return resolver(ctx, strings.TrimSpace(subject), strings.TrimSpace(providerBase))
}

func defaultResolveFinalizationBaseline(ctx context.Context, subject, providerBase string) (string, error) {
	if subject == "" || providerBase == "" || strings.ContainsAny(subject+providerBase, " \t\r\n\x00") {
		return "", errors.New("pull request subject and exact provider base revisions are required")
	}
	command := exec.CommandContext(ctx, "git", "merge-base", subject, providerBase)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git merge-base %s %s: %w", subject, providerBase, err)
	}
	baseline := strings.ToLower(strings.TrimSpace(string(output)))
	if baseline == "" || strings.ContainsAny(baseline, " \t\r\n") {
		return "", errors.New("git merge-base returned an invalid revision")
	}
	return baseline, nil
}

type frozenProviderEvidence struct {
	PullRequest    github.PullRequest                `json:"pull_request"`
	ReviewComments []github.PullRequestReviewComment `json:"review_comments"`
	Status         github.CombinedStatus             `json:"status"`
	CheckRuns      []github.CheckRun                 `json:"check_runs"`
	Commits        []github.PullRequestCommit        `json:"commits"`
}

func finalizationEvidenceDigest(facts pullRequestGateFacts) (string, error) {
	value := frozenProviderEvidence{PullRequest: facts.PullRequest,
		ReviewComments: append([]github.PullRequestReviewComment(nil), facts.ReviewComments...),
		Status:         facts.Status, CheckRuns: append([]github.CheckRun(nil), facts.CheckRuns...),
		Commits: append([]github.PullRequestCommit(nil), facts.Commits...)}
	sort.Slice(value.ReviewComments, func(i, j int) bool { return value.ReviewComments[i].ID < value.ReviewComments[j].ID })
	sort.Slice(value.Status.Statuses, func(i, j int) bool {
		if value.Status.Statuses[i].Context != value.Status.Statuses[j].Context {
			return value.Status.Statuses[i].Context < value.Status.Statuses[j].Context
		}
		return value.Status.Statuses[i].TargetURL < value.Status.Statuses[j].TargetURL
	})
	sort.Slice(value.CheckRuns, func(i, j int) bool {
		if value.CheckRuns[i].Name != value.CheckRuns[j].Name {
			return value.CheckRuns[i].Name < value.CheckRuns[j].Name
		}
		return value.CheckRuns[i].ID < value.CheckRuns[j].ID
	})
	sort.Slice(value.Commits, func(i, j int) bool { return value.Commits[i].SHA < value.Commits[j].SHA })
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func validateFinalizationPath(path string, rejectWorktree bool) error {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("path must be absolute")
	}
	if !rejectWorktree {
		return nil
	}
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		return nil
	}
	root := filepath.Clean(strings.TrimSpace(string(output)))
	clean := filepath.Clean(path)
	relative, err := filepath.Rel(root, clean)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path must be outside the current managed worktree %s", root)
	}
	return nil
}

func writeAtomicFinalizationPlan(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".issue-spec-finalization-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func compactFinalization(plan finalization.Plan, path string) finalizeCompact {
	return finalizeCompact{OK: len(plan.Blockers) == 0, PlanDigest: plan.PlanDigest, PlanPath: path,
		SubjectRevision: plan.Subject.SubjectRevision, BaselineRevision: plan.Subject.BaselineRevision,
		GraphDigest: plan.GraphDigest, ActiveProcessIDs: append([]string(nil), plan.Selection.ActiveProcessIDs...),
		OperationCount: len(plan.Reconcile.Operations), BlockerCount: len(plan.Blockers), Blockers: append([]finalization.Blocker(nil), plan.Blockers...)}
}

type finalizeMutationDetail struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	DependsOn []string          `json:"depends_on,omitempty"`
	Target    reconcile.Target  `json:"target"`
	Peer      *reconcile.Target `json:"peer,omitempty"`
	Status    string            `json:"status,omitempty"`
}

type finalizeDetail struct {
	Version         int                           `json:"version"`
	PlanDigest      string                        `json:"plan_digest"`
	Repository      string                        `json:"repository"`
	Proposal        int                           `json:"proposal"`
	Design          int                           `json:"design"`
	Implement       int                           `json:"implement"`
	Subject         finalization.Subject          `json:"subject"`
	Intent          finalization.Intent           `json:"intent"`
	GraphDigest     string                        `json:"graph_digest"`
	Selection       finalization.SelectionSummary `json:"selection"`
	Representations []finalization.Representation `json:"representations"`
	Blockers        []finalization.Blocker        `json:"blockers,omitempty"`
	Mutations       []finalizeMutationDetail      `json:"mutations"`
}

func (a *app) runFinalizeDetail(args []string) int {
	fs := newFlagSet("finalize detail", a.err)
	planPath := fs.String("plan", "", "absolute frozen finalization plan path")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if err := validateFinalizationPath(*planPath, false); err != nil {
		a.errorf("--plan: %v\n", err)
		return 2
	}
	plan, err := readFinalizationPlan(*planPath)
	if err != nil {
		a.errorf("read finalization plan: %v\n", err)
		return 2
	}
	detail := detailFinalization(plan)
	if *jsonOut {
		return a.outputJSON(detail)
	}
	fmt.Fprintf(a.out, "finalize plan %s subject=%s baseline=%s graph=%s\n", plan.PlanDigest,
		plan.Subject.SubjectRevision, plan.Subject.BaselineRevision, plan.GraphDigest)
	for _, chain := range plan.Selection.Historical {
		fmt.Fprintf(a.out, "- %s -> %s (%s)\n", chain.ProcessID, chain.ActiveSinkID, strings.Join(chain.Chain, " -> "))
	}
	for _, mutation := range detail.Mutations {
		fmt.Fprintf(a.out, "- %s %s %s/%s\n", mutation.ID, mutation.Kind, mutation.Target.Type, mutation.Target.ID)
	}
	for _, blocker := range plan.Blockers {
		fmt.Fprintf(a.out, "- blocker %s %s: %s\n", blocker.Code, blocker.ArtifactID, blocker.Message)
	}
	return 0
}

func detailFinalization(plan finalization.Plan) finalizeDetail {
	detail := finalizeDetail{Version: plan.Version, PlanDigest: plan.PlanDigest, Repository: plan.Repository,
		Proposal: plan.Proposal, Design: plan.Design, Implement: plan.Implement, Subject: plan.Subject, Intent: plan.Intent,
		GraphDigest: plan.GraphDigest, Selection: plan.Selection,
		Representations: append([]finalization.Representation(nil), plan.Representations...), Blockers: append([]finalization.Blocker(nil), plan.Blockers...)}
	for _, operation := range plan.Reconcile.Operations {
		detail.Mutations = append(detail.Mutations, finalizeMutationDetail{ID: operation.ID, Kind: operation.Kind,
			DependsOn: append([]string(nil), operation.DependsOn...), Target: operation.Target, Peer: operation.Desired.Peer, Status: operation.Desired.Status})
	}
	return detail
}

func readFinalizationPlan(path string) (finalization.Plan, error) {
	file, err := os.Open(path)
	if err != nil {
		return finalization.Plan{}, err
	}
	defer file.Close()
	return finalization.ReadPlan(file)
}

type finalizeApplyResult struct {
	OK             bool                          `json:"ok"`
	PlanDigest     string                        `json:"plan_digest"`
	Checkpoint     string                        `json:"checkpoint"`
	Reconcile      reconcile.Result              `json:"reconcile"`
	FinalSelection finalization.SelectionSummary `json:"final_selection"`
	FinalGateReady bool                          `json:"final_gate_ready"`
	Blockers       []finalization.Blocker        `json:"blockers,omitempty"`
}

func (a *app) runFinalizeApply(ctx context.Context, args []string) int {
	fs := newFlagSet("finalize apply", a.err)
	planPath := fs.String("plan", "", "absolute frozen finalization plan path")
	checkpointPath := fs.String("checkpoint", "", "absolute durable checkpoint path")
	allowNonAtomic := fs.Bool("allow-nonatomic", false, "allow guarded non-atomic comment updates")
	jsonOut := fs.Bool("json", false, "write compact JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	for name, path := range map[string]string{"plan": *planPath, "checkpoint": *checkpointPath} {
		if err := validateFinalizationPath(path, false); err != nil {
			a.errorf("--%s: %v\n", name, err)
			return 2
		}
	}
	plan, err := readFinalizationPlan(*planPath)
	if err != nil {
		a.errorf("read finalization plan: %v\n", err)
		return 2
	}
	client, _, err := a.clientFor(ctx, plan.Hostname)
	if err != nil {
		a.errorf("finalize apply backend: %v\n", err)
		return 1
	}
	runtimePlan := plan.Reconcile
	runtimePlan.AllowNonAtomic = runtimePlan.AllowNonAtomic || *allowNonAtomic
	runtimePlan.PlanDigest = ""
	if len(runtimePlan.Operations) != 0 {
		_, digest, validateErr := reconcile.Validate(runtimePlan)
		if validateErr != nil {
			a.errorf("validate runtime reconcile plan: %v\n", validateErr)
			return 2
		}
		runtimePlan.PlanDigest = digest
	}
	if err := a.validateFinalizationAuthority(ctx, client, plan, true); err != nil {
		a.errorf("finalization authority drift: %v\n", err)
		return 1
	}
	checkpoint := reconcile.Checkpoint{Version: 1, PlanDigest: runtimePlan.PlanDigest, Completed: map[string]string{}}
	if len(runtimePlan.Operations) != 0 {
		checkpoint, err = reconcile.LoadCheckpoint(*checkpointPath, runtimePlan.PlanDigest)
		if err != nil {
			a.errorf("load finalization checkpoint: %v\n", err)
			return 1
		}
	}
	if _, err := validateFinalizationRepresentations(ctx, client, plan, checkpoint.Completed); err != nil {
		a.errorf("finalization snapshot drift: %v\n", err)
		return 1
	}
	guarded := &finalizationGuardBackend{Backend: client, guard: func() error {
		return a.validateFinalizationAuthority(ctx, client, plan, false)
	}}
	reconcileResult := reconcile.Result{OK: true, PlanDigest: runtimePlan.PlanDigest, Checkpoint: *checkpointPath, Atomic: true}
	if len(runtimePlan.Operations) != 0 {
		if len(checkpoint.Completed) == 0 {
			reconcileResult, err = (reconcile.Engine{Backend: guarded}).Run(ctx, runtimePlan, *checkpointPath)
		} else {
			reconcileResult, err = runRemainingFinalizationOperations(ctx, guarded, runtimePlan, *checkpointPath, checkpoint)
		}
		if err != nil {
			a.errorf("finalize apply reconcile: %v\n", err)
			return 1
		}
	}
	if len(runtimePlan.Operations) != 0 {
		checkpoint, err = reconcile.LoadCheckpoint(*checkpointPath, runtimePlan.PlanDigest)
		if err != nil {
			a.errorf("reload finalization checkpoint: %v\n", err)
			return 1
		}
	}
	if _, err := validateFinalizationRepresentations(ctx, client, plan, checkpoint.Completed); err != nil {
		a.errorf("finalization final representation drift: %v\n", err)
		return 1
	}
	finalFacts, err := collectPullRequestGateFacts(ctx, client, plan.Repository, plan.Subject.PullRequest)
	if err != nil {
		a.errorf("final provider evidence observation: %v\n", err)
		return 1
	}
	if err := a.validateFinalizationFacts(ctx, plan, finalFacts, true); err != nil {
		a.errorf("finalization authority changed during apply: %v\n", err)
		return 1
	}
	finalObservations, err := observeFinalizationComments(ctx, client, plan.Repository, plan.Proposal, plan.Design, plan.Implement)
	if err != nil {
		a.errorf("final finalization observation: %v\n", err)
		return 1
	}
	finalArtifacts := artifactsFromFinalizationObservations(finalObservations)
	finalSelection, graphDigest, diagnostics, err := finalization.SummarizeArtifactsForImplement(finalArtifacts, plan.Implement)
	if err != nil {
		a.errorf("evaluate final finalization selection: %v\n", err)
		return 1
	}
	if len(diagnostics) != 0 || graphDigest != plan.GraphDigest {
		a.errorf("finalization graph drift: expected=%s current=%s diagnostics=%v\n", plan.GraphDigest, graphDigest, diagnostics)
		return 1
	}
	located, err := changegraph.LocateFromImplement(ctx, client, plan.Repository, plan.Implement)
	if err != nil {
		a.errorf("locate final finalization change: %v\n", err)
		return 1
	}
	if located.Proposal.Number != plan.Proposal || located.Design.Number != plan.Design || located.Implement.Number != plan.Implement {
		a.errorf("finalization issue identities changed during apply\n")
		return 1
	}
	finalReport, err := buildFinalVerifyReport(finalArtifacts, located.Proposal.URL, finalVerifyOptions{
		ImplementIssue: plan.Implement,
		PR:             plan.Subject.PullRequest, PRURL: finalFacts.PullRequest.HTMLURL, ExpectedRevision: plan.Subject.SubjectRevision,
		RationaleRequired: true, RationaleComments: finalFacts.ReviewComments, PRStatus: finalFacts.Status,
		PRCheckRuns: finalFacts.CheckRuns, PRCommits: finalFacts.Commits,
	})
	if err != nil {
		a.errorf("evaluate final shared verification gates: %v\n", err)
		return 1
	}
	result := finalizeApplyResult{OK: reconcileResult.OK && len(plan.Blockers) == 0 && finalReport.OK, PlanDigest: plan.PlanDigest,
		Checkpoint: *checkpointPath, Reconcile: reconcileResult, FinalSelection: finalSelection,
		FinalGateReady: finalReport.OK,
		Blockers:       append([]finalization.Blocker(nil), plan.Blockers...)}
	if *jsonOut {
		if code := a.outputJSON(result); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(a.out, "finalize apply %s: updated=%d unchanged=%d conflicted=%d pending=%d blockers=%d\n",
			plan.PlanDigest, reconcileResult.Updated, reconcileResult.Unchanged, reconcileResult.Conflicted, reconcileResult.Pending, len(plan.Blockers))
	}
	if !result.OK {
		return 1
	}
	return 0
}

func (a *app) validateFinalizationAuthority(ctx context.Context, backend github.Backend, plan finalization.Plan, includeEvidence bool) error {
	if includeEvidence {
		facts, err := collectPullRequestGateFacts(ctx, backend, plan.Repository, plan.Subject.PullRequest)
		if err != nil {
			return err
		}
		return a.validateFinalizationFacts(ctx, plan, facts, true)
	}
	pr, err := backend.GetPullRequest(ctx, plan.Repository, plan.Subject.PullRequest)
	if err != nil {
		return err
	}
	return a.validateFinalizationFacts(ctx, plan, pullRequestGateFacts{PullRequest: pr}, false)
}

func (a *app) validateFinalizationFacts(ctx context.Context, plan finalization.Plan, facts pullRequestGateFacts, includeEvidence bool) error {
	pr := facts.PullRequest
	if pr.Number != plan.Subject.PullRequest || pr.HTMLURL != plan.Subject.URL || strings.TrimSpace(pr.Head.SHA) != plan.Subject.SubjectRevision {
		return errors.New("pull request subject identity differs from the frozen plan")
	}
	providerBase := strings.TrimSpace(pr.Base.SHA)
	if providerBase == "" || providerBase != plan.Subject.ProviderBaseRevision {
		return fmt.Errorf("provider base revision differs: expected=%s current=%s", plan.Subject.ProviderBaseRevision, providerBase)
	}
	baseline, err := a.finalizationBaseline(ctx, pr.Head.SHA, providerBase)
	if err != nil {
		return err
	}
	if baseline != plan.Subject.BaselineRevision {
		return fmt.Errorf("baseline revision differs: expected=%s current=%s", plan.Subject.BaselineRevision, baseline)
	}
	if includeEvidence {
		digest, err := finalizationEvidenceDigest(facts)
		if err != nil {
			return err
		}
		if digest != plan.Subject.ProviderEvidenceDigest {
			return fmt.Errorf("provider evidence snapshot differs: expected=%s current=%s", plan.Subject.ProviderEvidenceDigest, digest)
		}
	}
	return nil
}

type finalizationGuardBackend struct {
	github.Backend
	guard func() error
}

func (b *finalizationGuardBackend) CreateComment(ctx context.Context, repo string, issue int, body string) (github.Comment, error) {
	if err := b.guard(); err != nil {
		return github.Comment{}, err
	}
	return b.Backend.CreateComment(ctx, repo, issue, body)
}

func (b *finalizationGuardBackend) UpdateComment(ctx context.Context, repo string, commentID int64, body string) (github.Comment, error) {
	if err := b.guard(); err != nil {
		return github.Comment{}, err
	}
	return b.Backend.UpdateComment(ctx, repo, commentID, body)
}

func (b *finalizationGuardBackend) GetCommentRepresentation(ctx context.Context, repo string, commentID int64) (github.CommentRepresentation, error) {
	conditional, ok := any(b.Backend).(github.ConditionalCommentBackend)
	if !ok {
		return github.CommentRepresentation{}, github.ErrConditionalCommentMutationUnsupported
	}
	return conditional.GetCommentRepresentation(ctx, repo, commentID)
}

func (b *finalizationGuardBackend) UpdateCommentConditional(ctx context.Context, repo string, commentID, version int64, body string) (github.CommentRepresentation, error) {
	if err := b.guard(); err != nil {
		return github.CommentRepresentation{}, err
	}
	conditional, ok := any(b.Backend).(github.ConditionalCommentBackend)
	if !ok {
		return github.CommentRepresentation{}, github.ErrConditionalCommentMutationUnsupported
	}
	return conditional.UpdateCommentConditional(ctx, repo, commentID, version, body)
}

func validateFinalizationRepresentations(ctx context.Context, backend github.Backend, plan finalization.Plan,
	completed map[string]string) (map[int64]github.Comment, error) {
	current := map[int64]github.Comment{}
	byIssue := map[int][]finalization.Representation{}
	for _, representation := range plan.Representations {
		byIssue[representation.Issue] = append(byIssue[representation.Issue], representation)
	}
	changedTargets := completedFinalizationTargets(plan.Reconcile.Operations, completed)
	partialEndpointDigests := resumableFinalizationEndpointDigests(plan.Reconcile.Operations, completed)
	for issue, representations := range byIssue {
		comments, err := backend.ListIssueComments(ctx, plan.Repository, issue)
		if err != nil {
			return nil, err
		}
		listed := map[int64]github.Comment{}
		for _, comment := range comments {
			listed[comment.ID] = comment
		}
		for _, representation := range representations {
			comment, exists := listed[representation.CommentID]
			if !exists {
				return nil, fmt.Errorf("frozen comment %d is missing", representation.CommentID)
			}
			exact, version, err := observeExactFinalizationComment(ctx, backend, plan.Repository, comment)
			if err != nil {
				return nil, err
			}
			if exact.HTMLURL != representation.URL || (representation.APIURL != "" && exact.URL != representation.APIURL) {
				return nil, fmt.Errorf("frozen comment %d provider URL differs", representation.CommentID)
			}
			typed := model.ParseTypedComment(exact.Body)
			if typed.Type != representation.Type || typed.ID != representation.ID {
				return nil, fmt.Errorf("frozen comment %d typed identity differs", representation.CommentID)
			}
			logicalKey := fmt.Sprintf("%d/%s/%s", representation.Issue, representation.Type, representation.ID)
			if !changedTargets[logicalKey] {
				currentDigest := model.RepresentationDigest(exact.Body)
				_, resumableEndpoint := partialEndpointDigests[logicalKey][currentDigest]
				resumableEndpoint = resumableEndpoint && currentDigest != representation.RepresentationDigest
				if (currentDigest != representation.RepresentationDigest ||
					(representation.RepresentationVersion > 0 && version != representation.RepresentationVersion)) && !resumableEndpoint {
					return nil, fmt.Errorf("frozen comment %d representation differs", representation.CommentID)
				}
			}
			current[representation.CommentID] = exact
		}
	}
	if err := validateCompletedFinalizationPostconditions(plan.Reconcile.Operations, completed, current); err != nil {
		return nil, err
	}
	return current, nil
}

func resumableFinalizationEndpointDigests(operations []reconcile.Operation, completed map[string]string) map[string]map[string]struct{} {
	result := map[string]map[string]struct{}{}
	for _, operation := range operations {
		if operation.Kind != "link" || len(operation.Precondition.Endpoints) == 0 {
			continue
		}
		dependenciesComplete := true
		for _, dependency := range operation.DependsOn {
			if _, ok := completed[dependency]; !ok {
				dependenciesComplete = false
				break
			}
		}
		if !dependenciesComplete {
			continue
		}
		for _, endpoint := range operation.Precondition.Endpoints {
			key := fmt.Sprintf("%d/%s/%s", endpoint.Target.Issue, strings.ToUpper(endpoint.Target.Type), endpoint.Target.ID)
			if result[key] == nil {
				result[key] = map[string]struct{}{}
			}
			result[key][endpoint.AfterDigest] = struct{}{}
		}
	}
	return result
}

func completedFinalizationTargets(operations []reconcile.Operation, completed map[string]string) map[string]bool {
	result := map[string]bool{}
	mark := func(target reconcile.Target) {
		result[fmt.Sprintf("%d/%s/%s", target.Issue, strings.ToUpper(target.Type), target.ID)] = true
	}
	for _, operation := range operations {
		if _, done := completed[operation.ID]; !done {
			continue
		}
		if operation.Kind == "link" && len(operation.Precondition.Endpoints) != 0 {
			for _, endpoint := range operation.Precondition.Endpoints {
				mark(endpoint.Target)
			}
			continue
		}
		mark(operation.Target)
		if operation.Desired.Peer != nil {
			if operation.Desired.CarrierAuthorizedBacklink {
				delete(result, fmt.Sprintf("%d/%s/%s", operation.Target.Issue, strings.ToUpper(operation.Target.Type), operation.Target.ID))
			}
			mark(*operation.Desired.Peer)
		}
	}
	return result
}

func validateCompletedFinalizationPostconditions(operations []reconcile.Operation, completed map[string]string,
	current map[int64]github.Comment) error {
	if len(completed) == 0 {
		return nil
	}
	byTarget := map[string]github.Comment{}
	for _, comment := range current {
		typed := model.ParseTypedComment(comment.Body)
		byTarget[fmt.Sprintf("%d/%s/%s", comment.IssueNumber, typed.Type, typed.ID)] = comment
	}
	// IssueNumber is not populated by every backend's comment response. Add a
	// second identity keyed only by type/id, which is safe because the frozen
	// plan rejects duplicate typed identities.
	for _, comment := range current {
		typed := model.ParseTypedComment(comment.Body)
		byTarget[typed.Type+"/"+typed.ID] = comment
	}
	lookup := func(target reconcile.Target) (github.Comment, bool) {
		comment, ok := byTarget[fmt.Sprintf("%d/%s/%s", target.Issue, strings.ToUpper(target.Type), target.ID)]
		if !ok {
			comment, ok = byTarget[strings.ToUpper(target.Type)+"/"+target.ID]
		}
		return comment, ok
	}
	lastCompletedMutation := map[string]string{}
	resumableEndpointDigests := resumableFinalizationEndpointDigests(operations, completed)
	targetKey := func(target reconcile.Target) string {
		return fmt.Sprintf("%d/%s/%s", target.Issue, strings.ToUpper(target.Type), target.ID)
	}
	for _, operation := range operations {
		if _, done := completed[operation.ID]; !done {
			continue
		}
		if operation.Kind == "link" && len(operation.Precondition.Endpoints) != 0 {
			for _, endpoint := range operation.Precondition.Endpoints {
				lastCompletedMutation[targetKey(endpoint.Target)] = operation.ID
			}
			continue
		}
		lastCompletedMutation[targetKey(operation.Target)] = operation.ID
		if operation.Kind == "link" && operation.Desired.Peer != nil {
			if !operation.Desired.CarrierAuthorizedBacklink {
				lastCompletedMutation[targetKey(operation.Target)] = operation.ID
			}
			lastCompletedMutation[targetKey(*operation.Desired.Peer)] = operation.ID
		}
	}
	for _, operation := range operations {
		if _, done := completed[operation.ID]; !done {
			continue
		}
		target, exists := lookup(operation.Target)
		if !exists {
			return fmt.Errorf("checkpointed operation %s target is missing", operation.ID)
		}
		switch operation.Kind {
		case "upsert":
			desired := model.ParseTypedComment(operation.Desired.Body)
			if desired.Type != strings.ToUpper(operation.Target.Type) || desired.ID != operation.Target.ID {
				return fmt.Errorf("checkpointed operation %s desired identity is invalid", operation.ID)
			}
			_, found, err := model.ParseSupersededBy(desired.Body, desired.ID)
			if err != nil || !found {
				return fmt.Errorf("checkpointed operation %s desired authority is invalid", operation.ID)
			}
			if target.Body != operation.Desired.Body {
				return fmt.Errorf("checkpointed operation %s postcondition drifted", operation.ID)
			}
		case "transition":
			if model.ParseTypedComment(target.Body).Status != operation.Desired.Status {
				return fmt.Errorf("checkpointed operation %s lifecycle postcondition drifted", operation.ID)
			}
		case "link":
			peer, ok := lookup(*operation.Desired.Peer)
			if !ok {
				return fmt.Errorf("checkpointed operation %s peer is missing", operation.ID)
			}
			if operation.Desired.CarrierAuthorizedBacklink {
				expected := operation.Precondition.AcceptedReceipt
				observed, found, err := model.ObserveAcceptedReceiptAuthority(target.Body, expected.Role)
				if err != nil || !found || observed.ReceiptID != expected.ReceiptID || observed.Digest != expected.Digest ||
					observed.Generation != expected.Generation || model.ParseTypedComment(target.Body).Status != "done" {
					return fmt.Errorf("checkpointed operation %s carrier authority drifted", operation.ID)
				}
				if !finalizationCommentLinks(peer, target) {
					return fmt.Errorf("checkpointed operation %s backlink postcondition drifted", operation.ID)
				}
			} else if !finalizationCommentLinks(target, peer) || !finalizationCommentLinks(peer, target) {
				return fmt.Errorf("checkpointed operation %s reciprocal link postcondition drifted", operation.ID)
			}
			for _, endpoint := range operation.Precondition.Endpoints {
				if lastCompletedMutation[targetKey(endpoint.Target)] != operation.ID {
					continue
				}
				comment, ok := lookup(endpoint.Target)
				currentDigest := model.RepresentationDigest(comment.Body)
				_, plannedLaterState := resumableEndpointDigests[targetKey(endpoint.Target)][currentDigest]
				if !ok || (currentDigest != endpoint.AfterDigest && !plannedLaterState) {
					return fmt.Errorf("checkpointed operation %s endpoint representation drifted", operation.ID)
				}
			}
		}
	}
	return nil
}

func finalizationCommentLinks(from, to github.Comment) bool {
	typed := model.ParseTypedComment(from.Body)
	want := map[string]bool{model.NormalizeURL(to.HTMLURL): true, model.NormalizeURL(to.URL): true}
	for _, related := range model.RelatedCommentURLs(typed) {
		if want[model.NormalizeURL(related)] {
			return true
		}
	}
	return false
}

func runRemainingFinalizationOperations(ctx context.Context, backend reconcile.Backend, plan reconcile.Plan,
	checkpointPath string, checkpoint reconcile.Checkpoint) (reconcile.Result, error) {
	ordered, digest, err := reconcile.Validate(plan)
	result := reconcile.Result{PlanDigest: digest, Checkpoint: checkpointPath, Atomic: true}
	if err != nil {
		return result, err
	}
	for _, operation := range ordered {
		if status, done := checkpoint.Completed[operation.ID]; done {
			result.Operations = append(result.Operations, reconcile.OperationResult{ID: operation.ID, Kind: operation.Kind,
				Status: "unchanged", Atomic: !plan.AllowNonAtomic, Message: "checkpointed " + status + " postcondition confirmed"})
			result.Unchanged++
			continue
		}
		blocked := false
		for _, dependency := range operation.DependsOn {
			if _, complete := checkpoint.Completed[dependency]; !complete {
				blocked = true
			}
		}
		if blocked {
			result.Operations = append(result.Operations, reconcile.OperationResult{ID: operation.ID, Kind: operation.Kind,
				Status: "pending", Atomic: !plan.AllowNonAtomic, Message: "dependency is not complete"})
			result.Pending++
			continue
		}
		operation.DependsOn = nil
		single := plan
		single.PlanDigest = ""
		single.Operations = []reconcile.Operation{operation}
		_, singleDigest, err := reconcile.Validate(single)
		if err != nil {
			return result, err
		}
		single.PlanDigest = singleDigest
		applied, err := (reconcile.Engine{Backend: backend}).Run(ctx, single, "")
		if err != nil {
			return result, err
		}
		operationResult := applied.Operations[0]
		result.Operations = append(result.Operations, operationResult)
		result.Atomic = result.Atomic && operationResult.Atomic
		switch operationResult.Status {
		case "created":
			result.Created++
		case "updated":
			result.Updated++
		case "unchanged":
			result.Unchanged++
		case "conflicted":
			result.Conflicted++
		case "pending":
			result.Pending++
		}
		if operationResult.Status == "created" || operationResult.Status == "updated" || operationResult.Status == "unchanged" {
			checkpoint.Completed[operation.ID] = operationResult.Status
			if err := reconcile.SaveCheckpoint(checkpointPath, checkpoint); err != nil {
				return result, err
			}
		}
	}
	result.OK = result.Conflicted == 0 && result.Pending == 0
	return result, nil
}

func artifactsFromFinalizationObservations(observations []finalization.Observation) []model.Artifact {
	var artifacts []model.Artifact
	for _, observation := range observations {
		if !model.IsLikelyTyped(observation.Body) && !model.IsLikelyCodeChangeRationale(observation.Body) {
			continue
		}
		artifacts = append(artifacts, model.Artifact{Issue: observation.Issue, CommentID: observation.CommentID,
			URL: observation.URL, APIURL: observation.APIURL, Comment: model.ParseTypedComment(observation.Body)})
	}
	return artifacts
}
