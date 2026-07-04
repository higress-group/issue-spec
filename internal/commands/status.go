package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/model"
)

type statusSummary struct {
	OK                bool                      `json:"ok"`
	Repo              string                    `json:"repo"`
	Issues            map[string]int            `json:"issues"`
	Counts            map[string]map[string]int `json:"counts"`
	BlockingQuestions int                       `json:"blocking_questions"`
	OpenReviews       int                       `json:"open_reviews"`
	Verify            map[string]string         `json:"verify"`
	Traceability      model.VerifyReport        `json:"traceability"`
	NextGates         []string                  `json:"next_gates"`
}

func (a *app) runStatus(ctx context.Context, args []string) int {
	fs := newFlagSet("status", a.err)
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
	summary := summarizeStatus(*repoFlag, proposalIssue, designIssue, implementIssue, artifacts)
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

func summarizeStatus(repo string, proposal, design, implement int, artifacts []model.Artifact) statusSummary {
	counts := map[string]map[string]int{}
	verify := map[string]string{}
	blockingQuestions := 0
	openReviews := 0
	for _, artifact := range artifacts {
		tc := artifact.Comment
		if tc.Type == "" {
			continue
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
	var gates []string
	if typeTotal(counts, "SPEC") == 0 {
		gates = append(gates, "proposal requires at least one SPEC before design")
	}
	if blockingQuestions > 0 {
		gates = append(gates, "blocking QUESTION comments must be resolved or accepted as assumptions")
	}
	if design != 0 && typeTotal(counts, "TASK") == 0 {
		gates = append(gates, "design requires TASK comments before implement")
	}
	if implement != 0 && typeTotal(counts, "PROCESS") == 0 {
		gates = append(gates, "implement requires PROCESS comments before worker start")
	}
	if openReviews > 0 {
		gates = append(gates, "open REVIEW comments block final verify/archive")
	}
	if !report.OK {
		gates = append(gates, "traceability errors must be fixed")
	}
	sort.Strings(gates)
	return statusSummary{
		OK:                len(gates) == 0,
		Repo:              repo,
		Issues:            map[string]int{"proposal": proposal, "design": design, "implement": implement},
		Counts:            counts,
		BlockingQuestions: blockingQuestions,
		OpenReviews:       openReviews,
		Verify:            verify,
		Traceability:      report,
		NextGates:         gates,
	}
}

func typeTotal(counts map[string]map[string]int, typ string) int {
	total := 0
	for _, count := range counts[typ] {
		total += count
	}
	return total
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
	for _, typ := range sortedTypes(summary.Counts) {
		fmt.Fprintf(out, "%s: %s\n", typ, formatStatusCounts(summary.Counts[typ]))
	}
	if summary.Traceability.OK {
		fmt.Fprintln(out, "traceability: OK")
	} else {
		fmt.Fprintf(out, "traceability: %d error(s)\n", len(summary.Traceability.Errors))
	}
	if len(summary.NextGates) > 0 {
		fmt.Fprintln(out, "blocking gates:")
		for _, gate := range summary.NextGates {
			fmt.Fprintf(out, "- %s\n", gate)
		}
	} else {
		fmt.Fprintln(out, "blocking gates: none")
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
