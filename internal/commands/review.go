package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func (a *app) runReview(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec review finding|reply|sync ...\n")
		return 2
	}
	switch args[0] {
	case "finding":
		return a.runReviewFinding(ctx, args[1:])
	case "reply":
		return a.runReviewReply(ctx, args[1:])
	case "sync":
		return a.runReviewSync(ctx, args[1:])
	default:
		a.errorf("unknown review command %q\n", args[0])
		return 2
	}
}

func (a *app) runReviewFinding(ctx context.Context, args []string) int {
	fs := newFlagSet("review finding", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	prFlag := fs.Int("pr", 0, "pull request number")
	implementFlag := fs.String("implement", "", "implement issue containing the active self-hosted code reference")
	revision := fs.String("revision", "", "expected external code head revision for self-hosted evidence")
	pathFlag := fs.String("path", "", "changed file path")
	lineFlag := fs.Int("line", 0, "RIGHT-side line number in the PR diff")
	id := fs.String("id", "", "FINDING id")
	severity := fs.String("severity", "P2", "finding severity: P0, P1, or P2")
	processID := fs.String("process", "", "PROCESS id assigned to fix this finding")
	specID := fs.String("spec", "", "SPEC id")
	specURL := fs.String("spec-url", "", "SPEC comment URL")
	bodyFile := fs.String("body-file", "", "finding body file, or - for stdin")
	bodyText := fs.String("body", "", "finding body text")
	agent := fs.String("agent", "Review Agent", "logical agent identity")
	agentSession := addAgentSessionFlag(fs)
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	if strings.TrimSpace(*pathFlag) == "" {
		a.errorf("--path is required\n")
		return 2
	}
	if *lineFlag <= 0 {
		a.errorf("--line must be a positive RIGHT-side diff line\n")
		return 2
	}
	body := strings.TrimSpace(*bodyText)
	if *bodyFile != "" {
		content, ok := a.readBodyFile(*bodyFile)
		if !ok {
			return 2
		}
		body = strings.TrimSpace(content)
	}
	client, token, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for review finding on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	profile, _, err := auth.ResolveProfile(a.profileName, *host)
	if err != nil {
		a.errorf("resolve review profile: %v\n", err)
		return 1
	}
	session := resolveWriterSession(*agentSession)
	if profile.Kind == auth.ProfileKindHosted {
		if *prFlag > 0 {
			a.errorf("--pr is not a self-hosted code authority; omit it and use --implement\n")
			return 2
		}
		implementIssue, parseErr := parseIssueFlag(*implementFlag, "implement")
		if parseErr != nil {
			a.errorf("%v\n", parseErr)
			return 2
		}
		target, provider, _, _, preflightErr := a.externalMutationTarget(ctx, *host, token.Value, repo, implementIssue,
			"code_change", *revision, codereview.CapabilityChangeComment)
		if preflightErr != nil {
			a.errorf("review finding capability preflight: %v\n", preflightErr)
			return 1
		}
		rendered, renderErr := model.RenderFindingBodyWithSession(*agent, session.ID, session.Source, *id, *severity,
			*processID, *specID, *specURL, body, "open", *pathFlag, *lineFlag)
		if renderErr != nil {
			a.errorf("create review finding: %v\n", renderErr)
			return 1
		}
		mutation, mutateErr := codereview.Mutate(ctx, provider, codereview.MutationRequest{Kind: codereview.MutationComment,
			Reference: target.Reference, Body: rendered, HeadRevision: target.SubjectRevision,
			Metadata: map[string]any{"kind": "finding", "finding": *id, "severity": model.NormalizeFindingSeverity(*severity),
				"process": *processID, "spec": *specID, "path": *pathFlag, "line": *lineFlag}})
		if mutateErr != nil {
			a.errorf("create review finding: %v\n", mutateErr)
			return 1
		}
		result := reviewFindingResult{OK: true, Created: true, URL: mutation.CanonicalURL, Path: *pathFlag,
			Line: *lineFlag, Finding: *id, Severity: model.NormalizeFindingSeverity(*severity), Process: *processID,
			Spec: *specID, ExternalID: mutation.ExternalID, ChangeID: mutation.Reference.ChangeID}
		if *jsonOut {
			return a.outputJSON(result)
		}
		fmt.Fprintf(a.out, "created external review finding: %s\n", result.URL)
		return 0
	}
	if *prFlag <= 0 {
		a.errorf("--pr must be a positive pull request number\n")
		return 2
	}
	result, err := createReviewFinding(ctx, client, repo, *prFlag, *pathFlag, *lineFlag, *id, *severity, *processID, *specID, *specURL, *agent, session, body)
	if err != nil {
		a.errorf("create review finding: %v\n", err)
		return 1
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	if result.Created {
		fmt.Fprintf(a.out, "created review finding: %s\n", result.URL)
	} else {
		fmt.Fprintf(a.out, "review finding already exists: %s\n", result.URL)
	}
	return 0
}

func (a *app) runReviewReply(ctx context.Context, args []string) int {
	fs := newFlagSet("review reply", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	prFlag := fs.Int("pr", 0, "pull request number")
	implementFlag := fs.String("implement", "", "implement issue containing the active self-hosted code reference")
	revision := fs.String("revision", "", "expected external code head revision for self-hosted evidence")
	commentID := fs.Int64("comment-id", 0, "parent PR review comment id")
	externalCommentID := fs.String("external-comment-id", "", "parent external review comment id")
	findingID := fs.String("finding", "", "FINDING id")
	processID := fs.String("process", "", "PROCESS id that fixed this finding")
	status := fs.String("status", "resolved", "reply status: resolved, fixed, done, closed, or superseded")
	bodyFile := fs.String("body-file", "", "reply body file, or - for stdin")
	bodyText := fs.String("body", "", "reply body text")
	agent := fs.String("agent", "Worker Agent", "logical agent identity")
	agentSession := addAgentSessionFlag(fs)
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	body := strings.TrimSpace(*bodyText)
	if *bodyFile != "" {
		content, ok := a.readBodyFile(*bodyFile)
		if !ok {
			return 2
		}
		body = strings.TrimSpace(content)
	}
	client, token, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for review reply on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	session := resolveWriterSession(*agentSession)
	profile, _, err := auth.ResolveProfile(a.profileName, *host)
	if err != nil {
		a.errorf("resolve review profile: %v\n", err)
		return 1
	}
	if profile.Kind == auth.ProfileKindHosted {
		if *prFlag > 0 || *commentID > 0 {
			a.errorf("--pr and --comment-id are GitHub-only; use --implement and --external-comment-id\n")
			return 2
		}
		if strings.TrimSpace(*externalCommentID) == "" {
			a.errorf("--external-comment-id is required\n")
			return 2
		}
		implementIssue, parseErr := parseIssueFlag(*implementFlag, "implement")
		if parseErr != nil {
			a.errorf("%v\n", parseErr)
			return 2
		}
		target, provider, _, _, preflightErr := a.externalMutationTarget(ctx, *host, token.Value, repo, implementIssue,
			"code_change", *revision, codereview.CapabilityChangeComment)
		if preflightErr != nil {
			a.errorf("review reply capability preflight: %v\n", preflightErr)
			return 1
		}
		rendered, renderErr := model.RenderFindingReplyBodyWithSession(*agent, session.ID, session.Source,
			*findingID, *processID, *status, body)
		if renderErr != nil {
			a.errorf("reply to review finding: %v\n", renderErr)
			return 1
		}
		mutation, mutateErr := codereview.Mutate(ctx, provider, codereview.MutationRequest{Kind: codereview.MutationComment,
			Reference: target.Reference, Body: rendered, HeadRevision: target.SubjectRevision,
			Metadata: map[string]any{"kind": "finding_reply", "parent_external_id": *externalCommentID,
				"finding": *findingID, "process": *processID, "status": model.NormalizeFindingStatus(*status)}})
		if mutateErr != nil {
			a.errorf("reply to review finding: %v\n", mutateErr)
			return 1
		}
		result := reviewReplyResult{OK: true, Created: true, URL: mutation.CanonicalURL, Finding: *findingID,
			Process: *processID, Status: model.NormalizeFindingStatus(*status), ExternalID: mutation.ExternalID,
			ParentExternalID: *externalCommentID, ChangeID: mutation.Reference.ChangeID}
		if *jsonOut {
			return a.outputJSON(result)
		}
		fmt.Fprintf(a.out, "created external review finding reply: %s\n", result.URL)
		return 0
	}
	if *prFlag <= 0 {
		a.errorf("--pr must be a positive pull request number\n")
		return 2
	}
	if *commentID <= 0 {
		a.errorf("--comment-id must be a positive PR review comment id\n")
		return 2
	}
	result, err := replyReviewFinding(ctx, client, repo, *prFlag, *commentID, *findingID, *processID, *status, *agent, session, body)
	if err != nil {
		a.errorf("reply to review finding: %v\n", err)
		return 1
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	if result.Created {
		fmt.Fprintf(a.out, "created review finding reply: %s\n", result.URL)
	} else {
		fmt.Fprintf(a.out, "review finding reply already exists: %s\n", result.URL)
	}
	return 0
}

func (a *app) runReviewSync(ctx context.Context, args []string) int {
	fs := newFlagSet("review sync", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name")
	host := fs.String("hostname", "github.com", "GitHub hostname")
	prFlag := fs.Int("pr", 0, "pull request number")
	revision := fs.String("revision", "", "expected external code head revision for self-hosted evidence")
	implementFlag := fs.String("implement", "", "implement issue number or URL")
	id := fs.String("id", "", "REVIEW id to upsert")
	agent := fs.String("agent", "Coordinator", "logical agent identity")
	agentSession := addAgentSessionFlag(fs)
	scope := fs.String("scope", "pr-review", "review scope")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	implementIssue, err := parseIssueFlag(*implementFlag, "implement")
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	if strings.TrimSpace(*id) == "" {
		a.errorf("--id is required\n")
		return 2
	}
	client, token, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for review sync on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	externalGate, selfHosted, err := a.externalGate(ctx, *host, token.Value, repo, implementIssue,
		"code_change", *revision, coreevidence.GateReview)
	if err != nil {
		a.errorf("review external evidence: %v\n", err)
		return 1
	}
	if selfHosted {
		if *prFlag > 0 {
			a.errorf("--pr is not a self-hosted code authority; omit it and use the active code_change reference\n")
			return 2
		}
		session := resolveWriterSession(*agentSession)
		action, comment, err := upsertExternalReviewSyncCommentAt(ctx, client, repo, implementIssue,
			*id, *agent, session, *scope, externalGate, time.Now().UTC())
		if err != nil {
			a.errorf("upsert REVIEW %s: %v\n", *id, err)
			return 1
		}
		result := map[string]any{"ok": true, "action": action, "comment_id": comment.ID, "url": comment.HTMLURL,
			"external_evidence": externalGate.Consumption}
		if *jsonOut {
			return a.outputJSON(result)
		}
		fmt.Fprintf(a.out, "%s REVIEW %s from external evidence revision %s: %s\n", action, *id,
			externalGate.Target.SubjectRevision, comment.HTMLURL)
		return 0
	}
	if *prFlag <= 0 {
		a.errorf("--pr must be a positive pull request number\n")
		return 2
	}
	pr, err := client.GetPullRequest(ctx, repo, *prFlag)
	if err != nil {
		a.errorf("read PR #%d: %v\n", *prFlag, err)
		return 1
	}
	reviewComments, err := client.ListPullRequestReviewComments(ctx, repo, *prFlag)
	if err != nil {
		a.errorf("read PR #%d review comments: %v\n", *prFlag, err)
		return 1
	}
	issueComments, err := client.ListIssueComments(ctx, repo, *prFlag)
	if err != nil {
		a.errorf("read PR #%d issue comments: %v\n", *prFlag, err)
		return 1
	}
	status, err := client.GetCombinedStatus(ctx, repo, pr.Head.SHA)
	if err != nil {
		a.errorf("read PR #%d status contexts: %v\n", *prFlag, err)
		return 1
	}
	checkRuns, err := client.ListCheckRuns(ctx, repo, pr.Head.SHA)
	if err != nil {
		a.errorf("read PR #%d check runs: %v\n", *prFlag, err)
		return 1
	}
	report := buildReviewSyncReport(pr, reviewComments, issueComments, status, checkRuns)
	if artifacts, collectErr := collectArtifacts(ctx, client, repo, implementIssue); collectErr == nil {
		for _, input := range buildProcessEvidenceInputs(artifacts, pr.HTMLURL, reviewComments, report, nil) {
			report.ProcessEvidence = append(report.ProcessEvidence, gates.EvaluateProcessEvidence(input, gates.TargetFinal, gates.ModeForecast))
		}
	} else {
		a.errorf("collect PROCESS evidence for review sync: %v\n", collectErr)
		return 1
	}
	session := resolveWriterSession(*agentSession)
	body, err := renderReviewSyncComment(*id, *agent, session, *scope, pr.HTMLURL, report)
	if err != nil {
		a.errorf("render review sync comment: %v\n", err)
		return 1
	}
	action, comment, _, err := upsertTypedComment(ctx, client, repo, implementIssue, "REVIEW", *id, body)
	if err != nil {
		a.errorf("upsert REVIEW %s: %v\n", *id, err)
		return 1
	}
	result := map[string]any{"ok": report.OK, "action": action, "comment_id": comment.ID, "url": comment.HTMLURL, "review": report}
	if *jsonOut {
		if code := a.outputJSON(result); code != 0 {
			return code
		}
		if !report.OK {
			return 1
		}
		return 0
	}
	fmt.Fprintf(a.out, "%s REVIEW %s: %s\n", action, *id, comment.HTMLURL)
	if !report.OK {
		return 1
	}
	return 0
}

func renderExternalReviewSyncComment(id, agent string, session writerSession, scope string, gate externalGateResult) (string, error) {
	return renderExternalReviewSyncCommentAt(id, agent, session, scope, gate, time.Now().UTC())
}

func renderExternalReviewSyncCommentAt(id, agent string, session writerSession, scope string, gate externalGateResult,
	synchronizedAt time.Time) (string, error) {
	if !gate.Evaluation.Passed {
		return "", errors.New("external review evidence gate has not passed")
	}
	if err := validateReviewCompletionTarget(gate.Target); err != nil {
		return "", err
	}
	if err := validateExactSnapshotIdentity(gate.Snapshot, gate.Target); err != nil {
		return "", fmt.Errorf("external review snapshot: %w", err)
	}
	findings, err := canonicalExternalReviewFindings(gate)
	if err != nil {
		return "", err
	}
	rawFindings, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return "", err
	}
	logical := fmt.Sprintf("## Review Summary: external code evidence\n\nEvaluated trusted evidence for `%s` at exact revision `%s`.\n\n### Canonical Findings\n\n```json\n%s\n```\n\nNo open P0/P1 findings remain in the consumed snapshot. External code review remains the owner of line-level discussion content.\n\n### Verdict\n\nPassed provider-neutral review evidence gate.",
		gate.Target.Reference.ChangeID, gate.Target.SubjectRevision, rawFindings)
	body, err := model.EnsureTypedBody("REVIEW", id, logical, model.BodyOptions{Agent: agent,
		AgentSessionID: session.ID, AgentSessionSource: session.Source, SubjectRevision: gate.Target.SubjectRevision,
		Status: "done", Scope: scope})
	if err != nil {
		return "", err
	}
	if len(findings) > 0 {
		body, _, err = stampConsumedEvidence(body, gate.Consumption)
		if err != nil {
			return "", err
		}
	}
	body, _, err = stampExternalReviewCompletion(body, externalReviewCompletion{
		ProviderKey: gate.Target.Reference.ProviderKey, ExternalRepository: gate.Target.Reference.ExternalRepository,
		ChangeID: gate.Target.Reference.ChangeID, ReferenceVersion: gate.Target.ReferenceVersion,
		SubjectRevision: gate.Target.SubjectRevision, SynchronizedAt: synchronizedAt.UTC(),
	})
	if err != nil {
		return "", err
	}
	policy := gate.ReviewCompletionPolicy
	policy.Required = true
	if err := validateExternalReviewCompletionAt(model.ParseTypedComment(body), gate.Target, policy, synchronizedAt); err != nil {
		return "", err
	}
	return body, nil
}

func stampExternalReviewCompletion(body string, completion externalReviewCompletion) (string, bool, error) {
	if err := validateReviewCompletionIdentity(completion); err != nil {
		return "", false, err
	}
	raw, err := json.Marshal(completion)
	if err != nil {
		return "", false, err
	}
	block := externalReviewCompletionStart + "\n" + string(raw) + "\n" + externalReviewCompletionEnd
	startCount := strings.Count(body, externalReviewCompletionStart)
	endCount := strings.Count(body, externalReviewCompletionEnd)
	if startCount != endCount || startCount > 1 || strings.Count(body, "issue-spec:external-review-completion") != startCount+endCount {
		return "", false, errors.New("existing external REVIEW completion block is malformed")
	}
	start := strings.Index(body, externalReviewCompletionStart)
	end := strings.Index(body, externalReviewCompletionEnd)
	if startCount == 1 && end < start+len(externalReviewCompletionStart) {
		return "", false, errors.New("existing external REVIEW completion block is malformed")
	}
	updated := body
	if start >= 0 {
		end += len(externalReviewCompletionEnd)
		updated = body[:start] + block + body[end:]
	} else {
		updated = strings.TrimRight(body, "\n") + "\n\n" + block + "\n"
	}
	return updated, updated != body, nil
}

// upsertExternalReviewSyncCommentAt computes the synchronization instant only
// after the authoritative provider snapshot has passed. It performs every
// local validation before the create/update call and advances a pre-existing
// completion timestamp even when the injected/current clock has not moved.
func upsertExternalReviewSyncCommentAt(ctx context.Context, client github.Operations, repo string, issueNumber int,
	id, agent string, session writerSession, scope string, gate externalGateResult, synchronizationNow time.Time) (string, github.Comment, error) {
	comments, err := client.ListIssueComments(ctx, repo, issueNumber)
	if err != nil {
		return "", github.Comment{}, err
	}
	var existing *github.Comment
	var previous time.Time
	for index := range comments {
		parsed := model.ParseTypedComment(comments[index].Body)
		if parsed.Type != "REVIEW" || parsed.ID != id {
			continue
		}
		if existing != nil {
			return "", github.Comment{}, fmt.Errorf("multiple active REVIEW comments use id %s", id)
		}
		existing = &comments[index]
		if completion, found, parseErr := parseExternalReviewCompletion(comments[index].Body); parseErr == nil && found {
			previous = completion.SynchronizedAt
		}
	}
	synchronizedAt := synchronizationNow.UTC()
	if synchronizedAt.IsZero() {
		return "", github.Comment{}, errors.New("external REVIEW synchronization time is invalid")
	}
	if !previous.IsZero() && !synchronizedAt.After(previous) {
		synchronizedAt = previous.Add(time.Nanosecond)
		if !synchronizedAt.After(previous) {
			return "", github.Comment{}, errors.New("external REVIEW synchronization time cannot advance")
		}
	}
	body, err := renderExternalReviewSyncCommentAt(id, agent, session, scope, gate, synchronizedAt)
	if err != nil {
		return "", github.Comment{}, err
	}
	if existing == nil {
		created, createErr := client.CreateComment(ctx, repo, issueNumber, body)
		return "created", created, createErr
	}
	for _, url := range model.RelatedCommentURLs(model.ParseTypedComment(existing.Body)) {
		body, _, err = model.AddRelatedCommentLink(body, url)
		if err != nil {
			return "", github.Comment{}, err
		}
	}
	policy := gate.ReviewCompletionPolicy
	policy.Required = true
	if err := validateExternalReviewCompletionAt(model.ParseTypedComment(body), gate.Target, policy, synchronizationNow); err != nil {
		return "", github.Comment{}, err
	}
	updated, updateErr := client.UpdateComment(ctx, repo, existing.ID, body)
	return "updated", updated, updateErr
}

type canonicalExternalReviewFinding struct {
	EvidenceID   string `json:"evidence_id"`
	FindingID    string `json:"finding_id"`
	ProcessID    string `json:"process_id"`
	SpecID       string `json:"spec_id"`
	Severity     string `json:"severity"`
	State        string `json:"state"`
	CanonicalURL string `json:"canonical_url,omitempty"`
}

func canonicalExternalReviewFindings(gate externalGateResult) ([]canonicalExternalReviewFinding, error) {
	consumed := make(map[string]struct{}, len(gate.Evaluation.EvidenceIDs))
	for _, id := range gate.Evaluation.EvidenceIDs {
		consumed[id] = struct{}{}
	}
	findings := make([]canonicalExternalReviewFinding, 0)
	for _, record := range gate.Snapshot.Records {
		if record.Kind != codereview.EvidenceReview {
			continue
		}
		if _, ok := consumed[record.ID]; !ok {
			continue
		}
		if err := record.ValidateReviewLinkage(); err != nil {
			return nil, fmt.Errorf("canonical external review finding %q: %w", record.ID, err)
		}
		findings = append(findings, canonicalExternalReviewFinding{EvidenceID: record.ID,
			FindingID: strings.TrimSpace(record.FindingID), ProcessID: strings.TrimSpace(record.ProcessID),
			SpecID: strings.TrimSpace(record.SpecID), Severity: strings.ToUpper(strings.TrimSpace(record.Severity)),
			State: strings.ToLower(strings.TrimSpace(record.State)), CanonicalURL: strings.TrimSpace(record.CanonicalURL)})
	}
	sort.Slice(findings, func(i, j int) bool {
		left := findings[i].FindingID + "\x00" + findings[i].ProcessID + "\x00" + findings[i].SpecID + "\x00" + findings[i].EvidenceID
		right := findings[j].FindingID + "\x00" + findings[j].ProcessID + "\x00" + findings[j].SpecID + "\x00" + findings[j].EvidenceID
		return left < right
	})
	return findings, nil
}

type reviewFindingResult struct {
	OK         bool   `json:"ok"`
	Created    bool   `json:"created"`
	CommentID  int64  `json:"comment_id"`
	URL        string `json:"url"`
	PR         int    `json:"pr"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Finding    string `json:"finding"`
	Severity   string `json:"severity"`
	Process    string `json:"process"`
	Spec       string `json:"spec"`
	ExternalID string `json:"external_id,omitempty"`
	ChangeID   string `json:"change_id,omitempty"`
}

type reviewReplyResult struct {
	OK               bool   `json:"ok"`
	Created          bool   `json:"created"`
	CommentID        int64  `json:"comment_id"`
	ParentCommentID  int64  `json:"parent_comment_id"`
	URL              string `json:"url"`
	PR               int    `json:"pr"`
	Finding          string `json:"finding"`
	Process          string `json:"process"`
	Status           string `json:"status"`
	ExternalID       string `json:"external_id,omitempty"`
	ParentExternalID string `json:"parent_external_id,omitempty"`
	ChangeID         string `json:"change_id,omitempty"`
}

func createReviewFinding(ctx context.Context, client interface {
	GetPullRequest(context.Context, string, int) (github.PullRequest, error)
	ListPullRequestFiles(context.Context, string, int) ([]github.PullRequestFile, error)
	ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error)
	CreatePullRequestReviewComment(context.Context, string, int, string, string, string, int, string) (github.PullRequestReviewComment, error)
}, repo string, prNumber int, path string, line int, findingID, severity, processID, specID, specURL, agent string, session writerSession, findingBody string) (reviewFindingResult, error) {
	path = strings.TrimSpace(path)
	findingID = strings.TrimSpace(findingID)
	processID = strings.TrimSpace(processID)
	specID = strings.TrimSpace(specID)
	files, err := client.ListPullRequestFiles(ctx, repo, prNumber)
	if err != nil {
		return reviewFindingResult{}, err
	}
	if !lineExistsInPullRequestFiles(path, line, files) {
		return reviewFindingResult{}, fmt.Errorf("%s:%d is not a RIGHT-side changed line in PR #%d", path, line, prNumber)
	}
	existing, err := client.ListPullRequestReviewComments(ctx, repo, prNumber)
	if err != nil {
		return reviewFindingResult{}, err
	}
	for _, comment := range existing {
		marker, ok, err := model.FindFindingMarker(comment.Body)
		if err != nil {
			return reviewFindingResult{}, err
		}
		if ok && model.SameFinding(marker, findingID, path, line) {
			return reviewFindingResult{
				OK:        true,
				Created:   false,
				CommentID: comment.ID,
				URL:       comment.HTMLURL,
				PR:        prNumber,
				Path:      path,
				Line:      line,
				Finding:   findingID,
				Severity:  marker.Severity,
				Process:   processID,
				Spec:      specID,
			}, nil
		}
	}
	pr, err := client.GetPullRequest(ctx, repo, prNumber)
	if err != nil {
		return reviewFindingResult{}, err
	}
	body, err := model.RenderFindingBodyWithSession(agent, session.ID, session.Source, findingID, severity, processID, specID, specURL, findingBody, "open", path, line)
	if err != nil {
		return reviewFindingResult{}, err
	}
	comment, err := client.CreatePullRequestReviewComment(ctx, repo, prNumber, body, pr.Head.SHA, path, line, "RIGHT")
	if err != nil {
		return reviewFindingResult{}, err
	}
	return reviewFindingResult{
		OK:        true,
		Created:   true,
		CommentID: comment.ID,
		URL:       comment.HTMLURL,
		PR:        prNumber,
		Path:      path,
		Line:      line,
		Finding:   findingID,
		Severity:  model.NormalizeFindingSeverity(severity),
		Process:   processID,
		Spec:      specID,
	}, nil
}

func replyReviewFinding(ctx context.Context, client interface {
	ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error)
	ReplyPullRequestReviewComment(context.Context, string, int, int64, string) (github.PullRequestReviewComment, error)
}, repo string, prNumber int, parentCommentID int64, findingID, processID, status, agent string, session writerSession, replyBody string) (reviewReplyResult, error) {
	findingID = strings.TrimSpace(findingID)
	processID = strings.TrimSpace(processID)
	status = model.NormalizeFindingStatus(status)
	existing, err := client.ListPullRequestReviewComments(ctx, repo, prNumber)
	if err != nil {
		return reviewReplyResult{}, err
	}
	foundParent := false
	for _, comment := range existing {
		if comment.ID == parentCommentID {
			foundParent = true
			continue
		}
		if comment.InReplyToID != parentCommentID {
			continue
		}
		marker, ok, err := model.FindFindingReplyMarker(comment.Body)
		if err != nil {
			return reviewReplyResult{}, err
		}
		if ok && marker.Finding == findingID && marker.Process == processID && marker.Status == status &&
			strings.TrimSpace(marker.Agent) == strings.TrimSpace(agent) {
			return reviewReplyResult{
				OK:              true,
				Created:         false,
				CommentID:       comment.ID,
				ParentCommentID: parentCommentID,
				URL:             comment.HTMLURL,
				PR:              prNumber,
				Finding:         findingID,
				Process:         processID,
				Status:          status,
			}, nil
		}
	}
	if !foundParent {
		return reviewReplyResult{}, fmt.Errorf("parent PR review comment %d not found on PR #%d", parentCommentID, prNumber)
	}
	body, err := model.RenderFindingReplyBodyWithSession(agent, session.ID, session.Source, findingID, processID, status, replyBody)
	if err != nil {
		return reviewReplyResult{}, err
	}
	comment, err := client.ReplyPullRequestReviewComment(ctx, repo, prNumber, parentCommentID, body)
	if err != nil {
		return reviewReplyResult{}, err
	}
	return reviewReplyResult{
		OK:              true,
		Created:         true,
		CommentID:       comment.ID,
		ParentCommentID: parentCommentID,
		URL:             comment.HTMLURL,
		PR:              prNumber,
		Finding:         findingID,
		Process:         processID,
		Status:          status,
	}, nil
}

type reviewSyncReport struct {
	OK                 bool                          `json:"ok"`
	PR                 int                           `json:"pr"`
	PRURL              string                        `json:"pr_url"`
	SubjectRevision    string                        `json:"subject_revision,omitempty"`
	RevisionSource     string                        `json:"revision_source,omitempty"`
	RationaleComments  int                           `json:"rationale_comments"`
	ActionableFindings []reviewFinding               `json:"actionable_findings"`
	BlockingFindings   []reviewFinding               `json:"blocking_findings"`
	ResolvedFindings   []reviewFinding               `json:"resolved_findings"`
	FindingReplies     []reviewReply                 `json:"finding_replies,omitempty"`
	Rationales         []reviewRationale             `json:"rationales,omitempty"`
	IssueComments      int                           `json:"issue_comments"`
	Diagnostics        []metadataDiagnostic          `json:"diagnostics,omitempty"`
	FailedChecks       []reviewCheck                 `json:"failed_checks"`
	PendingChecks      []reviewCheck                 `json:"pending_checks"`
	PassedChecks       []reviewCheck                 `json:"passed_checks"`
	ProcessEvidence    []gates.ProcessEvidenceReport `json:"process_evidence,omitempty"`
}

type reviewFinding struct {
	ID                 string `json:"id,omitempty"`
	CommentID          int64  `json:"comment_id"`
	URL                string `json:"url"`
	Path               string `json:"path"`
	Line               int    `json:"line"`
	Severity           string `json:"severity"`
	Status             string `json:"status"`
	Process            string `json:"process,omitempty"`
	Spec               string `json:"spec,omitempty"`
	Agent              string `json:"agent,omitempty"`
	AgentSessionID     string `json:"agent_session_id,omitempty"`
	AgentSessionSource string `json:"agent_session_source,omitempty"`
	ResolvedByAgent    string `json:"resolved_by_agent,omitempty"`
	SubjectRevision    string `json:"subject_revision,omitempty"`
	RevisionSource     string `json:"revision_source,omitempty"`
	Summary            string `json:"summary"`
}

type reviewReply struct {
	Finding            string `json:"finding,omitempty"`
	CommentID          int64  `json:"comment_id"`
	URL                string `json:"url"`
	Process            string `json:"process,omitempty"`
	Status             string `json:"status"`
	Agent              string `json:"agent,omitempty"`
	AgentSessionID     string `json:"agent_session_id,omitempty"`
	AgentSessionSource string `json:"agent_session_source,omitempty"`
}

type reviewRationale struct {
	Process            string `json:"process,omitempty"`
	Spec               string `json:"spec,omitempty"`
	CommentID          int64  `json:"comment_id"`
	URL                string `json:"url"`
	Agent              string `json:"agent,omitempty"`
	AgentSessionID     string `json:"agent_session_id,omitempty"`
	AgentSessionSource string `json:"agent_session_source,omitempty"`
}

type reviewCheck struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	Conclusion      string `json:"conclusion,omitempty"`
	URL             string `json:"url,omitempty"`
	SubjectRevision string `json:"subject_revision,omitempty"`
	Trusted         bool   `json:"trusted"`
	Source          string `json:"source,omitempty"`
}

func buildReviewSyncReport(pr github.PullRequest, reviewComments []github.PullRequestReviewComment, issueComments []github.Comment, status github.CombinedStatus, checkRuns []github.CheckRun) reviewSyncReport {
	report := reviewSyncReport{PR: pr.Number, PRURL: pr.HTMLURL, IssueComments: len(issueComments)}
	if revision := strings.TrimSpace(pr.Head.SHA); revision != "" {
		report.SubjectRevision = revision
		report.RevisionSource = fmt.Sprintf("github-pull-request-head:%d", pr.Number)
	}
	findingOwnerByID := map[int64]string{}
	for _, comment := range reviewComments {
		if comment.InReplyToID != 0 {
			continue
		}
		if finding, ok, err := model.FindFindingMarker(comment.Body); err == nil && ok {
			findingOwnerByID[comment.ID] = finding.Agent
		}
	}
	resolvedByParent := map[int64]bool{}
	resolutionAgentByParent := map[int64]string{}
	resolutionRevisionByParent := map[int64]string{}
	resolutionSourceByParent := map[int64]string{}
	for _, comment := range reviewComments {
		reply, ok, err := model.FindFindingReplyMarker(comment.Body)
		if err != nil || !ok || !model.IsTerminalFindingStatus(reply.Status) {
			continue
		}
		if comment.InReplyToID == 0 {
			continue
		}
		// SPEC-003: a worker reply alone MUST NOT resolve a finding. Only a
		// terminal reply authored by the finding's owning review agent resolves
		// it, so a worker "fixed" reply keeps the finding blocking until the
		// review agent re-checks and replies under its own identity.
		owner, ok := findingOwnerByID[comment.InReplyToID]
		if !ok || owner == "" || reply.Agent != owner {
			continue
		}
		resolvedByParent[comment.InReplyToID] = true
		resolutionAgentByParent[comment.InReplyToID] = reply.Agent
		resolutionRevisionByParent[comment.InReplyToID] = strings.TrimSpace(comment.CommitID)
		resolutionSourceByParent[comment.InReplyToID] = fmt.Sprintf("github-pr-review-comment:%d", comment.ID)
	}
	for _, comment := range reviewComments {
		if rationale, ok, err := model.FindRationaleMarker(comment.Body); err == nil && ok {
			report.RationaleComments++
			report.Rationales = append(report.Rationales, reviewRationale{
				Process:            rationale.Process,
				Spec:               rationale.Spec,
				CommentID:          comment.ID,
				URL:                comment.HTMLURL,
				Agent:              rationale.Agent,
				AgentSessionID:     rationale.AgentSessionID,
				AgentSessionSource: rationale.AgentSessionSource,
			})
			report.Diagnostics = append(report.Diagnostics, artifactSessionDiagnostics("RATIONALE/"+rationale.Process, comment.HTMLURL, rationale.AgentSessionID, rationale.AgentSessionSource)...)
			continue
		}
		if reply, ok, err := model.FindFindingReplyMarker(comment.Body); err == nil && ok {
			report.FindingReplies = append(report.FindingReplies, reviewReply{
				Finding:            reply.Finding,
				CommentID:          comment.ID,
				URL:                comment.HTMLURL,
				Process:            reply.Process,
				Status:             reply.Status,
				Agent:              reply.Agent,
				AgentSessionID:     reply.AgentSessionID,
				AgentSessionSource: reply.AgentSessionSource,
			})
			report.Diagnostics = append(report.Diagnostics, artifactSessionDiagnostics("FINDING_REPLY/"+reply.Finding, comment.HTMLURL, reply.AgentSessionID, reply.AgentSessionSource)...)
			continue
		}
		if comment.InReplyToID != 0 {
			continue
		}
		finding, ok, err := model.FindFindingMarker(comment.Body)
		if err == nil && ok {
			item := reviewFinding{
				ID:                 finding.ID,
				CommentID:          comment.ID,
				URL:                comment.HTMLURL,
				Path:               firstNonEmpty(finding.Path, comment.Path),
				Line:               firstPositive(finding.Line, comment.Line),
				Severity:           finding.Severity,
				Status:             finding.Status,
				Process:            finding.Process,
				Spec:               finding.Spec,
				Agent:              finding.Agent,
				AgentSessionID:     finding.AgentSessionID,
				AgentSessionSource: finding.AgentSessionSource,
				Summary:            firstFindingSummary(comment.Body),
			}
			report.Diagnostics = append(report.Diagnostics, artifactSessionDiagnostics("FINDING/"+finding.ID, comment.HTMLURL, finding.AgentSessionID, finding.AgentSessionSource)...)
			if model.IsTerminalFindingStatus(item.Status) || resolvedByParent[comment.ID] {
				item.Status = "resolved"
				if resolver := resolutionAgentByParent[comment.ID]; resolver != "" {
					item.ResolvedByAgent = resolver
					item.SubjectRevision = resolutionRevisionByParent[comment.ID]
					item.RevisionSource = resolutionSourceByParent[comment.ID]
				} else {
					item.ResolvedByAgent = finding.Agent
					item.SubjectRevision = strings.TrimSpace(comment.CommitID)
					item.RevisionSource = fmt.Sprintf("github-pr-review-comment:%d", comment.ID)
				}
				report.ResolvedFindings = append(report.ResolvedFindings, item)
				continue
			}
			report.ActionableFindings = append(report.ActionableFindings, item)
			if blocksReview(item.Severity) {
				report.BlockingFindings = append(report.BlockingFindings, item)
			}
			continue
		}
		item := reviewFinding{
			ID:        fmt.Sprintf("comment-%d", comment.ID),
			CommentID: comment.ID,
			URL:       comment.HTMLURL,
			Path:      comment.Path,
			Line:      comment.Line,
			Severity:  findingSeverity(comment.Body),
			Status:    "open",
			Summary:   firstFindingSummary(comment.Body),
		}
		report.ActionableFindings = append(report.ActionableFindings, item)
		if blocksReview(item.Severity) {
			report.BlockingFindings = append(report.BlockingFindings, item)
		}
	}
	for _, s := range status.Statuses {
		// Commit statuses are returned for a requested SHA, but the status object
		// itself carries no revision. PR head context is an expectation, not a
		// provider-owned carrier fact, so status contexts remain untrusted.
		check := reviewCheck{Name: s.Context, State: s.State, URL: s.TargetURL, Source: "github-status-context"}
		if s.State == "success" {
			report.PassedChecks = append(report.PassedChecks, check)
		} else if s.State == "pending" {
			report.PendingChecks = append(report.PendingChecks, check)
		} else {
			report.FailedChecks = append(report.FailedChecks, check)
		}
	}
	for _, run := range checkRuns {
		check := reviewCheck{Name: run.Name, State: run.Status, Conclusion: run.Conclusion,
			URL: firstNonEmpty(run.DetailsURL, run.HTMLURL), SubjectRevision: strings.TrimSpace(run.HeadSHA),
			Trusted: strings.TrimSpace(run.HeadSHA) != "", Source: fmt.Sprintf("github-check-run:%d", run.ID)}
		if run.Status != "completed" {
			report.PendingChecks = append(report.PendingChecks, check)
			continue
		}
		switch run.Conclusion {
		case "success", "neutral", "skipped":
			report.PassedChecks = append(report.PassedChecks, check)
		default:
			report.FailedChecks = append(report.FailedChecks, check)
		}
	}
	sortReviewFindings(report.ActionableFindings)
	sortReviewFindings(report.BlockingFindings)
	sortReviewFindings(report.ResolvedFindings)
	report.OK = len(report.BlockingFindings) == 0 && len(report.FailedChecks) == 0 && len(report.PendingChecks) == 0
	return report
}

func renderReviewSyncComment(id, agent string, session writerSession, scope, prURL string, report reviewSyncReport) (string, error) {
	status := "done"
	if !report.OK {
		status = "blocked"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", model.RenderMarker("REVIEW", id, 1))
	b.WriteString(model.RenderHeader("REVIEW", id, model.BodyOptions{
		Agent:              agent,
		AgentSessionID:     session.ID,
		AgentSessionSource: session.Source,
		SubjectRevision:    report.SubjectRevision,
		Status:             status,
		Scope:              scope,
		Links:              map[string][]string{"Implement Issue": {"N/A"}, "PR": {prURL}},
	}))
	b.WriteString("\n\n## Review Sync Summary\n\n")
	fmt.Fprintf(&b, "- PR: %s\n", prURL)
	fmt.Fprintf(&b, "- Rationale comments: %d\n", report.RationaleComments)
	fmt.Fprintf(&b, "- PR issue comments: %d\n", report.IssueComments)
	fmt.Fprintf(&b, "- Actionable findings: %d\n", len(report.ActionableFindings))
	fmt.Fprintf(&b, "- Blocking findings: %d\n", len(report.BlockingFindings))
	fmt.Fprintf(&b, "- Resolved findings: %d\n", len(report.ResolvedFindings))
	fmt.Fprintf(&b, "- Failed checks: %d\n", len(report.FailedChecks))
	fmt.Fprintf(&b, "- Pending checks: %d\n", len(report.PendingChecks))
	b.WriteString("\n## Actionable Findings\n\n")
	if len(report.ActionableFindings) == 0 {
		b.WriteString("- None.\n")
	} else {
		for _, finding := range report.ActionableFindings {
			fmt.Fprintf(&b, "- %s %s status=%s owner=%s %s:%d %s %s\n", finding.Severity, finding.ID, finding.Status, ownerOrUnknown(finding.Agent), finding.Path, finding.Line, finding.URL, finding.Summary)
		}
	}
	b.WriteString("\n## Blocking Findings\n\n")
	if len(report.BlockingFindings) == 0 {
		b.WriteString("- None.\n")
	} else {
		for _, finding := range report.BlockingFindings {
			fmt.Fprintf(&b, "- %s %s status=%s owner=%s %s:%d %s %s\n", finding.Severity, finding.ID, finding.Status, ownerOrUnknown(finding.Agent), finding.Path, finding.Line, finding.URL, finding.Summary)
		}
	}
	b.WriteString("\n## Resolved Findings\n\n")
	if len(report.ResolvedFindings) == 0 {
		b.WriteString("- None.\n")
	} else {
		for _, finding := range report.ResolvedFindings {
			fmt.Fprintf(&b, "- %s %s status=%s owner=%s resolved_by=%s %s:%d %s %s\n", finding.Severity, finding.ID, finding.Status, ownerOrUnknown(finding.Agent), ownerOrUnknown(finding.ResolvedByAgent), finding.Path, finding.Line, finding.URL, finding.Summary)
		}
	}
	b.WriteString("\n## Finding Replies\n\n")
	if len(report.FindingReplies) == 0 {
		b.WriteString("- None.\n")
	} else {
		for _, reply := range report.FindingReplies {
			fmt.Fprintf(&b, "- %s status=%s owner=%s %s\n", reply.Finding, reply.Status, ownerOrUnknown(reply.Agent), reply.URL)
		}
	}
	b.WriteString("\n## Rationale\n\n")
	if len(report.Rationales) == 0 {
		b.WriteString("- None.\n")
	} else {
		for _, rationale := range report.Rationales {
			fmt.Fprintf(&b, "- %s %s owner=%s %s\n", rationale.Process, rationale.Spec, ownerOrUnknown(rationale.Agent), rationale.URL)
		}
	}
	b.WriteString("\n## Checks\n\n")
	writeReviewChecks(&b, "Failed", report.FailedChecks)
	writeReviewChecks(&b, "Pending", report.PendingChecks)
	writeReviewChecks(&b, "Passed", report.PassedChecks)
	b.WriteString("\n## PROCESS Evidence Observation\n\n")
	b.WriteString("This review-sync projection is observational and MUST NOT be treated as final readiness; final verify re-collects active SPEC and authoritative evidence.\n\n")
	if len(report.ProcessEvidence) == 0 {
		b.WriteString("- None.\n")
	} else {
		for _, process := range report.ProcessEvidence {
			fmt.Fprintf(&b, "- %s\n", process.Summary())
		}
	}
	b.WriteString("\n## Verdict\n\n")
	if report.OK {
		b.WriteString("Review sync passed.\n")
	} else {
		b.WriteString("Review sync blocked.\n")
	}
	return b.String(), nil
}

func ownerOrUnknown(agent string) string {
	if strings.TrimSpace(agent) == "" {
		return "unknown"
	}
	return agent
}

func writeReviewChecks(b *strings.Builder, label string, checks []reviewCheck) {
	fmt.Fprintf(b, "### %s\n\n", label)
	if len(checks) == 0 {
		b.WriteString("- None.\n")
		return
	}
	for _, check := range checks {
		fmt.Fprintf(b, "- %s state=%s conclusion=%s %s\n", check.Name, check.State, check.Conclusion, check.URL)
	}
}

func sortReviewFindings(findings []reviewFinding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].CommentID == findings[j].CommentID {
			return findings[i].ID < findings[j].ID
		}
		return findings[i].CommentID < findings[j].CommentID
	})
}

func blocksReview(severity string) bool {
	switch model.NormalizeFindingSeverity(severity) {
	case "P0", "P1":
		return true
	default:
		return false
	}
}

func findingSeverity(body string) string {
	body = strings.ToUpper(body)
	for _, severity := range []string{"P0", "P1", "P2"} {
		if strings.Contains(body, severity) {
			return severity
		}
	}
	return "P2"
}

func firstFindingSummary(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "<!--") {
			continue
		}
		if isFindingMetadataLine(line) {
			continue
		}
		if len(line) > 120 {
			return line[:120] + "..."
		}
		return line
	}
	return ""
}

func isFindingMetadataLine(line string) bool {
	for _, prefix := range []string{"Agent:", "Agent Session ID:", "Agent Session Source:", "Type:", "ID:", "Finding:", "Severity:", "Status:", "Process:", "Spec:", "Spec Comment:"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
