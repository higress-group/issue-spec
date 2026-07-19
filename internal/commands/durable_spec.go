package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/durable"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/reconcile/filecas"
	"github.com/higress-group/issue-spec/internal/workflow"
)

var exactGitRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (a *app) runDurableSpec(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec durable-spec preview|apply|check|detail ...\n")
		return 2
	}
	switch args[0] {
	case "preview":
		return a.runDurableSpecPreview(ctx, args[1:])
	case "apply":
		return a.runDurableSpecApply(ctx, args[1:])
	case "check":
		return a.runDurableSpecCheck(ctx, args[1:])
	case "detail":
		return a.runDurableSpecDetail(args[1:])
	default:
		a.errorf("unknown durable-spec command %q\n", args[0])
		return 2
	}
}

func (a *app) runDurableSpecCheck(ctx context.Context, args []string) int {
	fs := newFlagSet("durable-spec check", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	proposalFlag := fs.String("proposal", "", "Proposal Issue number or URL")
	baselineFlag := fs.String("baseline", "", "exact 40-character baseline Git revision")
	subjectFlag := fs.String("subject", "", "exact 40-character subject Git revision")
	rootFlag := fs.String("root", ".", "repository worktree root")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	resultPathFlag := fs.String("result-out", "", "optional absolute saved result path outside the worktree")
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
	root, err := canonicalRepositoryRoot(*rootFlag)
	if err != nil {
		a.errorf("--root: %v\n", err)
		return 2
	}
	if strings.TrimSpace(*resultPathFlag) != "" {
		if err := validateDurableSidecarPath(*resultPathFlag, root); err != nil {
			a.errorf("--result-out: %v\n", err)
			return 2
		}
	}
	baseline, err := resolveExactGitRevision(ctx, root, *baselineFlag)
	if err != nil {
		a.errorf("--baseline: %v\n", err)
		return 2
	}
	subject, err := resolveExactGitRevision(ctx, root, *subjectFlag)
	if err != nil {
		a.errorf("--subject: %v\n", err)
		return 2
	}
	workflowAuthority, err := observeDurableWorkflow(root)
	if err != nil {
		a.errorf("observe durable workflow: %v\n", err)
		return 1
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("durable-spec check backend: %v\n", err)
		return 1
	}
	// Check path safety against the explicit baseline tree below, not against
	// incidental working-tree durable files. Canonical SPEC grammar and intent
	// remain strict while legacy existence is proven from baselineFiles.
	sources, err := observeDurableSources(ctx, client, repo, proposal, "")
	if err != nil {
		a.errorf("observe confirmed source SPECs: %v\n", err)
		return 1
	}
	baselineFiles, err := observeDurableBaselineFiles(ctx, root, baseline, sources)
	if err != nil {
		a.errorf("observe durable baseline: %v\n", err)
		return 1
	}
	subjectFiles, err := observeDurableBaselineFiles(ctx, root, subject, sources)
	if err != nil {
		a.errorf("observe durable subject: %v\n", err)
		return 1
	}
	changedPaths, err := changedDurablePaths(ctx, root, baseline, subject)
	if err != nil {
		a.errorf("observe durable tree changes: %v\n", err)
		return 1
	}
	revisionError, err := durableRevisionError(ctx, root, baseline, subject)
	if err != nil {
		a.errorf("compare durable revisions: %v\n", err)
		return 1
	}
	result, err := durable.Check(durable.CheckInput{CompileInput: durable.CompileInput{Repository: repo, Proposal: proposal,
		ProposalURL: proposalURLForSources(sources, *host, repo, proposal), BaselineRevision: baseline,
		Workflow: workflowAuthority, Sources: sources, BaselineFiles: baselineFiles}, SubjectRevision: subject,
		SubjectFiles: subjectFiles, ChangedDurablePaths: changedPaths, RevisionError: revisionError})
	if err != nil {
		a.errorf("check durable projection: %v\n", err)
		return 1
	}
	resultPath := strings.TrimSpace(*resultPathFlag)
	if resultPath == "" && !result.OK {
		resultPath, err = temporaryDurableCheckPath(root)
		if err != nil {
			a.errorf("allocate durable check result: %v\n", err)
			return 1
		}
	}
	if resultPath != "" {
		data, encodeErr := durable.CanonicalCheckResultJSON(result)
		if encodeErr != nil {
			a.errorf("encode durable check result: %v\n", encodeErr)
			return 1
		}
		if writeErr := writeDurableSidecar(resultPath, data); writeErr != nil {
			a.errorf("write durable check result: %v\n", writeErr)
			return 1
		}
	}
	compact := durable.CompactCheckResult(result, resultPath)
	if *jsonOut {
		if code := a.outputJSON(compact); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(a.out, "durable-spec check %s: operations=%d blockers=%d baseline=%s subject=%s\n",
			result.ResultDigest, result.OperationCount, len(result.Blockers), baseline, subject)
		for _, blocker := range compact.Blockers {
			fmt.Fprintf(a.out, "- %s affected=%s truncated=%d detail=%s\n", blocker.Code,
				strings.Join(blocker.AffectedIDs, ","), blocker.TruncatedCount, blocker.DetailAction)
		}
	}
	if !result.OK {
		return 1
	}
	return 0
}

func (a *app) runDurableSpecPreview(ctx context.Context, args []string) int {
	fs := newFlagSet("durable-spec preview", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	proposalFlag := fs.String("proposal", "", "Proposal Issue number or URL")
	baselineFlag := fs.String("baseline", "", "exact 40-character baseline Git revision")
	rootFlag := fs.String("root", ".", "repository worktree root")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	planPath := fs.String("plan-out", "", "absolute sidecar path outside the worktree")
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
	root, err := canonicalRepositoryRoot(*rootFlag)
	if err != nil {
		a.errorf("--root: %v\n", err)
		return 2
	}
	if err := validateDurableSidecarPath(*planPath, root); err != nil {
		a.errorf("--plan-out: %v\n", err)
		return 2
	}
	baseline, err := resolveExactGitRevision(ctx, root, *baselineFlag)
	if err != nil {
		a.errorf("--baseline: %v\n", err)
		return 2
	}
	workflowAuthority, err := observeDurableWorkflow(root)
	if err != nil {
		a.errorf("observe durable workflow: %v\n", err)
		return 1
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("durable-spec preview backend: %v\n", err)
		return 1
	}
	sources, err := observeDurableSources(ctx, client, repo, proposal, root)
	if err != nil {
		a.errorf("observe confirmed source SPECs: %v\n", err)
		return 1
	}
	baselineFiles, err := observeDurableBaselineFiles(ctx, root, baseline, sources)
	if err != nil {
		a.errorf("observe durable baseline: %v\n", err)
		return 1
	}
	plan, err := durable.CompilePlan(durable.CompileInput{Repository: repo, Proposal: proposal,
		ProposalURL: proposalURLForSources(sources, *host, repo, proposal), BaselineRevision: baseline,
		Workflow: workflowAuthority, Sources: sources, BaselineFiles: baselineFiles})
	if err != nil {
		a.errorf("compile durable plan: %v\n", err)
		return 1
	}
	data, err := durable.CanonicalPlanJSON(plan)
	if err != nil {
		a.errorf("encode durable plan: %v\n", err)
		return 1
	}
	if err := writeDurableSidecar(*planPath, data); err != nil {
		a.errorf("write durable plan: %v\n", err)
		return 1
	}
	compact := durable.Compact(plan, *planPath)
	if *jsonOut {
		return a.outputJSON(compact)
	}
	fmt.Fprintf(a.out, "durable-spec preview %s: operations=%d files=%d blockers=%d plan=%s\n",
		plan.PlanDigest, len(plan.Operations), len(plan.Files), len(plan.Blockers), *planPath)
	for _, blocker := range compact.Blockers {
		fmt.Fprintf(a.out, "- %s affected=%s truncated=%d detail=%s\n", blocker.Code,
			strings.Join(blocker.AffectedIDs, ","), blocker.TruncatedCount, blocker.DetailAction)
	}
	return 0
}

type durableDetailResult struct {
	Version          int                        `json:"version"`
	PlanDigest       string                     `json:"plan_digest"`
	Repository       string                     `json:"repository"`
	Proposal         int                        `json:"proposal"`
	BaselineRevision string                     `json:"baseline_revision"`
	Workflow         durable.WorkflowAuthority  `json:"workflow"`
	Sources          []durable.SourceAuthority  `json:"sources"`
	Operations       []durable.PlannedOperation `json:"operations"`
	Blockers         []durable.Blocker          `json:"blockers,omitempty"`
	Findings         []durable.Finding          `json:"findings,omitempty"`
}

type durableCheckDetailResult struct {
	Version          int               `json:"version"`
	ResultDigest     string            `json:"result_digest"`
	Repository       string            `json:"repository"`
	Proposal         int               `json:"proposal"`
	BaselineRevision string            `json:"baseline_revision"`
	SubjectRevision  string            `json:"subject_revision"`
	OperationCount   int               `json:"operation_count"`
	Blockers         []durable.Blocker `json:"blockers,omitempty"`
	Findings         []durable.Finding `json:"findings,omitempty"`
}

func (a *app) runDurableSpecDetail(args []string) int {
	fs := newFlagSet("durable-spec detail", a.err)
	planPath := fs.String("plan", "", "absolute frozen durable plan path")
	resultPath := fs.String("result", "", "absolute saved durable check result path")
	codeFlag := fs.String("code", "", "stable blocker code to expand")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if (*planPath == "") == (*resultPath == "") {
		a.errorf("exactly one of --plan or --result is required\n")
		return 2
	}
	if *resultPath != "" {
		return a.runDurableCheckDetail(*resultPath, strings.TrimSpace(*codeFlag), *jsonOut)
	}
	if !filepath.IsAbs(*planPath) {
		a.errorf("--plan: path must be absolute\n")
		return 2
	}
	plan, err := readDurablePlan(*planPath)
	if err != nil {
		a.errorf("read durable plan: %v\n", err)
		return 2
	}
	result := durableDetailResult{Version: plan.Version, PlanDigest: plan.PlanDigest, Repository: plan.Repository,
		Proposal: plan.Proposal, BaselineRevision: plan.BaselineRevision, Workflow: plan.Workflow,
		Sources: append([]durable.SourceAuthority(nil), plan.Sources...), Operations: append([]durable.PlannedOperation(nil), plan.Operations...)}
	wantCode := strings.TrimSpace(*codeFlag)
	for _, blocker := range plan.Blockers {
		if wantCode == "" || blocker.Code == wantCode {
			result.Blockers = append(result.Blockers, blocker)
		}
	}
	for _, finding := range plan.Findings {
		if wantCode == "" || finding.Code == wantCode {
			result.Findings = append(result.Findings, finding)
		}
	}
	if wantCode != "" && len(result.Blockers) == 0 {
		a.errorf("durable plan has no blocker code %q\n", wantCode)
		return 2
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "durable-spec plan %s repository=%s proposal=%d baseline=%s\n",
		plan.PlanDigest, plan.Repository, plan.Proposal, plan.BaselineRevision)
	for _, finding := range result.Findings {
		fmt.Fprintf(a.out, "- %s operation=%s source=%s path=%s: %s\n", finding.Code,
			finding.OperationID, finding.SourceSpecID, finding.Path, finding.Message)
	}
	return 0
}

func (a *app) runDurableCheckDetail(resultPath, wantCode string, jsonOut bool) int {
	if !filepath.IsAbs(resultPath) {
		a.errorf("--result: path must be absolute\n")
		return 2
	}
	result, err := readDurableCheckResult(resultPath)
	if err != nil {
		a.errorf("read durable check result: %v\n", err)
		return 2
	}
	detail := durableCheckDetailResult{Version: result.Version, ResultDigest: result.ResultDigest,
		Repository: result.Repository, Proposal: result.Proposal, BaselineRevision: result.BaselineRevision,
		SubjectRevision: result.SubjectRevision, OperationCount: result.OperationCount}
	for _, blocker := range result.Blockers {
		if wantCode == "" || blocker.Code == wantCode {
			detail.Blockers = append(detail.Blockers, blocker)
		}
	}
	for _, finding := range result.Findings {
		if wantCode == "" || finding.Code == wantCode {
			detail.Findings = append(detail.Findings, finding)
		}
	}
	if wantCode != "" && len(detail.Blockers) == 0 {
		a.errorf("durable check result has no blocker code %q\n", wantCode)
		return 2
	}
	if jsonOut {
		return a.outputJSON(detail)
	}
	fmt.Fprintf(a.out, "durable-spec check result %s repository=%s proposal=%d baseline=%s subject=%s\n",
		result.ResultDigest, result.Repository, result.Proposal, result.BaselineRevision, result.SubjectRevision)
	for _, finding := range detail.Findings {
		fmt.Fprintf(a.out, "- %s operation=%s source=%s path=%s: %s\n", finding.Code,
			finding.OperationID, finding.SourceSpecID, finding.Path, finding.Message)
	}
	return 0
}

func (a *app) runDurableSpecApply(ctx context.Context, args []string) int {
	fs := newFlagSet("durable-spec apply", a.err)
	planPath := fs.String("plan", "", "absolute frozen durable plan path")
	expectedDigest := fs.String("expected-plan-digest", "", "exact expected frozen plan digest")
	rootFlag := fs.String("root", ".", "repository worktree root")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	root, err := canonicalRepositoryRoot(*rootFlag)
	if err != nil {
		a.errorf("--root: %v\n", err)
		return 2
	}
	if err := validateDurableSidecarPath(*planPath, root); err != nil {
		a.errorf("--plan: %v\n", err)
		return 2
	}
	plan, err := readDurablePlan(*planPath)
	if err != nil {
		a.errorf("read durable plan: %v\n", err)
		return 2
	}
	workflowAuthority, err := observeDurableWorkflow(root)
	if err != nil {
		a.errorf("reobserve durable workflow: %v\n", err)
		return 1
	}
	baseline, err := resolveExactGitRevision(ctx, root, plan.BaselineRevision)
	if err != nil {
		a.errorf("reobserve durable baseline: %v\n", err)
		return 1
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("durable-spec apply backend: %v\n", err)
		return 1
	}
	sourceInputs, err := observeDurableSources(ctx, client, plan.Repository, plan.Proposal, root)
	if err != nil {
		a.errorf("reobserve confirmed source SPECs: %v\n", err)
		return 1
	}
	sources, err := exactSourceAuthorities(sourceInputs)
	if err != nil {
		a.errorf("reobserve confirmed source SPECs: %v\n", err)
		return 1
	}
	result, err := durable.ApplyPlan(root, plan, *expectedDigest, durable.AuthorityObservation{
		BaselineRevision: baseline, Workflow: workflowAuthority, Sources: sources})
	if err != nil {
		if *jsonOut {
			_ = a.outputJSON(map[string]any{"ok": false, "plan_digest": plan.PlanDigest, "error": err.Error(), "files": result.Files})
		} else {
			a.errorf("durable-spec apply: %v\n", err)
		}
		return 1
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "durable-spec apply %s: updated=%d unchanged=%d\n",
		plan.PlanDigest, result.Files.Updated, result.Files.Unchanged)
	return 0
}

func observeDurableSources(ctx context.Context, backend github.Backend, repo string, proposal int, root string) ([]durable.SourceInput, error) {
	comments, err := backend.ListIssueComments(ctx, repo, proposal)
	if err != nil {
		return nil, err
	}
	var result []durable.SourceInput
	for _, listed := range comments {
		parsed := model.ParseTypedComment(listed.Body)
		if parsed.Type != "SPEC" || parsed.Status != "confirmed" {
			continue
		}
		exact, version, err := observeExactFinalizationComment(ctx, backend, repo, listed)
		if err != nil {
			return nil, fmt.Errorf("observe SPEC comment %d: %w", listed.ID, err)
		}
		parsed = model.ParseTypedComment(exact.Body)
		if parsed.Type != "SPEC" || parsed.Status != "confirmed" {
			return nil, fmt.Errorf("SPEC comment %d changed identity after listing", listed.ID)
		}
		artifact := model.Artifact{Issue: proposal, CommentID: exact.ID, URL: exact.HTMLURL, APIURL: exact.URL, Comment: parsed}
		canonical := model.ValidateArtifactAtRoot(artifact, root)
		canonicalError := strings.Join(model.CanonicalDiagnosticStrings(canonical), "; ")
		intent, found, intentErr := model.ParseSpecDurableIntent(parsed.ID, exact.Body, root)
		item := durable.SourceInput{ID: parsed.ID, URL: exact.HTMLURL, RepresentationVersion: version,
			RepresentationDigest: model.RepresentationDigest(exact.Body), Body: model.LogicalBody(exact.Body),
			Intent: intent, IntentFound: found, CanonicalError: canonicalError}
		if intentErr != nil {
			item.IntentError = intentErr.Error()
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID+"\x00"+result[i].URL+"\x00"+result[i].RepresentationDigest <
			result[j].ID+"\x00"+result[j].URL+"\x00"+result[j].RepresentationDigest
	})
	return result, nil
}

func exactSourceAuthorities(inputs []durable.SourceInput) ([]durable.SourceAuthority, error) {
	result := make([]durable.SourceAuthority, 0, len(inputs))
	for _, source := range inputs {
		if source.CanonicalError != "" || source.IntentError != "" || !source.IntentFound {
			return nil, fmt.Errorf("source SPEC %s is no longer one canonical durable representation", source.ID)
		}
		requirement := ""
		for _, line := range strings.Split(source.Body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "## Requirement:") {
				requirement = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "## Requirement:"))
			}
		}
		intent, err := durable.NormalizeIntent(source.Intent, durable.ValidationOptions{SpecID: source.ID, SpecRequirement: requirement})
		if err != nil {
			return nil, fmt.Errorf("source SPEC %s durable intent: %w", source.ID, err)
		}
		result = append(result, durable.SourceAuthority{ID: source.ID, URL: source.URL,
			RepresentationVersion: source.RepresentationVersion, RepresentationDigest: source.RepresentationDigest, Intent: intent})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID+"\x00"+result[i].URL+"\x00"+result[i].RepresentationDigest <
			result[j].ID+"\x00"+result[j].URL+"\x00"+result[j].RepresentationDigest
	})
	return result, nil
}

func observeDurableWorkflow(root string) (durable.WorkflowAuthority, error) {
	resolved, err := workflow.Resolve(root)
	if err != nil {
		return durable.WorkflowAuthority{}, err
	}
	configPath := strings.TrimSpace(resolved.Source.ConfigPath)
	var data []byte
	if configPath == "" {
		configPath = "<builtin>"
		data = []byte("builtin:" + resolved.Source.SchemaName + "\n")
	} else {
		if !filepath.IsAbs(configPath) {
			configPath = filepath.Join(root, filepath.FromSlash(configPath))
		}
		absolute, err := filepath.Abs(configPath)
		if err != nil {
			return durable.WorkflowAuthority{}, err
		}
		resolvedPath, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return durable.WorkflowAuthority{}, err
		}
		if resolvedPath != absolute {
			return durable.WorkflowAuthority{}, errors.New("selected workflow config must not be a symlink")
		}
		relative, err := filepath.Rel(root, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return durable.WorkflowAuthority{}, errors.New("selected workflow config escapes repository root")
		}
		data, err = os.ReadFile(absolute)
		if err != nil {
			return durable.WorkflowAuthority{}, err
		}
		configPath = filepath.ToSlash(relative)
	}
	return durable.WorkflowAuthority{ConfigPath: configPath, ConfigDigest: filecas.FileDigest(data), Mode: resolved.DurableSpecsMode()}, nil
}

func observeDurableBaselineFiles(ctx context.Context, root, revision string, sources []durable.SourceInput) (map[string]durable.BaselineFile, error) {
	paths := map[string]bool{}
	for _, source := range sources {
		if source.IntentError != "" || !source.IntentFound {
			continue
		}
		for _, operation := range source.Intent.Operations {
			if strings.TrimSpace(operation.Path) != "" {
				paths[operation.Path] = true
			}
		}
	}
	ordered := make([]string, 0, len(paths))
	for targetPath := range paths {
		ordered = append(ordered, targetPath)
	}
	sort.Strings(ordered)
	result := map[string]durable.BaselineFile{}
	for _, targetPath := range ordered {
		command := exec.CommandContext(ctx, "git", "show", revision+":"+targetPath)
		command.Dir = root
		output, err := command.Output()
		if err == nil {
			result[targetPath] = durable.BaselineFile{Exists: true, Body: string(output)}
			continue
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result[targetPath] = durable.BaselineFile{}
			continue
		}
		return nil, fmt.Errorf("git show %s:%s: %w", revision, targetPath, err)
	}
	return result, nil
}

func resolveExactGitRevision(ctx context.Context, root, value string) (string, error) {
	value = strings.TrimSpace(value)
	if !exactGitRevision.MatchString(value) {
		return "", errors.New("exact lowercase 40-character Git revision is required")
	}
	command := exec.CommandContext(ctx, "git", "rev-parse", "--verify", value+"^{commit}")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolve exact Git revision: %w", err)
	}
	resolved := strings.ToLower(strings.TrimSpace(string(output)))
	if resolved != value {
		return "", fmt.Errorf("resolved Git revision %s differs from requested exact revision %s", resolved, value)
	}
	return resolved, nil
}

func canonicalRepositoryRoot(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = "."
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository root must be an existing directory")
	}
	return resolved, nil
}

func validateDurableSidecarPath(path, root string) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return errors.New("path must be absolute")
	}
	clean, err := resolveProspectivePath(filepath.Clean(path))
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, clean)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path must be outside the current worktree %s", root)
	}
	return nil
}

func resolveProspectivePath(value string) (string, error) {
	current := filepath.Dir(value)
	suffix := []string{filepath.Base(value)}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			parts := append([]string{resolved}, suffix...)
			return filepath.Join(parts...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
	}
}

func writeDurableSidecar(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".issue-spec-durable-plan-*")
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

func readDurablePlan(path string) (durable.Plan, error) {
	file, err := os.Open(path)
	if err != nil {
		return durable.Plan{}, err
	}
	defer file.Close()
	return durable.ReadPlan(file)
}

func readDurableCheckResult(path string) (durable.CheckResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return durable.CheckResult{}, err
	}
	defer file.Close()
	return durable.ReadCheckResult(file)
}

func changedDurablePaths(ctx context.Context, root, baseline, subject string) ([]string, error) {
	command := exec.CommandContext(ctx, "git", "diff", "--no-renames", "--name-only", "-z", baseline, subject, "--",
		"issue-spec/specs", "openspec/specs")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, value := range strings.Split(string(output), "\x00") {
		if value = strings.TrimSpace(value); value != "" {
			paths = append(paths, filepath.ToSlash(value))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func durableRevisionError(ctx context.Context, root, baseline, subject string) (string, error) {
	command := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", baseline, subject)
	command.Dir = root
	err := command.Run()
	if err == nil {
		return "", nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return fmt.Sprintf("baseline revision %s is not an ancestor of subject revision %s", baseline, subject), nil
	}
	return "", err
}

func temporaryDurableCheckPath(root string) (string, error) {
	seen := map[string]bool{}
	for _, directory := range []string{os.TempDir(), filepath.Dir(root)} {
		if directory == "" || seen[directory] {
			continue
		}
		seen[directory] = true
		file, err := os.CreateTemp(directory, "issue-spec-durable-check-*.json")
		if err != nil {
			continue
		}
		path := file.Name()
		if closeErr := file.Close(); closeErr != nil {
			_ = os.Remove(path)
			continue
		}
		_ = os.Remove(path)
		if err := validateDurableSidecarPath(path, root); err == nil {
			return path, nil
		}
	}
	return "", errors.New("no writable temporary result location exists outside the repository worktree")
}

func proposalURLForSources(sources []durable.SourceInput, hostname, repo string, proposal int) string {
	for _, source := range sources {
		if index := strings.Index(source.URL, "#"); index > 0 {
			return source.URL[:index]
		}
	}
	return "https://" + strings.TrimSpace(hostname) + "/" + repo + "/issues/" + fmt.Sprint(proposal)
}
