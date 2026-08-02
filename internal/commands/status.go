package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/durable"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/relationships"
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
	Relationships     relationships.Index         `json:"relationships"`
}

func (a *app) runStatus(ctx context.Context, args []string) int {
	fs := newFlagSet("status", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	proposalFlag := fs.String("proposal", "", "proposal issue number or URL")
	designFlag := fs.String("design", "", "design issue number or URL")
	implementFlag := fs.String("implement", "", "implement issue number or URL")
	gateFlag := fs.String("gate", "", "planning gate: proposal, design, or implement (default inferred from supplied issues)")
	jsonOut := fs.Bool("json", false, "write JSON output")
	summaryOut := fs.Bool("summary", false, "write compact versioned JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if *summaryOut && !*jsonOut {
		a.errorf("--summary requires --json\n")
		return 2
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
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for status on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	artifacts, err := collectArtifacts(ctx, client, repo, proposalIssue, designIssue, implementIssue)
	if err != nil {
		a.errorf("collect artifacts: %v\n", err)
		return 1
	}
	answers, err := collectAnswerResolution(ctx, client, repo, proposalIssue, designIssue, implementIssue)
	if err != nil {
		a.errorf("collect ANSWER authority: %v\n", err)
		return 1
	}
	workflowPlan, workflowErr := workflow.Resolve(".")
	collection := statusGateCollection{Remote: statusForecastRemoteFacts(target), Answers: answers}
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
		var output any = summary
		if *summaryOut {
			output = gates.ProjectCompactSummary(summary.Gate, summary.Counts, collection.Subject,
				gates.Remediation{CommandFamily: "status", Arguments: compactDetailArguments(args)}, summary.Relationships)
		}
		if code := a.outputJSON(output); code != 0 {
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
	index, indexErr := relationships.BuildIndex(artifacts)
	report := model.VerifyTraceabilityWithRelationships(artifacts, commandTraceabilityEdges(index), indexErr)
	if *jsonOut {
		if code := a.outputJSON(struct {
			model.VerifyReport
			Relationships relationships.Index `json:"relationships"`
		}{VerifyReport: report, Relationships: index}); code != 0 {
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
	counts := artifactStatusCounts(artifacts)
	gateArtifacts := artifactsForImplementGate(artifacts, implement)
	relationshipIndex, relationshipErr := relationships.BuildIndex(gateArtifacts)
	verify := map[string]string{}
	blockingQuestions := 0
	openReviews := 0
	var malformed []model.CanonicalDiagnostic
	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.Type == "" {
			continue
		}
		// Cross-issue PROCESS comments are audit-visible but do not carry the
		// selected Implement gate.
		if isPlanningArtifactType(tc.Type) && tc.Status != "superseded" && (tc.Type != "PROCESS" || implement <= 0 || artifact.Issue == implement) {
			malformed = append(malformed, model.ValidateArtifactAtRoot(artifact, ".")...)
			if workflowPlan.DurableSpecsMode() == durable.ModeRepository && tc.Type == "SPEC" && strings.EqualFold(tc.Status, "confirmed") &&
				(proposal <= 0 || artifact.Issue == proposal) {
				if _, found, _ := model.ParseSpecDurableIntent(tc.ID, tc.Body, "."); !found {
					malformed = append(malformed, model.CanonicalDiagnostic{
						Severity: "error", Type: "SPEC", ID: tc.ID, URL: artifact.URL,
						Element: "durable-intent-required",
						Message: "confirmed SPEC requires exactly one Durable Intent under durable_specs.mode repository",
					})
				}
			}
		}
		if tc.Type == "QUESTION" && !model.QuestionIsSatisfied(tc, collection.Answers) {
			blockingQuestions++
		}
		if tc.Type == "REVIEW" && tc.Status != "done" && tc.Status != "superseded" {
			openReviews++
		}
		if tc.Type == "VERIFY" {
			verify[tc.ID] = tc.Status
		}
	}
	report := model.VerifyTraceabilityWithRelationships(gateArtifacts,
		commandTraceabilityEdges(relationshipIndex), relationshipErr)
	report = mergeVerifyReports(report, excludedProcessTraceability(artifacts, gateArtifacts))
	var diagnostics []metadataDiagnostic
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
	snapshot := gates.Snapshot{
		Target:        target,
		Mode:          mode,
		Artifacts:     gateArtifacts,
		Answers:       collection.Answers,
		Canonical:     gates.CanonicalFacts{Observed: true, Diagnostics: malformed},
		Traceability:  gates.TraceabilityFacts{Observed: true, Report: report},
		Relationships: observedRelationshipFacts(relationshipIndex, relationshipErr),
		Workflow:      workflowFacts,
		Remote:        collection.Remote,
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
		Relationships:     relationshipIndex,
	}
}

func commandTraceabilityEdges(index relationships.Index) []model.TraceabilityEdge {
	result := make([]model.TraceabilityEdge, 0, len(index.Edges))
	for _, edge := range index.Edges {
		result = append(result, model.TraceabilityEdge{Kind: string(edge.Kind), OwnerID: edge.Owner.ID, TargetID: edge.Target.ID})
	}
	return result
}

func observedRelationshipFacts(index relationships.Index, err error) gates.RelationshipFacts {
	facts := gates.RelationshipFacts{Required: true, Observed: true, Index: index}
	if err != nil {
		facts.Error = err.Error()
	}
	return facts
}

func excludedProcessTraceability(all, scoped []model.Artifact) model.VerifyReport {
	included := map[string]bool{}
	for _, artifact := range scoped {
		if artifact.Comment.Type == "PROCESS" {
			included[artifact.URL+"\x00"+artifact.Comment.ID] = true
		}
	}
	excludedByID := map[string]model.Artifact{}
	for _, artifact := range all {
		if artifact.Comment.Type == "PROCESS" && !included[artifact.URL+"\x00"+artifact.Comment.ID] {
			excludedByID[artifact.Comment.ID] = artifact
		}
	}
	report := model.VerifyReport{OK: true}
	for _, artifact := range scoped {
		if artifact.Comment.Type != "PROCESS" {
			continue
		}
		replacement, found, err := model.ParseSupersededBy(artifact.Comment.Body, artifact.Comment.ID)
		if err != nil || !found {
			continue
		}
		excluded, ok := excludedByID[replacement.ProcessID]
		if !ok || excluded.URL != replacement.URL {
			continue
		}
		report.Errors = append(report.Errors, fmt.Sprintf(
			"%s is outside the selected Implement issue and excluded from canonical relationship authority", excluded.Comment.ID))
	}
	sort.Strings(report.Errors)
	report.OK = len(report.Errors) == 0
	return report
}

func mergeVerifyReports(values ...model.VerifyReport) model.VerifyReport {
	result := model.VerifyReport{OK: true}
	errorsSeen, warningsSeen := map[string]bool{}, map[string]bool{}
	for _, value := range values {
		for _, message := range value.Errors {
			if !errorsSeen[message] {
				errorsSeen[message] = true
				result.Errors = append(result.Errors, message)
			}
		}
		for _, message := range value.Warnings {
			if !warningsSeen[message] {
				warningsSeen[message] = true
				result.Warnings = append(result.Warnings, message)
			}
		}
	}
	sort.Strings(result.Errors)
	sort.Strings(result.Warnings)
	result.OK = len(result.Errors) == 0
	return result
}

// artifactsForImplementGate projects only active planning types and binds
// PROCESS selection to the exact Implement issue supplied by the caller.
// Historical REVIEW/VERIFY and other inert carriers stay available in the
// surrounding status audit fields but cannot enter planning gate evaluation.
func artifactsForImplementGate(artifacts []model.Artifact, implementIssue int) []model.Artifact {
	projected := make([]model.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if !isPlanningArtifactType(artifact.Comment.Type) ||
			(implementIssue > 0 && artifact.Comment.Type == "PROCESS" && artifact.Issue != implementIssue) {
			continue
		}
		projected = append(projected, artifact)
	}
	return projected
}

func isPlanningArtifactType(value string) bool {
	switch value {
	case "SPEC", "QUESTION", "TASK", "PROCESS":
		return true
	default:
		return false
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
	case gates.TargetImplement:
		if design == 0 || implement == 0 {
			return "", fmt.Errorf("%s gate requires --design and --implement", target)
		}
		return target, nil
	default:
		return "", fmt.Errorf("unsupported value %q (want proposal, design, or implement)", raw)
	}
}

func statusForecastRemoteFacts(target gates.Target) gates.RemoteFacts {
	return gates.RemoteFacts{Workspace: gates.WorkspaceFacts{Observed: true}}
}

type statusGateCollection struct {
	Remote  gates.RemoteFacts
	Subject *gates.CompactSubject
	Answers model.AnswerResolution
}

func artifactStatusCounts(artifacts []model.Artifact) map[string]map[string]int {
	counts := map[string]map[string]int{}
	for _, artifact := range artifacts {
		comment := artifact.Comment
		if comment.Type == "" {
			continue
		}
		if counts[comment.Type] == nil {
			counts[comment.Type] = map[string]int{}
		}
		status := comment.Status
		if status == "" {
			status = "unknown"
		}
		counts[comment.Type][status]++
	}
	return counts
}

func compactDetailArguments(args []string) []string {
	detail := make([]string, 0, len(args))
	for _, argument := range args {
		name := strings.TrimLeft(argument, "-")
		if name == "summary" || strings.HasPrefix(name, "summary=") {
			continue
		}
		detail = append(detail, argument)
	}
	return detail
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
