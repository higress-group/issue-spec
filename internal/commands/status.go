package commands

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/workflow"
)

type statusSummary struct {
	OK                bool                        `json:"ok"`
	Repo              string                      `json:"repo"`
	Issues            map[string]int              `json:"issues"`
	Counts            map[string]map[string]int   `json:"counts"`
	BlockingQuestions int                         `json:"blocking_questions"`
	OpenReviews       int                         `json:"open_reviews"`
	Verify            map[string]string           `json:"verify"`
	Traceability      model.VerifyReport          `json:"traceability"`
	Diagnostics       []metadataDiagnostic        `json:"diagnostics,omitempty"`
	Malformed         []model.CanonicalDiagnostic `json:"malformed,omitempty"`
	Workflow          *workflow.Plan              `json:"workflow,omitempty"`
	NextGates         []string                    `json:"next_gates"`
	Gate              gates.Report                `json:"gate"`
}

func (a *app) runStatus(ctx context.Context, args []string) int {
	fs := newFlagSet("status", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	proposalFlag := fs.String("proposal", "", "proposal issue number or URL")
	designFlag := fs.String("design", "", "design issue number or URL")
	implementFlag := fs.String("implement", "", "implement issue number or URL")
	gateFlag := fs.String("gate", "", "readiness gate: proposal, design, implement, final, or archive (default inferred from supplied issues)")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	proposalIssue, err := parseIssueFlag(*proposalFlag, "proposal")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	designIssue, err := optionalIssue(*designFlag)
	if err != nil {
		a.errorf("--design: %v\n", err)
		return 2
	}
	implementIssue, err := optionalIssue(*implementFlag)
	if err != nil {
		a.errorf("--implement: %v\n", err)
		return 2
	}
	target, err := resolveStatusGate(*gateFlag, designIssue, implementIssue)
	if err != nil {
		a.errorf("--gate: %v\n", err)
		return 2
	}
	client, token, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for status on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	artifacts, err := collectArtifacts(ctx, client, repo, proposalIssue, designIssue, implementIssue)
	if err != nil {
		a.errorf("collect artifacts: %v\n", err)
		return 1
	}
	workflowPlan, workflowErr := workflow.Resolve(".")
	profile, _, profileErr := auth.ResolveProfile(a.profileName, *host)
	collection := statusGateCollection{Remote: statusForecastRemoteFacts(target)}
	if profileErr == nil {
		collection = a.collectStatusGateFacts(ctx, client, profile, token.Value, repo, implementIssue, target, artifacts)
	}
	summary := summarizeStatusForGate(*repoFlag, proposalIssue, designIssue, implementIssue, target, artifacts, workflowPlan, workflowErr, collection)
	if proposalIssueData, perr := client.GetIssue(ctx, repo, proposalIssue); perr == nil {
		summary.Diagnostics = append(summary.Diagnostics, authoringCompletenessDiagnostics("proposal", proposalIssueData.HTMLURL, proposalIssueData.Body)...)
	}
	if designIssue > 0 {
		if designIssueData, derr := client.GetIssue(ctx, repo, designIssue); derr == nil {
			summary.Diagnostics = append(summary.Diagnostics, authoringCompletenessDiagnostics("design", designIssueData.HTMLURL, designIssueData.Body)...)
		}
	}
	if *jsonOut {
		if code := a.outputJSON(summary); code != 0 {
			return code
		}
		if !summary.OK {
			return 1
		}
		return 0
	}
	printStatus(a.out, summary)
	if !summary.OK {
		return 1
	}
	return 0
}

func (a *app) runVerifyLinks(ctx context.Context, args []string) int {
	fs := newFlagSet("verify-links", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	proposalFlag := fs.String("proposal", "", "proposal issue number or URL")
	designFlag := fs.String("design", "", "design issue number or URL")
	implementFlag := fs.String("implement", "", "implement issue number or URL")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	proposalIssue, err := parseIssueFlag(*proposalFlag, "proposal")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	designIssue, err := parseIssueFlag(*designFlag, "design")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	implementIssue, err := parseIssueFlag(*implementFlag, "implement")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for verify-links on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	artifacts, err := collectArtifacts(ctx, client, repo, proposalIssue, designIssue, implementIssue)
	if err != nil {
		a.errorf("collect artifacts: %v\n", err)
		return 1
	}
	report := model.VerifyTraceability(artifacts)
	if *jsonOut {
		if code := a.outputJSON(report); code != 0 {
			return code
		}
		if !report.OK {
			return 1
		}
		return 0
	}
	if report.OK {
		fmt.Fprintln(a.out, "traceability OK")
		return 0
	}
	fmt.Fprintln(a.out, "traceability errors:")
	for _, msg := range report.Errors {
		fmt.Fprintf(a.out, "- %s\n", msg)
	}
	return 1
}

func summarizeStatus(repo string, proposal, design, implement int, artifacts []model.Artifact, workflowState ...any) statusSummary {
	target, _ := resolveStatusGate("", design, implement)
	return summarizeStatusForGate(repo, proposal, design, implement, target, artifacts, workflowState...)
}

func summarizeStatusForGate(repo string, proposal, design, implement int, target gates.Target, artifacts []model.Artifact, workflowState ...any) statusSummary {
	var workflowPlan workflow.Plan
	var workflowErr error
	collection := statusGateCollection{Remote: statusForecastRemoteFacts(target)}
	for _, value := range workflowState {
		if plan, ok := value.(workflow.Plan); ok {
			workflowPlan = plan
		}
		if err, ok := value.(error); ok {
			workflowErr = err
		}
		if collected, ok := value.(statusGateCollection); ok {
			collection = collected
		}
	}
	counts := map[string]map[string]int{}
	verify := map[string]string{}
	blockingQuestions := 0
	openReviews := 0
	var malformed []model.CanonicalDiagnostic
	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.Type == "" {
			continue
		}
		if tc.Status != "superseded" {
			malformed = append(malformed, model.ValidateArtifact(artifact)...)
		}
		if counts[tc.Type] == nil {
			counts[tc.Type] = map[string]int{}
		}
		status := tc.Status
		if status == "" {
			status = "unknown"
		}
		counts[tc.Type][status]++
		if tc.Type == "QUESTION" && tc.Status == "blocked" {
			blockingQuestions++
		}
		if tc.Type == "REVIEW" && tc.Status != "done" && tc.Status != "superseded" {
			openReviews++
		}
		if tc.Type == "VERIFY" {
			verify[tc.ID] = tc.Status
		}
	}
	report := model.VerifyTraceability(artifacts)
	diagnostics := typedSessionDiagnostics(artifacts)
	workflowFacts := gates.WorkflowFacts{Required: true, Known: true, Valid: workflowErr == nil && !workflowPlan.HasErrors()}
	if workflowErr != nil {
		workflowFacts.Errors = append(workflowFacts.Errors, workflowErr.Error())
	}
	for _, diagnostic := range workflowPlan.Diagnostics {
		if diagnostic.Severity == "error" {
			workflowFacts.Errors = append(workflowFacts.Errors, diagnostic.Code+": "+diagnostic.Message)
		}
	}
	mode := gates.ModeForecast
	if target == gates.TargetFinal || target == gates.TargetArchive {
		mode = gates.ModeAuthoritative
	}
	snapshot := gates.Snapshot{
		Target:          target,
		Mode:            mode,
		Artifacts:       artifacts,
		Canonical:       gates.CanonicalFacts{Observed: true, Diagnostics: malformed},
		Traceability:    gates.TraceabilityFacts{Observed: true, Report: report},
		Workflow:        workflowFacts,
		Remote:          collection.Remote,
		ProcessEvidence: collection.ProcessEvidence,
	}
	gateReport, gateErr := gates.Evaluate(snapshot)
	if gateErr != nil {
		gateReport = gates.Report{Ready: false, Target: target, Mode: mode, PointInTime: mode == gates.ModeForecast,
			Diagnostics: []gates.Diagnostic{{Code: "gate.evaluation_failed", Gate: target, Severity: gates.SeverityError,
				Blocking: true, Message: gateErr.Error(), Current: "invalid", Expected: "valid gate snapshot",
				Remediation: gates.Remediation{CommandFamily: "status"}, Freshness: gates.FreshnessLocal}}}
	}
	nextGates := legacyStatusGateMessages(gateReport.Diagnostics)
	var workflowSummary *workflow.Plan
	if workflowPlan.Source.SchemaName != "" || len(workflowPlan.Diagnostics) > 0 {
		workflowSummary = &workflowPlan
	}
	return statusSummary{
		OK:                gateReport.Ready,
		Repo:              repo,
		Issues:            map[string]int{"proposal": proposal, "design": design, "implement": implement},
		Counts:            counts,
		BlockingQuestions: blockingQuestions,
		OpenReviews:       openReviews,
		Verify:            verify,
		Traceability:      report,
		Diagnostics:       diagnostics,
		Malformed:         malformed,
		Workflow:          workflowSummary,
		NextGates:         nextGates,
		Gate:              gateReport,
	}
}

func resolveStatusGate(raw string, design, implement int) (gates.Target, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		switch {
		case implement > 0:
			return gates.TargetImplement, nil
		case design > 0:
			return gates.TargetDesign, nil
		default:
			return gates.TargetProposal, nil
		}
	}
	target := gates.Target(value)
	switch target {
	case gates.TargetProposal:
		return target, nil
	case gates.TargetDesign:
		if design == 0 {
			return "", fmt.Errorf("design gate requires --design")
		}
		return target, nil
	case gates.TargetImplement, gates.TargetFinal, gates.TargetArchive:
		if design == 0 || implement == 0 {
			return "", fmt.Errorf("%s gate requires --design and --implement", target)
		}
		return target, nil
	default:
		return "", fmt.Errorf("unsupported value %q (want proposal, design, implement, final, or archive)", raw)
	}
}

func statusForecastRemoteFacts(target gates.Target) gates.RemoteFacts {
	facts := gates.RemoteFacts{Workspace: gates.WorkspaceFacts{Observed: true}}
	if target == gates.TargetFinal || target == gates.TargetArchive {
		facts.PRChecks = gates.Fact{Required: true, Expected: "all required checks passed"}
		facts.ReviewFindings = gates.Fact{Required: true, Expected: "no blocking findings"}
		facts.Workspace.ExpectedRevision = gates.Fact{Required: true, Expected: "exact PR or external subject revision"}
	}
	if target == gates.TargetArchive {
		facts.DurableSpec = gates.Fact{Required: true, Expected: "durable spec valid"}
	}
	return facts
}

type statusGateCollection struct {
	Remote          gates.RemoteFacts
	ProcessEvidence []gates.ProcessEvidenceInput
}

func (a *app) collectStatusGateFacts(ctx context.Context, client github.Backend, profile auth.Profile, token, repo string,
	implementIssue int, target gates.Target, artifacts []model.Artifact) statusGateCollection {
	collection := statusGateCollection{Remote: statusForecastRemoteFacts(target)}
	if target != gates.TargetFinal && target != gates.TargetArchive {
		return collection
	}
	if profile.Kind == auth.ProfileKindHosted {
		collection.Remote.PRChecks = gates.Fact{}
		collection.Remote.ReviewFindings = gates.Fact{}
		gate, _, gateErr := a.externalGateWithProfile(ctx, profile, token, repo, implementIssue, "code_change", "",
			coreevidence.GateVerify, ".", "status")
		if revision := strings.TrimSpace(gate.Target.SubjectRevision); revision != "" {
			collection.Remote.Workspace.ExpectedRevision = gates.Fact{Required: true, Known: true, Passed: true,
				Expected: revision, Current: revision}
		} else if a.newNativeEvidenceProvider != nil {
			if native, nativeErr := a.newNativeEvidenceProvider(profile, token); nativeErr == nil {
				if target, targetErr := native.ResolveTarget(ctx, repo, implementIssue, "code_change"); targetErr == nil && strings.TrimSpace(target.SubjectRevision) != "" {
					collection.Remote.Workspace.ExpectedRevision = gates.Fact{Required: true, Known: true, Passed: true,
						Expected: strings.TrimSpace(target.SubjectRevision), Current: strings.TrimSpace(target.SubjectRevision)}
				}
			}
		}
		collection.Remote.ProviderEvidence = gates.Fact{Required: true, Known: true, Passed: gateErr == nil,
			Expected: "trusted exact-revision provider evidence"}
		if gateErr != nil {
			collection.Remote.ProviderEvidence.Current = "unavailable"
			return collection
		}
		collection.ProcessEvidence = buildProcessEvidenceInputs(artifacts, "", nil, reviewSyncReport{}, &gate.Consumption)
		return collection
	}

	prNumber, prURL, ok := exactStatusPullRequest(artifacts, repo)
	if !ok {
		return collection
	}
	pr, err := client.GetPullRequest(ctx, repo, prNumber)
	if err != nil || strings.TrimSpace(pr.Head.SHA) == "" {
		return collection
	}
	if strings.TrimSpace(pr.HTMLURL) != "" {
		prURL = pr.HTMLURL
	}
	reviewComments, commentsErr := client.ListPullRequestReviewComments(ctx, repo, prNumber)
	combined, statusErr := client.GetCombinedStatus(ctx, repo, pr.Head.SHA)
	checks, checksErr := client.ListCheckRuns(ctx, repo, pr.Head.SHA)
	collection.Remote.Workspace.ExpectedRevision = gates.Fact{Required: true, Known: true, Passed: true,
		Expected: pr.Head.SHA, Current: pr.Head.SHA}
	if commentsErr != nil || statusErr != nil || checksErr != nil {
		return collection
	}
	review := buildReviewSyncReport(pr, reviewComments, nil, combined, checks)
	collection.Remote.PRChecks = gates.Fact{Required: true, Known: true,
		Passed:  len(review.FailedChecks) == 0 && len(review.PendingChecks) == 0,
		Current: fmt.Sprintf("failed=%d pending=%d", len(review.FailedChecks), len(review.PendingChecks)), Expected: "failed=0 pending=0"}
	collection.Remote.ReviewFindings = gates.Fact{Required: true, Known: true, Passed: len(review.BlockingFindings) == 0,
		Current: fmt.Sprintf("blocking=%d", len(review.BlockingFindings)), Expected: "blocking=0"}
	collection.ProcessEvidence = buildProcessEvidenceInputs(artifacts, prURL, reviewComments, review, nil)
	return collection
}

func exactStatusPullRequest(artifacts []model.Artifact, repo string) (int, string, bool) {
	seen := map[string]bool{}
	var selected string
	for _, artifact := range artifacts {
		if artifact.Comment.Type != "PROCESS" || artifact.Comment.Status == "superseded" {
			continue
		}
		for _, raw := range artifact.Comment.Links["PR"] {
			normalized := model.NormalizeURL(raw)
			if normalized != "" {
				seen[normalized] = true
				selected = normalized
			}
		}
	}
	if len(seen) != 1 {
		return 0, "", false
	}
	parsed, err := url.Parse(selected)
	if err != nil {
		return 0, "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	repoParts := strings.Split(strings.Trim(repo, "/"), "/")
	if len(parts) != 4 || len(repoParts) != 2 || parts[0] != repoParts[0] || parts[1] != repoParts[1] || parts[2] != "pull" {
		return 0, "", false
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return 0, "", false
	}
	return number, selected, true
}

func legacyStatusGateMessages(diagnostics []gates.Diagnostic) []string {
	seen := map[string]bool{}
	var messages []string
	for _, diagnostic := range diagnostics {
		if !diagnostic.Blocking {
			continue
		}
		message := diagnostic.Message
		switch diagnostic.Code {
		case gates.CodeArtifactNoncanonical:
			message = "malformed typed comments must be regenerated, migrated, or superseded"
		case gates.CodeTraceabilityInvalid:
			message = "traceability errors must be fixed"
		case gates.CodeWorkflowInvalid, gates.CodeWorkflowUnknown:
			message = "workflow config/schema diagnostics must be fixed"
		}
		if !seen[message] {
			seen[message] = true
			messages = append(messages, message)
		}
	}
	sort.Strings(messages)
	return messages
}

func optionalIssue(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	return issueNumberFlag(value)
}

func printStatus(out interface{ Write([]byte) (int, error) }, summary statusSummary) {
	fmt.Fprintf(out, "repo: %s\n", summary.Repo)
	fmt.Fprintf(out, "issues: proposal #%d", summary.Issues["proposal"])
	if summary.Issues["design"] != 0 {
		fmt.Fprintf(out, ", design #%d", summary.Issues["design"])
	}
	if summary.Issues["implement"] != 0 {
		fmt.Fprintf(out, ", implement #%d", summary.Issues["implement"])
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "gate: %s mode=%s ready=%v point-in-time=%v\n", summary.Gate.Target, summary.Gate.Mode, summary.Gate.Ready, summary.Gate.PointInTime)
	for _, typ := range sortedTypes(summary.Counts) {
		fmt.Fprintf(out, "%s: %s\n", typ, formatStatusCounts(summary.Counts[typ]))
	}
	if summary.Traceability.OK {
		fmt.Fprintln(out, "traceability: OK")
	} else {
		fmt.Fprintf(out, "traceability: %d error(s)\n", len(summary.Traceability.Errors))
	}
	if len(summary.Diagnostics) > 0 {
		fmt.Fprintln(out, "metadata diagnostics:")
		for _, diagnostic := range summary.Diagnostics {
			fmt.Fprintf(out, "- %s %s: %s\n", diagnostic.Level, diagnostic.Code, diagnostic.Message)
		}
	}
	if len(summary.Malformed) > 0 {
		fmt.Fprintf(out, "malformed typed comments: %d\n", len(summary.Malformed))
		for _, d := range summary.Malformed {
			url := d.URL
			if url == "" {
				url = "N/A"
			}
			fmt.Fprintf(out, "- %s %s (%s): %s\n", d.Type, d.ID, url, d.Message)
		}
	}
	if summary.Workflow != nil {
		fmt.Fprintf(out, "workflow: %s schema=%s\n", summary.Workflow.Source.Kind, summary.Workflow.Source.SchemaName)
		for _, diagnostic := range summary.Workflow.Diagnostics {
			if diagnostic.Severity == "info" {
				continue
			}
			fmt.Fprintf(out, "- workflow %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	}
	if len(summary.NextGates) > 0 {
		fmt.Fprintln(out, "blocking gates:")
		for _, gate := range summary.NextGates {
			fmt.Fprintf(out, "- %s\n", gate)
		}
	} else {
		fmt.Fprintln(out, "blocking gates: none")
	}
	if len(summary.Gate.Diagnostics) > 0 {
		fmt.Fprintln(out, "gate diagnostics:")
		for _, diagnostic := range summary.Gate.Diagnostics {
			identity := diagnostic.Artifact.ID
			if identity == "" {
				identity = "change"
			}
			fmt.Fprintf(out, "- %s %s severity=%s blocking=%v freshness=%s current=%s expected=%s: %s\n",
				diagnostic.Code, identity, diagnostic.Severity, diagnostic.Blocking, diagnostic.Freshness, diagnostic.Current, diagnostic.Expected, diagnostic.Message)
		}
	}
}

func sortedTypes(counts map[string]map[string]int) []string {
	types := make([]string, 0, len(counts))
	for typ := range counts {
		types = append(types, typ)
	}
	sort.Strings(types)
	return types
}

func formatStatusCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ", ")
}
