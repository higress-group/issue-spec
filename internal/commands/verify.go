package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

type finalVerifyReport struct {
	OK                    bool                          `json:"ok"`
	Traceability          model.VerifyReport            `json:"traceability"`
	Errors                []string                      `json:"errors"`
	Warnings              []string                      `json:"warnings,omitempty"`
	Diagnostics           []metadataDiagnostic          `json:"diagnostics,omitempty"`
	SpecCoverage          map[string]bool               `json:"spec_coverage"`
	RationaleCoverage     map[string]bool               `json:"rationale_coverage,omitempty"`
	Noncanonical          []model.CanonicalDiagnostic   `json:"noncanonical,omitempty"`
	ReviewFindingBlockers []reviewFinding               `json:"review_finding_blockers,omitempty"`
	FailedChecks          []reviewCheck                 `json:"failed_checks,omitempty"`
	PendingChecks         []reviewCheck                 `json:"pending_checks,omitempty"`
	PR                    int                           `json:"pr,omitempty"`
	DurableSpecPath       string                        `json:"durable_spec_path,omitempty"`
	DurableSpecCheck      map[string]bool               `json:"durable_spec_check,omitempty"`
	ExternalEvidence      *externalEvidenceConsumption  `json:"external_evidence,omitempty"`
	Gate                  gates.Report                  `json:"gate"`
	ProcessEvidence       []gates.ProcessEvidenceReport `json:"process_evidence,omitempty"`
}

type finalVerifyOptions struct {
	DurableSpecPath   string
	PR                int
	PRURL             string
	ExpectedRevision  string
	RationaleRequired bool
	RationaleComments []github.PullRequestReviewComment
	PRStatus          github.CombinedStatus
	PRCheckRuns       []github.CheckRun
	PRCommits         []github.PullRequestCommit
	ExternalEvidence  *externalEvidenceConsumption
	CarrierRevisions  map[string]gates.CarrierRevisionFact
}

func (a *app) runVerify(ctx context.Context, args []string) int {
	fs := newFlagSet("verify", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	proposalFlag := fs.String("proposal", "", "proposal issue number or URL")
	designFlag := fs.String("design", "", "design issue number or URL")
	implementFlag := fs.String("implement", "", "implement issue number or URL")
	prFlag := fs.Int("pr", 0, "pull request number for rationale-comment verification")
	revision := fs.String("revision", "", "expected external code head revision for self-hosted evidence")
	durableSpec := fs.String("durable-spec", "", "durable spec file to verify")
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
	client, token, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for verify on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	proposalIssueData, err := client.GetIssue(ctx, repo, proposalIssue)
	if err != nil {
		a.errorf("read proposal issue #%d: %v\n", proposalIssue, err)
		return 1
	}
	artifacts, err := collectArtifacts(ctx, client, repo, proposalIssue, designIssue, implementIssue)
	if err != nil {
		a.errorf("collect artifacts: %v\n", err)
		return 1
	}
	var rationaleComments []github.PullRequestReviewComment
	var prStatus github.CombinedStatus
	var prCheckRuns []github.CheckRun
	var prCommits []github.PullRequestCommit
	var prURL string
	var expectedRevision string
	externalGate, selfHosted, err := a.externalGate(ctx, *host, token.Value, repo, implementIssue,
		"code_change", *revision, coreevidence.GateVerify)
	if err != nil {
		a.errorf("verify external evidence: %v\n", err)
		return 1
	}
	if selfHosted && *prFlag > 0 {
		a.errorf("--pr is not a self-hosted code authority; omit it and use the active code_change reference\n")
		return 2
	}
	if !selfHosted && *prFlag > 0 {
		facts, err := collectPullRequestGateFacts(ctx, client, repo, *prFlag)
		if err != nil {
			a.errorf("read stable PR #%d gate facts: %v\n", *prFlag, err)
			return 1
		}
		pr := facts.PullRequest
		prURL = pr.HTMLURL
		expectedRevision = pr.Head.SHA
		rationaleComments = facts.ReviewComments
		prStatus = facts.Status
		prCheckRuns = facts.CheckRuns
		prCommits = facts.Commits
	}
	var processExternalEvidence *externalEvidenceConsumption
	if selfHosted {
		processExternalEvidence = &externalGate.Consumption
		expectedRevision = externalGate.Target.SubjectRevision
	}
	report, err := buildFinalVerifyReport(artifacts, proposalIssueData.HTMLURL, finalVerifyOptions{
		DurableSpecPath:   *durableSpec,
		PR:                *prFlag,
		PRURL:             prURL,
		ExpectedRevision:  expectedRevision,
		RationaleRequired: *prFlag > 0,
		RationaleComments: rationaleComments,
		PRStatus:          prStatus,
		PRCheckRuns:       prCheckRuns,
		PRCommits:         prCommits,
		ExternalEvidence:  processExternalEvidence,
	})
	if err != nil {
		a.errorf("verify: %v\n", err)
		return 1
	}
	var finalVerify *model.Artifact
	if selfHosted {
		candidate, revisionErr := exactRevisionBoundVerify(artifacts, externalGate.Target.SubjectRevision)
		if revisionErr != nil {
			report.Errors = append(report.Errors, revisionErr.Error())
		} else {
			finalVerify = candidate
			report.ExternalEvidence = &externalGate.Consumption
		}
		report.OK = len(report.Errors) == 0
	}
	report.Diagnostics = append(report.Diagnostics, authoringCompletenessDiagnostics("proposal", proposalIssueData.HTMLURL, proposalIssueData.Body)...)
	if designIssue > 0 {
		if designIssueData, derr := client.GetIssue(ctx, repo, designIssue); derr == nil {
			report.Diagnostics = append(report.Diagnostics, authoringCompletenessDiagnostics("design", designIssueData.HTMLURL, designIssueData.Body)...)
		}
	}
	if selfHosted && report.OK && finalVerify != nil {
		updated, changed, stampErr := stampConsumedEvidence(finalVerify.Comment.Body, externalGate.Consumption)
		if stampErr != nil {
			a.errorf("record consumed external evidence: %v\n", stampErr)
			return 1
		}
		if changed {
			if _, updateErr := client.UpdateComment(ctx, repo, finalVerify.CommentID, updated); updateErr != nil {
				a.errorf("record consumed external evidence on %s: %v\n", finalVerify.Comment.ID, updateErr)
				return 1
			}
		}
	}
	if *jsonOut {
		if code := a.outputJSON(report); code != 0 {
			return code
		}
		if !report.OK {
			return 1
		}
		return 0
	}
	printFinalVerify(a.out, report)
	if !report.OK {
		return 1
	}
	return 0
}

const (
	consumedEvidenceStart = "<!-- issue-spec:consumed-evidence version=1 -->"
	consumedEvidenceEnd   = "<!-- /issue-spec:consumed-evidence -->"
)

func exactRevisionBoundVerify(artifacts []model.Artifact, revision string) (*model.Artifact, error) {
	var candidates []*model.Artifact
	for index := range artifacts {
		if artifacts[index].Comment.Type == "VERIFY" && artifacts[index].Comment.Status == "done" {
			candidates = append(candidates, &artifacts[index])
		}
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("self-hosted verify requires exactly one active done VERIFY (found %d)", len(candidates))
	}
	raw := strings.TrimSpace(sectionContent(candidates[0].Comment.Body, "### Revision"))
	raw = strings.Trim(raw, "`")
	if fields := strings.Fields(raw); len(fields) != 1 || fields[0] != revision {
		return nil, fmt.Errorf("%s must contain `### Revision` with exact external head revision %s", candidates[0].Comment.ID, revision)
	}
	return candidates[0], nil
}

func stampConsumedEvidence(body string, consumption externalEvidenceConsumption) (string, bool, error) {
	consumption.EvidenceIDs = append([]string(nil), consumption.EvidenceIDs...)
	sort.Strings(consumption.EvidenceIDs)
	consumption.Bindings = normalizeExternalEvidenceBindings(append([]externalEvidenceBinding(nil), consumption.Bindings...))
	if consumption.ProviderKey == "" || consumption.ExternalRepository == "" || consumption.ChangeID == "" ||
		consumption.SubjectRevision == "" || len(consumption.EvidenceIDs) == 0 || len(consumption.Bindings) == 0 {
		return "", false, errors.New("consumed evidence identity is incomplete")
	}
	raw, err := json.Marshal(consumption)
	if err != nil {
		return "", false, err
	}
	block := consumedEvidenceStart + "\n### Consumed External Evidence\n\n```json\n" + string(raw) + "\n```\n" + consumedEvidenceEnd
	startCount, endCount := strings.Count(body, consumedEvidenceStart), strings.Count(body, consumedEvidenceEnd)
	if startCount != endCount || startCount > 1 {
		return "", false, errors.New("existing consumed evidence block is malformed")
	}
	start, end := strings.Index(body, consumedEvidenceStart), strings.Index(body, consumedEvidenceEnd)
	if startCount == 1 && end < start+len(consumedEvidenceStart) {
		return "", false, errors.New("existing consumed evidence block is malformed")
	}
	updated := body
	if start >= 0 {
		end += len(consumedEvidenceEnd)
		updated = body[:start] + block + body[end:]
	} else {
		updated = strings.TrimRight(body, "\n") + "\n\n" + block + "\n"
	}
	return updated, updated != body, nil
}

func buildFinalVerifyReport(artifacts []model.Artifact, proposalURL string, opts finalVerifyOptions) (finalVerifyReport, error) {
	traceability := model.VerifyTraceability(artifacts)
	report := finalVerifyReport{
		Traceability:      traceability,
		SpecCoverage:      map[string]bool{},
		RationaleCoverage: map[string]bool{},
		PR:                opts.PR,
	}
	report.Diagnostics = append(report.Diagnostics, typedSessionDiagnostics(artifacts)...)
	var activeSpecs []model.Artifact
	var activeProcesses []model.Artifact
	var doneVerifyBodies []string
	var canonical []model.CanonicalDiagnostic
	for _, artifact := range artifacts {
		tc := artifact.Comment
		switch tc.Type {
		case "SPEC":
			if tc.Status != "superseded" {
				activeSpecs = append(activeSpecs, artifact)
				report.SpecCoverage[tc.ID] = false
			}
		case "PROCESS":
			if tc.Status != "superseded" {
				activeProcesses = append(activeProcesses, artifact)
			}
		case "VERIFY":
			if tc.Status == "done" {
				doneVerifyBodies = append(doneVerifyBodies, tc.Body)
			}
		}
		if artifact.Comment.Status == "superseded" {
			continue
		}
		diags := model.ValidateArtifact(artifact)
		canonical = append(canonical, diags...)
		report.Noncanonical = append(report.Noncanonical, diags...)
	}
	verifyText := strings.Join(doneVerifyBodies, "\n")
	for _, spec := range activeSpecs {
		if strings.Contains(verifyText, spec.Comment.ID) {
			report.SpecCoverage[spec.Comment.ID] = true
		}
	}

	var reviewReport reviewSyncReport
	remote := gates.RemoteFacts{}
	if opts.RationaleRequired {
		reviewReport = buildReviewSyncReport(github.PullRequest{Number: opts.PR, HTMLURL: opts.PRURL}, opts.RationaleComments, nil, opts.PRStatus, opts.PRCheckRuns)
		remote.PRChecks = gates.Fact{Required: true, Known: true,
			Passed:   len(reviewReport.FailedChecks) == 0 && len(reviewReport.PendingChecks) == 0,
			Current:  fmt.Sprintf("failed=%d pending=%d", len(reviewReport.FailedChecks), len(reviewReport.PendingChecks)),
			Expected: "failed=0 pending=0"}
		remote.ReviewFindings = gates.Fact{Required: true, Known: true,
			Passed:  len(reviewReport.BlockingFindings) == 0,
			Current: fmt.Sprintf("blocking=%d", len(reviewReport.BlockingFindings)), Expected: "blocking=0"}
	}

	target := gates.TargetFinal
	if strings.TrimSpace(opts.DurableSpecPath) != "" {
		check, err := verifyDurableSpecFile(opts.DurableSpecPath, proposalURL, activeSpecs)
		if err != nil {
			return report, err
		}
		report.DurableSpecPath = opts.DurableSpecPath
		report.DurableSpecCheck = check
		durableOK := true
		for _, ok := range check {
			if !ok {
				durableOK = false
			}
		}
		remote.DurableSpec = gates.Fact{Required: true, Known: true, Passed: durableOK,
			Current: fmt.Sprintf("valid=%v", durableOK), Expected: "valid=true"}
		target = gates.TargetArchive
	}
	var processEvidence []gates.ProcessEvidenceInput
	if opts.RationaleRequired || opts.ExternalEvidence != nil || hasExplicitProcessWorkspace(artifacts) {
		processEvidence = buildProcessEvidenceInputs(artifacts, opts.PRURL, opts.RationaleComments, reviewReport, opts.ExternalEvidence)
	}
	gateReport, err := gates.Evaluate(gates.Snapshot{
		Target: target, Mode: gates.ModeAuthoritative, Artifacts: artifacts,
		Canonical:       gates.CanonicalFacts{Observed: true, Diagnostics: canonical},
		Traceability:    gates.TraceabilityFacts{Observed: true, Report: traceability},
		Remote:          remote,
		ProcessEvidence: processEvidence,
	})
	if err != nil {
		return report, err
	}
	workspaceReport, err := gates.EvaluateWorkspaceEvidence(gates.WorkspaceEvaluationInput{
		Target: target, Mode: gates.ModeAuthoritative, Artifacts: artifacts,
		ExpectedRevision:    gates.Fact{Required: true, Known: strings.TrimSpace(opts.ExpectedRevision) != "", Passed: true, Expected: strings.TrimSpace(opts.ExpectedRevision)},
		IntegrationAncestry: pullRequestIntegrationAncestry(artifacts, opts.PRCommits, opts.ExpectedRevision),
		ProcessEvidence:     gateReport.Processes,
		CarrierRevisions:    mergeCarrierRevisionFacts(gates.ProcessCarrierRevisionFacts(gateReport.Processes), opts.CarrierRevisions),
	})
	if err != nil {
		return report, err
	}
	var workspaceGateDiagnostics []gates.Diagnostic
	for _, diagnostic := range workspaceReport.Diagnostics {
		if !diagnostic.Blocking && diagnostic.Severity == gates.SeverityWarning {
			report.Warnings = append(report.Warnings, diagnostic.Message)
			continue
		}
		workspaceGateDiagnostics = append(workspaceGateDiagnostics, diagnostic)
	}
	gateReport.Diagnostics = append(gateReport.Diagnostics, workspaceGateDiagnostics...)
	sort.SliceStable(gateReport.Diagnostics, func(i, j int) bool {
		if gateReport.Diagnostics[i].Code != gateReport.Diagnostics[j].Code {
			return gateReport.Diagnostics[i].Code < gateReport.Diagnostics[j].Code
		}
		return gateReport.Diagnostics[i].Artifact.ID < gateReport.Diagnostics[j].Artifact.ID
	})
	for _, diagnostic := range workspaceGateDiagnostics {
		if diagnostic.Blocking {
			gateReport.Ready = false
		}
	}
	report.Gate = gateReport
	report.ProcessEvidence = gateReport.Processes
	for _, diagnostic := range gateReport.Diagnostics {
		if diagnostic.Code == gates.CodeProcessExecutionClassLegacy {
			report.Warnings = append(report.Warnings, diagnostic.Message)
		}
		if message, ok := legacyVerifyGateError(diagnostic); ok {
			report.Errors = append(report.Errors, message)
		}
	}

	if opts.RationaleRequired {
		for _, process := range activeProcesses {
			report.RationaleCoverage[process.Comment.ID] = false
		}
		for _, process := range report.ProcessEvidence {
			for _, satisfied := range process.Satisfied {
				if satisfied == "matching inline rationale" {
					report.RationaleCoverage[process.ProcessID] = true
				}
			}
		}
		report.Diagnostics = append(report.Diagnostics, reviewReport.Diagnostics...)
		report.ReviewFindingBlockers = reviewReport.BlockingFindings
		for _, finding := range report.ReviewFindingBlockers {
			report.Errors = append(report.Errors, fmt.Sprintf("open %s review finding %s on %s:%d", finding.Severity, finding.ID, finding.Path, finding.Line))
		}
		report.FailedChecks = reviewReport.FailedChecks
		report.PendingChecks = reviewReport.PendingChecks
		for _, check := range report.FailedChecks {
			report.Errors = append(report.Errors, fmt.Sprintf("PR check %s failed state=%s conclusion=%s", check.Name, check.State, check.Conclusion))
		}
		for _, check := range report.PendingChecks {
			report.Errors = append(report.Errors, fmt.Sprintf("PR check %s is pending state=%s conclusion=%s", check.Name, check.State, check.Conclusion))
		}
	}
	if !opts.RationaleRequired {
		report.RationaleCoverage = nil
	}
	if report.DurableSpecCheck != nil {
		check := report.DurableSpecCheck
		for key, ok := range check {
			if !ok {
				report.Errors = append(report.Errors, fmt.Sprintf("durable spec missing %s", key))
			}
		}
	}
	sort.Strings(report.Errors)
	sort.Strings(report.Warnings)
	report.OK = len(report.Errors) == 0
	return report, nil
}

func mergeCarrierRevisionFacts(collected, supplied map[string]gates.CarrierRevisionFact) map[string]gates.CarrierRevisionFact {
	merged := make(map[string]gates.CarrierRevisionFact, len(collected)+len(supplied))
	for processID, fact := range collected {
		merged[processID] = fact
	}
	for processID, fact := range supplied {
		merged[processID] = fact
	}
	return merged
}

func legacyVerifyGateError(diagnostic gates.Diagnostic) (string, bool) {
	id := diagnostic.Artifact.ID
	switch diagnostic.Code {
	case gates.CodeSpecRequired:
		return "at least one active SPEC is required", true
	case gates.CodeTaskRequired:
		return "at least one active TASK is required", true
	case gates.CodeProcessRequired:
		return "at least one active PROCESS is required", true
	case gates.CodeSpecStatusInvalid:
		return fmt.Sprintf("%s must be confirmed or done before final verify", id), true
	case gates.CodeQuestionBlocked:
		return fmt.Sprintf("%s is still blocked", id), true
	case gates.CodeTaskNotDone:
		return fmt.Sprintf("%s must be done before final verify", id), true
	case gates.CodeProcessNotDone:
		return fmt.Sprintf("%s must be done before final verify", id), true
	case gates.CodeReviewOpen:
		return fmt.Sprintf("%s must be done or superseded before final verify", id), true
	case gates.CodeVerifyRequired:
		return "at least one done VERIFY comment is required", true
	case gates.CodeVerifyTestEvidenceMissing:
		return "no done VERIFY comment references test evidence (SPEC-006)", true
	case gates.CodeVerifySpecCoverageMissing:
		return fmt.Sprintf("%s is not referenced by any done VERIFY comment", id), true
	case gates.CodeProcessHandoffMissing:
		return fmt.Sprintf("%s is a serial-chain predecessor but records no ### Handoff evidence (SPEC-006)", id), true
	case gates.CodeArtifactNoncanonical:
		url := diagnostic.Artifact.URL
		if url == "" {
			url = "N/A"
		}
		return fmt.Sprintf("%s %s (%s) is noncanonical: %s", diagnostic.Artifact.Type, id, url, diagnostic.Message), true
	case gates.CodeTraceabilityInvalid:
		return diagnostic.Message, true
	case gates.CodeProcessExecutionClassInvalid, gates.CodeProcessTaskLinkMissing,
		gates.CodeProcessSpecLinkMissing, gates.CodeProcessPRLinkMissing, gates.CodeProcessCarrierMissing:
		return diagnostic.Message, true
	case gates.CodeProcessWorkspaceRequired, gates.CodeProcessWorkspaceInvalid, gates.CodeProcessWorkspaceStateInvalid,
		gates.CodeProcessWorkspaceModeInvalid, gates.CodeProcessWorkspaceRevisionUnknown, gates.CodeProcessWorkspaceRevisionStale,
		gates.CodeProcessWorkspaceReviewEvidenceMissing, gates.CodeProcessWorkspaceVerifyEvidenceMissing,
		gates.CodeProcessWorkspaceProviderEvidenceMissing:
		return diagnostic.Message, true
	default:
		// Remote check/finding diagnostics have richer legacy projections below;
		// PROCESS evidence policy is integrated by PROCESS-009.
		return "", false
	}
}

func hasExplicitProcessWorkspace(artifacts []model.Artifact) bool {
	for _, artifact := range artifacts {
		if artifact.Comment.Type == "PROCESS" && artifact.Comment.Status != "superseded" &&
			model.ParseProcessWorkspace(artifact.Comment.ID, artifact.URL, artifact.Comment.Body).Explicit {
			return true
		}
	}
	return false
}

// sectionContent returns the trimmed text of the named `###`/`##` section, up to
// the next heading of the same or higher level.
func sectionContent(body, heading string) string {
	lines := strings.Split(model.LogicalBody(body), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return ""
	}
	var out []string
	for _, line := range lines[start:] {
		if strings.HasPrefix(strings.TrimSpace(line), "## ") || strings.HasPrefix(strings.TrimSpace(line), "### ") {
			break
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func isEmptyOrNA(text string) bool {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "-"))
	return text == "" || strings.EqualFold(text, "N/A")
}

func linkValuesContain(values []string, want string) bool {
	want = model.NormalizeURL(want)
	for _, value := range values {
		if model.NormalizeURL(value) == want {
			return true
		}
	}
	return false
}

func rationaleCoverage(comments []github.PullRequestReviewComment, activeSpecIDs map[string]bool) map[string]bool {
	covered := map[string]bool{}
	for _, comment := range comments {
		marker, ok, err := model.FindRationaleMarker(comment.Body)
		if err != nil || !ok {
			continue
		}
		if marker.Process == "" || marker.Spec == "" || !activeSpecIDs[marker.Spec] {
			continue
		}
		if !strings.Contains(comment.Body, "Spec Comment:") {
			continue
		}
		covered[marker.Process] = true
	}
	return covered
}

func verifyDurableSpecFile(path, proposalURL string, specs []model.Artifact) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	body := string(data)
	check := map[string]bool{
		"final title":          strings.HasPrefix(strings.TrimSpace(body), "# "),
		"Purpose section":      strings.Contains(body, "\n## Purpose\n"),
		"Requirements section": strings.Contains(body, "\n## Requirements\n"),
		"proposal issue URL":   proposalURL != "" && strings.Contains(body, proposalURL),
		"no delta headings":    !containsDeltaHeading(body),
	}
	for _, spec := range specs {
		if spec.URL != "" {
			check["source "+spec.Comment.ID+" URL"] = strings.Contains(body, spec.URL)
		}
	}
	return check, nil
}

func containsDeltaHeading(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "## ADDED Requirements", "## MODIFIED Requirements", "## REMOVED Requirements", "## RENAMED Requirements":
			return true
		}
	}
	return false
}

func printFinalVerify(out interface{ Write([]byte) (int, error) }, report finalVerifyReport) {
	if report.OK {
		fmt.Fprintln(out, "final verify OK")
	} else {
		fmt.Fprintln(out, "final verify failed")
	}
	fmt.Fprintf(out, "traceability: %v\n", report.Traceability.OK)
	for specID, covered := range report.SpecCoverage {
		fmt.Fprintf(out, "coverage %s: %v\n", specID, covered)
	}
	for processID, covered := range report.RationaleCoverage {
		fmt.Fprintf(out, "rationale %s: %v\n", processID, covered)
	}
	for _, process := range report.ProcessEvidence {
		fmt.Fprintf(out, "process evidence: %s\n", process.Summary())
	}
	if report.DurableSpecPath != "" {
		fmt.Fprintf(out, "durable spec: %s\n", report.DurableSpecPath)
	}
	for _, err := range report.Errors {
		fmt.Fprintf(out, "- %s\n", err)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(out, "- warning: %s\n", warning)
	}
	if len(report.Diagnostics) > 0 {
		fmt.Fprintln(out, "metadata diagnostics:")
		for _, diagnostic := range report.Diagnostics {
			fmt.Fprintf(out, "- %s %s: %s\n", diagnostic.Level, diagnostic.Code, diagnostic.Message)
		}
	}
}
