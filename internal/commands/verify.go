package commands

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

var processIDRe = regexp.MustCompile(`PROCESS-[0-9]{3,}`)

// testEvidenceRe matches whole-word test mentions (test/tests/testing/tested) so
// that a done VERIFY summarizing test evidence is not satisfied by incidental
// substrings like "latest" or "greatest".
var testEvidenceRe = regexp.MustCompile(`(?i)\btest(s|ing|ed)?\b`)

type finalVerifyReport struct {
	OK                    bool                        `json:"ok"`
	Traceability          model.VerifyReport          `json:"traceability"`
	Errors                []string                    `json:"errors"`
	Warnings              []string                    `json:"warnings,omitempty"`
	Diagnostics           []metadataDiagnostic        `json:"diagnostics,omitempty"`
	SpecCoverage          map[string]bool             `json:"spec_coverage"`
	RationaleCoverage     map[string]bool             `json:"rationale_coverage,omitempty"`
	Noncanonical          []model.CanonicalDiagnostic `json:"noncanonical,omitempty"`
	ReviewFindingBlockers []reviewFinding             `json:"review_finding_blockers,omitempty"`
	FailedChecks          []reviewCheck               `json:"failed_checks,omitempty"`
	PendingChecks         []reviewCheck               `json:"pending_checks,omitempty"`
	PR                    int                         `json:"pr,omitempty"`
	DurableSpecPath       string                      `json:"durable_spec_path,omitempty"`
	DurableSpecCheck      map[string]bool             `json:"durable_spec_check,omitempty"`
}

type finalVerifyOptions struct {
	DurableSpecPath   string
	PR                int
	PRURL             string
	RationaleRequired bool
	RationaleComments []github.PullRequestReviewComment
	PRStatus          github.CombinedStatus
	PRCheckRuns       []github.CheckRun
}

func (a *app) runVerify(ctx context.Context, args []string) int {
	fs := newFlagSet("verify", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	proposalFlag := fs.String("proposal", "", "proposal issue number or URL")
	designFlag := fs.String("design", "", "design issue number or URL")
	implementFlag := fs.String("implement", "", "implement issue number or URL")
	prFlag := fs.Int("pr", 0, "pull request number for rationale-comment verification")
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
	client, _, err := a.clientFor(ctx, *host)
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
	var prURL string
	if *prFlag > 0 {
		pr, err := client.GetPullRequest(ctx, repo, *prFlag)
		if err != nil {
			a.errorf("read PR #%d: %v\n", *prFlag, err)
			return 1
		}
		prURL = pr.HTMLURL
		rationaleComments, err = client.ListPullRequestReviewComments(ctx, repo, *prFlag)
		if err != nil {
			a.errorf("read PR #%d review comments: %v\n", *prFlag, err)
			return 1
		}
		prStatus, err = client.GetCombinedStatus(ctx, repo, pr.Head.SHA)
		if err != nil {
			a.errorf("read PR #%d status contexts: %v\n", *prFlag, err)
			return 1
		}
		prCheckRuns, err = client.ListCheckRuns(ctx, repo, pr.Head.SHA)
		if err != nil {
			a.errorf("read PR #%d check runs: %v\n", *prFlag, err)
			return 1
		}
	}
	report, err := buildFinalVerifyReport(artifacts, proposalIssueData.HTMLURL, finalVerifyOptions{
		DurableSpecPath:   *durableSpec,
		PR:                *prFlag,
		PRURL:             prURL,
		RationaleRequired: *prFlag > 0,
		RationaleComments: rationaleComments,
		PRStatus:          prStatus,
		PRCheckRuns:       prCheckRuns,
	})
	if err != nil {
		a.errorf("verify: %v\n", err)
		return 1
	}
	report.Diagnostics = append(report.Diagnostics, authoringCompletenessDiagnostics("proposal", proposalIssueData.HTMLURL, proposalIssueData.Body)...)
	if designIssue > 0 {
		if designIssueData, derr := client.GetIssue(ctx, repo, designIssue); derr == nil {
			report.Diagnostics = append(report.Diagnostics, authoringCompletenessDiagnostics("design", designIssueData.HTMLURL, designIssueData.Body)...)
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

func buildFinalVerifyReport(artifacts []model.Artifact, proposalURL string, opts finalVerifyOptions) (finalVerifyReport, error) {
	report := finalVerifyReport{
		Traceability:      model.VerifyTraceability(artifacts),
		SpecCoverage:      map[string]bool{},
		RationaleCoverage: map[string]bool{},
		PR:                opts.PR,
	}
	report.Errors = append(report.Errors, report.Traceability.Errors...)
	report.Diagnostics = append(report.Diagnostics, typedSessionDiagnostics(artifacts)...)
	var activeSpecs []model.Artifact
	activeSpecIDs := map[string]bool{}
	var activeProcesses []model.Artifact
	var doneVerifyBodies []string
	for _, artifact := range artifacts {
		tc := artifact.Comment
		switch tc.Type {
		case "SPEC":
			if tc.Status != "superseded" {
				activeSpecs = append(activeSpecs, artifact)
				activeSpecIDs[tc.ID] = true
				report.SpecCoverage[tc.ID] = false
				if tc.Status != "confirmed" && tc.Status != "done" {
					report.Errors = append(report.Errors, fmt.Sprintf("%s must be confirmed or done before final verify", tc.ID))
				}
			}
		case "QUESTION":
			if tc.Status == "blocked" {
				report.Errors = append(report.Errors, fmt.Sprintf("%s is still blocked", tc.ID))
			}
		case "TASK":
			if tc.Status != "done" && tc.Status != "superseded" {
				report.Errors = append(report.Errors, fmt.Sprintf("%s must be done before final verify", tc.ID))
			}
		case "PROCESS":
			if tc.Status != "superseded" {
				activeProcesses = append(activeProcesses, artifact)
				if opts.RationaleRequired {
					report.RationaleCoverage[tc.ID] = false
					if opts.PRURL != "" && !linkValuesContain(tc.Links["PR"], opts.PRURL) {
						report.Errors = append(report.Errors, fmt.Sprintf("%s must link PR %s", tc.ID, opts.PRURL))
					}
				}
			}
			if tc.Status != "done" && tc.Status != "superseded" {
				report.Errors = append(report.Errors, fmt.Sprintf("%s must be done before final verify", tc.ID))
			}
		case "REVIEW":
			if tc.Status != "done" && tc.Status != "superseded" {
				report.Errors = append(report.Errors, fmt.Sprintf("%s must be done or superseded before final verify", tc.ID))
			}
		case "VERIFY":
			if tc.Status == "done" {
				doneVerifyBodies = append(doneVerifyBodies, tc.Body)
			}
		}
	}
	// Recompute canonical validity from remote bodies so a write-time
	// --allow-noncanonical bypass cannot durably pass final verify. This blocks
	// archive readiness before durable spec creation when any active required
	// typed comment is malformed.
	for _, artifact := range artifacts {
		if artifact.Comment.Status == "superseded" {
			continue
		}
		diags := model.ValidateArtifact(artifact)
		if len(diags) == 0 {
			continue
		}
		report.Noncanonical = append(report.Noncanonical, diags...)
		for _, d := range diags {
			url := d.URL
			if url == "" {
				url = "N/A"
			}
			report.Errors = append(report.Errors, fmt.Sprintf("%s %s (%s) is noncanonical: %s", d.Type, d.ID, url, d.Message))
		}
	}
	if len(activeSpecs) == 0 {
		report.Errors = append(report.Errors, "at least one active SPEC is required")
	}
	if len(doneVerifyBodies) == 0 {
		report.Errors = append(report.Errors, "at least one done VERIFY comment is required")
	}
	verifyText := strings.Join(doneVerifyBodies, "\n")
	// SPEC-006: serial-chain PROCESS predecessors must record ### Handoff
	// evidence, and a done VERIFY must summarize test evidence. A PROCESS is a
	// serial-chain predecessor when another active PROCESS declares it as a
	// dependency; parent-TASK presence itself is already enforced by canonical
	// PROCESS validation above.
	report.Errors = append(report.Errors, serialHandoffErrors(activeProcesses)...)
	if len(doneVerifyBodies) > 0 && !testEvidenceRe.MatchString(verifyText) {
		report.Errors = append(report.Errors, "no done VERIFY comment references test evidence (SPEC-006)")
	}
	for _, spec := range activeSpecs {
		if strings.Contains(verifyText, spec.Comment.ID) {
			report.SpecCoverage[spec.Comment.ID] = true
		} else {
			report.Errors = append(report.Errors, fmt.Sprintf("%s is not referenced by any done VERIFY comment", spec.Comment.ID))
		}
	}
	if opts.RationaleRequired {
		covered := rationaleCoverage(opts.RationaleComments, activeSpecIDs)
		for _, process := range activeProcesses {
			if covered[process.Comment.ID] {
				report.RationaleCoverage[process.Comment.ID] = true
			} else {
				report.Errors = append(report.Errors, fmt.Sprintf("%s has no PR rationale comment linked to an active SPEC", process.Comment.ID))
			}
		}
		reviewReport := buildReviewSyncReport(github.PullRequest{Number: opts.PR, HTMLURL: opts.PRURL}, opts.RationaleComments, nil, opts.PRStatus, opts.PRCheckRuns)
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
	if strings.TrimSpace(opts.DurableSpecPath) != "" {
		check, err := verifyDurableSpecFile(opts.DurableSpecPath, proposalURL, activeSpecs)
		if err != nil {
			return report, err
		}
		report.DurableSpecPath = opts.DurableSpecPath
		report.DurableSpecCheck = check
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

// serialHandoffErrors reports done PROCESS predecessors in a serial chain that
// carry no ### Handoff evidence. A predecessor is any PROCESS that another active
// PROCESS declares as a dependency.
func serialHandoffErrors(processes []model.Artifact) []string {
	ids := map[string]bool{}
	for _, p := range processes {
		if p.Comment.ID != "" {
			ids[p.Comment.ID] = true
		}
	}
	dependedUpon := map[string]bool{}
	for _, p := range processes {
		for _, dep := range processDependencyIDs(p.Comment.Body) {
			if dep != p.Comment.ID && ids[dep] {
				dependedUpon[dep] = true
			}
		}
	}
	var errs []string
	for _, p := range processes {
		if p.Comment.Status != "done" || !dependedUpon[p.Comment.ID] {
			continue
		}
		if isEmptyOrNA(sectionContent(p.Comment.Body, "### Handoff")) {
			errs = append(errs, fmt.Sprintf("%s is a serial-chain predecessor but records no ### Handoff evidence (SPEC-006)", p.Comment.ID))
		}
	}
	return errs
}

// processDependencyIDs extracts referenced PROCESS ids from a PROCESS body's
// ### Dependencies section.
func processDependencyIDs(body string) []string {
	deps := sectionContent(body, "### Dependencies")
	if deps == "" {
		return nil
	}
	return processIDRe.FindAllString(deps, -1)
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
	if report.DurableSpecPath != "" {
		fmt.Fprintf(out, "durable spec: %s\n", report.DurableSpecPath)
	}
	for _, err := range report.Errors {
		fmt.Fprintf(out, "- %s\n", err)
	}
	if len(report.Diagnostics) > 0 {
		fmt.Fprintln(out, "metadata diagnostics:")
		for _, diagnostic := range report.Diagnostics {
			fmt.Fprintf(out, "- %s %s: %s\n", diagnostic.Level, diagnostic.Code, diagnostic.Message)
		}
	}
}
