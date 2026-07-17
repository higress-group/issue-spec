package commands

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestExternalReviewSyncIsForcedAndIdempotent(t *testing.T) {
	app, native, comments, creates, updates, out, errOut := setupExternalReviewSyncCommand(t)
	t.Setenv(codexThreadIDEnv, "")
	args := []string{"--repo", "acme/widgets", "--hostname", "issues.test", "--implement", "9",
		"--revision", "head-abc", "--id", "REVIEW-101", "--agent", "Review Agent", "--agent-session", "review-session-7"}
	var firstCompletion externalReviewCompletion
	for run := 1; run <= 2; run++ {
		out.Reset()
		errOut.Reset()
		if code := app.runReviewSync(t.Context(), args); code != 0 {
			t.Fatalf("run %d exit=%d stdout=%q stderr=%q", run, code, out.String(), errOut.String())
		}
		completion, found, err := parseExternalReviewCompletion((*comments)[0].Body)
		if err != nil || !found {
			t.Fatalf("run %d completion found=%t err=%v body=%q", run, found, err, (*comments)[0].Body)
		}
		if run == 1 {
			firstCompletion = completion
			linked, _, linkErr := model.AddRelatedCommentLink((*comments)[0].Body,
				"https://issues.test/acme/widgets/issues/9#issuecomment-process")
			if linkErr != nil {
				t.Fatal(linkErr)
			}
			(*comments)[0].Body = linked
		} else if !completion.SynchronizedAt.After(firstCompletion.SynchronizedAt) {
			t.Fatalf("completion timestamp did not advance: first=%s second=%s", firstCompletion.SynchronizedAt, completion.SynchronizedAt)
		}
	}
	if *creates != 1 || *updates != 1 || len(*comments) != 1 || native.syncs != 2 || native.resolveCalls != 4 {
		t.Fatalf("creates=%d updates=%d comments=%d syncs=%d resolves=%d", *creates, *updates, len(*comments),
			native.syncs, native.resolveCalls)
	}
	parsed := model.ParseTypedComment((*comments)[0].Body)
	if parsed.Type != "REVIEW" || parsed.ID != "REVIEW-101" || parsed.Status != "done" ||
		parsed.Agent != "Review Agent" || parsed.AgentSessionID != "review-session-7" ||
		parsed.AgentSessionSource != agentSessionParamSource || parsed.SubjectRevision != "head-abc" ||
		strings.Count((*comments)[0].Body, externalReviewCompletionStart) != 1 ||
		!linksContainURL(parsed.Links["Related Comments"], "https://issues.test/acme/widgets/issues/9#issuecomment-process") ||
		!strings.Contains((*comments)[0].Body, `"subject_revision":"head-abc"`) {
		t.Fatalf("persisted REVIEW=%+v", parsed)
	}
}

func TestExternalReviewSyncZeroFindingsWritesCompletionWithoutReviewFacts(t *testing.T) {
	app, native, comments, creates, updates, _, errOut := setupExternalReviewSyncCommand(t)
	native.target.Provider = &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
		Reference: native.target.Reference, SubjectRevision: native.target.SubjectRevision, CapturedAt: time.Now().UTC()}}
	code := app.runReviewSync(t.Context(), []string{"--repo", "acme/widgets", "--hostname", "issues.test",
		"--implement", "9", "--revision", "head-abc", "--id", "REVIEW-101", "--agent", "Independent Reviewer"})
	if code != 0 || *creates != 1 || *updates != 0 || len(*comments) != 1 {
		t.Fatalf("exit=%d creates=%d updates=%d comments=%d stderr=%q", code, *creates, *updates, len(*comments), errOut.String())
	}
	body := (*comments)[0].Body
	if strings.Contains(body, consumedEvidenceStart) || !strings.Contains(body, "### Canonical Findings\n\n```json\n[]\n```") {
		t.Fatalf("zero-finding REVIEW invented review facts or consumed bindings: %q", body)
	}
	completion, found, err := parseExternalReviewCompletion(body)
	if err != nil || !found || completion.SubjectRevision != "head-abc" || completion.ReferenceVersion != 7 {
		t.Fatalf("completion=%+v found=%t err=%v", completion, found, err)
	}
}

func TestExternalReviewSyncFailedRetryLeavesExistingReviewByteStable(t *testing.T) {
	app, native, comments, creates, updates, _, errOut := setupExternalReviewSyncCommand(t)
	args := []string{"--repo", "acme/widgets", "--hostname", "issues.test", "--implement", "9",
		"--revision", "head-abc", "--id", "REVIEW-101"}
	if code := app.runReviewSync(t.Context(), args); code != 0 {
		t.Fatalf("initial sync exit=%d stderr=%q", code, errOut.String())
	}
	before := (*comments)[0].Body
	native.syncErr = errors.New("ledger persistence unavailable")
	errOut.Reset()
	if code := app.runReviewSync(t.Context(), args); code != 1 {
		t.Fatalf("retry exit=%d stderr=%q", code, errOut.String())
	}
	if *creates != 1 || *updates != 0 || len(*comments) != 1 || (*comments)[0].Body != before {
		t.Fatalf("failed retry mutated REVIEW: creates=%d updates=%d body_changed=%t", *creates, *updates, (*comments)[0].Body != before)
	}
}

func TestExternalReviewSyncDisplaysOnlyCanonicalLedgerFindings(t *testing.T) {
	app, _, comments, _, _, _, errOut := setupExternalReviewSyncCommand(t)
	if code := app.runReviewSync(t.Context(), []string{"--repo", "acme/widgets", "--hostname", "issues.test",
		"--implement", "9", "--revision", "head-abc", "--id", "REVIEW-101"}); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	body := (*comments)[0].Body
	for _, want := range []string{`"evidence_id": "review-ledger"`, `"finding_id": "FINDING-001"`,
		`"process_id": "PROCESS-001"`, `"spec_id": "SPEC-001"`, `"severity": "P2"`, `"state": "resolved"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("canonical finding field %q missing from %q", want, body)
		}
	}
	if strings.Contains(body, "PayloadDigest") || strings.Contains(body, "payload_digest") {
		t.Fatalf("provider payload detail leaked into REVIEW: %q", body)
	}
}

func TestExternalReviewSyncFailuresNeverMutateReview(t *testing.T) {
	tests := map[string]func(*app, *commandNativeEvidence, *commandEvidenceProvider){
		"authorization": func(app *app, _ *commandNativeEvidence, _ *commandEvidenceProvider) {
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) {
				return nil, errors.New("authorization denied")
			}
		},
		"registry": func(app *app, _ *commandNativeEvidence, _ *commandEvidenceProvider) {
			app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) {
				return nil, errors.New("operator registry malformed")
			}
		},
		"capability": func(_ *app, _ *commandNativeEvidence, provider *commandEvidenceProvider) {
			provider.capabilities = []codereview.Capability{codereview.CapabilityChangeComment}
		},
		"snapshot": func(_ *app, _ *commandNativeEvidence, provider *commandEvidenceProvider) {
			provider.snapshotErr = errors.New("snapshot unavailable")
		},
		"persistence": func(_ *app, native *commandNativeEvidence, _ *commandEvidenceProvider) {
			native.syncErr = errors.New("writer authorization denied")
		},
		"reference movement": func(_ *app, native *commandNativeEvidence, _ *commandEvidenceProvider) {
			moved := native.target
			moved.ReferenceVersion++
			native.targets = []coreevidence.NativeTarget{native.target, moved}
		},
	}
	for name, fail := range tests {
		t.Run(name, func(t *testing.T) {
			app, native, comments, creates, updates, _, errOut := setupExternalReviewSyncCommand(t)
			provider, err := app.lookupOperatorProvider(t.Context(), auth.Profile{}, "code.example")
			if err != nil {
				t.Fatal(err)
			}
			fail(app, native, provider.(*commandEvidenceProvider))
			code := app.runReviewSync(t.Context(), []string{"--repo", "acme/widgets", "--hostname", "issues.test",
				"--implement", "9", "--revision", "head-abc", "--id", "REVIEW-101"})
			if code != 1 || *creates != 0 || *updates != 0 || len(*comments) != 0 {
				t.Fatalf("exit=%d creates=%d updates=%d comments=%d stderr=%q", code, *creates, *updates,
					len(*comments), errOut.String())
			}
		})
	}
}

func setupExternalReviewSyncCommand(t *testing.T) (*app, *commandNativeEvidence, *[]github.Comment, *int, *int,
	*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	clearCommandAuthEnv(t)
	t.Setenv("ISSUE_SPEC_TOKEN", "review-token")
	t.Setenv(codereview.OperatorProvidersFileEnv, "")
	t.Chdir(t.TempDir())
	profile := auth.Profile{Name: "review-sync-test", Kind: auth.ProfileKindHosted, Hostname: "issues.test",
		APIURL: "https://issues.test/api/v3", NativeAPIURL: "https://issues.test/api/v1",
		WebURL: "https://issues.test", ServerInstanceID: "review-sync-test-instance"}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	provider := &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
		Reference: reference, SubjectRevision: "head-abc", CapturedAt: now}}
	ledger := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
		SubjectRevision: "head-abc", CapturedAt: now, Records: []codereview.EvidenceRecord{
			testEvidenceRecord("review-ledger", codereview.EvidenceReview, "resolved", "head-abc", now),
		}}
	native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
		SubjectRevision: "head-abc", Provider: &commandEvidenceProvider{snapshot: ledger}}}
	comments := []github.Comment{}
	creates, updates := 0, 0
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "rest", Kind: "rest", Host: profile.Hostname},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return append([]github.Comment(nil), comments...), nil
		},
		createComment: func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			creates++
			created := github.Comment{ID: 71, Body: body, HTMLURL: "https://issues.test/acme/widgets/issues/9#comment-71"}
			comments = append(comments, created)
			return created, nil
		},
		updateComment: func(_ context.Context, _ string, id int64, body string) (github.Comment, error) {
			updates++
			if len(comments) != 1 || comments[0].ID != id {
				return github.Comment{}, errors.New("unexpected REVIEW update target")
			}
			comments[0].Body = body
			return comments[0], nil
		},
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := newApp(strings.NewReader(""), out, errOut)
	app.profileName = profile.Name
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
	app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) { return provider, nil }
	return app, native, &comments, &creates, &updates, out, errOut
}

func TestBuildReviewSyncReportClassifiesRationaleFindingsAndChecks(t *testing.T) {
	rationale, err := model.RenderRationaleBody("Worker", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "why", "a.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 1, Body: rationale, Path: "a.go", Line: 10},
		{ID: 2, Body: "P1: fix this", Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
	}, []github.Comment{{ID: 3}}, github.CombinedStatus{Statuses: []github.Status{
		{Context: "license/cla", State: "success"},
		{Context: "ci/test", State: "failure"},
	}}, []github.CheckRun{
		{Name: "DCO", Status: "completed", Conclusion: "success"},
		{Name: "build", Status: "queued"},
	})
	if report.OK {
		t.Fatal("finding and failed/pending checks should block review sync")
	}
	if report.RationaleComments != 1 || len(report.ActionableFindings) != 1 || len(report.FailedChecks) != 1 || len(report.PendingChecks) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(report.BlockingFindings) != 1 {
		t.Fatalf("expected one blocking finding: %+v", report.BlockingFindings)
	}
	if report.ActionableFindings[0].Severity != "P1" {
		t.Fatalf("severity = %s", report.ActionableFindings[0].Severity)
	}
}

func TestBuildReviewSyncReportIncludesSessionMetadataDiagnostics(t *testing.T) {
	finding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: finding, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
	}, nil, github.CombinedStatus{}, nil)
	if len(report.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v", report.Diagnostics)
	}
	if report.Diagnostics[0].Code != "missing_session_metadata" || report.Diagnostics[0].Artifact != "FINDING/FINDING-001" {
		t.Fatalf("unexpected diagnostic: %+v", report.Diagnostics[0])
	}
}

func TestFirstFindingSummarySkipsSessionMetadata(t *testing.T) {
	body := `<!-- issue-spec:finding id=FINDING-001 severity=P1 process=PROCESS-001 spec=SPEC-001 status=open path=a.go line=1 version=1 -->
Agent: Review Agent
Agent Session ID: codex-session-123
Agent Session Source: CODEX_THREAD_ID
Type: FINDING
ID: FINDING-001

Fix the real bug.`
	if got := firstFindingSummary(body); got != "Fix the real bug." {
		t.Fatalf("summary = %q", got)
	}
}

func TestBuildReviewSyncReportP2FindingDoesNotBlock(t *testing.T) {
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: "P2: polish this before follow-up", Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
	}, nil, github.CombinedStatus{}, nil)
	if !report.OK {
		t.Fatalf("P2-only findings should not block review sync: %+v", report)
	}
	if len(report.ActionableFindings) != 1 || len(report.BlockingFindings) != 0 {
		t.Fatalf("unexpected finding classification: %+v", report)
	}
}

func TestBuildReviewSyncReportResolvedFindingReply(t *testing.T) {
	finding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this before merge.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := model.RenderFindingReplyBody("Review", "FINDING-001", "PROCESS-001", "resolved", "Re-checked; the fix satisfies the finding.")
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: finding, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
		{ID: 3, InReplyToID: 2, Body: reply, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r3"},
	}, nil, github.CombinedStatus{}, nil)
	if !report.OK {
		t.Fatalf("resolved finding should not block review sync: %+v", report)
	}
	if len(report.ActionableFindings) != 0 || len(report.BlockingFindings) != 0 || len(report.ResolvedFindings) != 1 {
		t.Fatalf("unexpected finding classification: %+v", report)
	}
}

func TestBuildReviewSyncReportRetainsProviderOwnedRevisions(t *testing.T) {
	finding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := model.RenderFindingReplyBody("Review", "FINDING-001", "PROCESS-001", "resolved", "Re-checked old revision.")
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4}, []github.PullRequestReviewComment{
		{ID: 2, Body: finding, CommitID: "finding-head", Path: "b.go", Line: 20},
		{ID: 3, InReplyToID: 2, Body: reply, CommitID: "reviewed-old-head"},
	}, nil, github.CombinedStatus{Statuses: []github.Status{{Context: "legacy", State: "success"}}}, []github.CheckRun{
		{ID: 9, Name: "unit", HeadSHA: "checked-new-head", Status: "completed", Conclusion: "success"},
	})
	if got := report.ResolvedFindings[0]; got.SubjectRevision != "reviewed-old-head" || got.RevisionSource != "github-pr-review-comment:3" {
		t.Fatalf("resolved finding revision = %+v", got)
	}
	if len(report.PassedChecks) != 2 {
		t.Fatalf("passed checks = %+v", report.PassedChecks)
	}
	for _, check := range report.PassedChecks {
		switch check.Name {
		case "legacy":
			if check.Trusted || check.SubjectRevision != "" {
				t.Fatalf("revisionless status context became trusted: %+v", check)
			}
		case "unit":
			if !check.Trusted || check.SubjectRevision != "checked-new-head" {
				t.Fatalf("check-run head not retained: %+v", check)
			}
		}
	}
}

func TestBuildReviewSyncReportWorkerReplyAloneDoesNotResolve(t *testing.T) {
	finding, err := model.RenderFindingBody("Review Agent", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this before merge.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	// A worker's own terminal reply must not clear a blocking finding; only the
	// owning review agent's re-check resolves it (SPEC-003).
	workerReply, err := model.RenderFindingReplyBody("Worker Agent", "FINDING-001", "PROCESS-001", "fixed", "Applied the fix.")
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: finding, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
		{ID: 3, InReplyToID: 2, Body: workerReply, HTMLURL: "https://github.com/o/r/pull/4#discussion_r3"},
	}, nil, github.CombinedStatus{}, nil)
	if report.OK {
		t.Fatalf("worker reply alone must keep the finding blocking: %+v", report)
	}
	if len(report.ResolvedFindings) != 0 || len(report.BlockingFindings) != 1 {
		t.Fatalf("unexpected finding classification: %+v", report)
	}
	if len(report.FindingReplies) != 1 || report.FindingReplies[0].Agent != "Worker Agent" {
		t.Fatalf("expected the worker fix reply to be exposed: %+v", report.FindingReplies)
	}
}

func TestBuildReviewSyncReportExposesRationaleOwner(t *testing.T) {
	rationale, err := model.RenderRationaleBodyWithSession("Worker Agent Gamma", "worker-session-9", "agent-session-parameter", "PROCESS-001", "SPEC-002", "https://github.com/o/r/issues/1#issuecomment-1", "This block implements owner exposure.", "a.go", 5)
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: rationale, Path: "a.go", Line: 5, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
	}, nil, github.CombinedStatus{}, nil)
	if report.RationaleComments != 1 || len(report.Rationales) != 1 {
		t.Fatalf("expected one exposed rationale: %+v", report.Rationales)
	}
	if report.Rationales[0].Agent != "Worker Agent Gamma" || report.Rationales[0].Process != "PROCESS-001" {
		t.Fatalf("rationale owner not recoverable: %+v", report.Rationales[0])
	}
}

func TestBuildReviewSyncReportExposesLogicalOwners(t *testing.T) {
	finding, err := model.RenderFindingBody("Review Agent Alpha", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this before merge.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	workerReply, err := model.RenderFindingReplyBody("Worker Agent Beta", "FINDING-001", "PROCESS-001", "fixed", "Applied the fix in the latest patch.")
	if err != nil {
		t.Fatal(err)
	}
	reviewResolution, err := model.RenderFindingReplyBody("Review Agent Alpha", "FINDING-001", "PROCESS-001", "resolved", "Re-checked the diff; the fix satisfies the finding.")
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: finding, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
		{ID: 3, InReplyToID: 2, Body: workerReply, HTMLURL: "https://github.com/o/r/pull/4#discussion_r3"},
		{ID: 4, InReplyToID: 2, Body: reviewResolution, HTMLURL: "https://github.com/o/r/pull/4#discussion_r4"},
	}, nil, github.CombinedStatus{}, nil)

	if len(report.ResolvedFindings) != 1 {
		t.Fatalf("expected one resolved finding, got %+v", report.ResolvedFindings)
	}
	resolved := report.ResolvedFindings[0]
	if resolved.Agent != "Review Agent Alpha" {
		t.Fatalf("expected finding owner Review Agent Alpha, got %q", resolved.Agent)
	}
	if resolved.ResolvedByAgent != "Review Agent Alpha" {
		t.Fatalf("expected resolution owner Review Agent Alpha, got %q", resolved.ResolvedByAgent)
	}
	if len(report.FindingReplies) != 2 {
		t.Fatalf("expected two finding replies, got %+v", report.FindingReplies)
	}
	if report.FindingReplies[0].Agent != "Worker Agent Beta" {
		t.Fatalf("expected fix-reply owner Worker Agent Beta, got %q", report.FindingReplies[0].Agent)
	}
	if report.FindingReplies[1].Agent != "Review Agent Alpha" {
		t.Fatalf("expected resolution-reply owner Review Agent Alpha, got %q", report.FindingReplies[1].Agent)
	}
}

func TestBuildReviewSyncReportDoesNotResolveDuplicateFindingIDAcrossThreads(t *testing.T) {
	firstFinding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this first issue.", "open", "a.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	secondFinding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-002", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this second issue.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := model.RenderFindingReplyBody("Review", "FINDING-001", "PROCESS-001", "resolved", "Re-checked only the first thread.")
	if err != nil {
		t.Fatal(err)
	}

	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: firstFinding, Path: "a.go", Line: 10, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
		{ID: 3, Body: secondFinding, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r3"},
		{ID: 4, InReplyToID: 2, Body: reply, Path: "a.go", Line: 10, HTMLURL: "https://github.com/o/r/pull/4#discussion_r4"},
	}, nil, github.CombinedStatus{}, nil)

	if report.OK {
		t.Fatalf("second duplicate finding should still block review sync: %+v", report)
	}
	if len(report.ResolvedFindings) != 1 || report.ResolvedFindings[0].CommentID != 2 {
		t.Fatalf("unexpected resolved findings: %+v", report.ResolvedFindings)
	}
	if len(report.BlockingFindings) != 1 || report.BlockingFindings[0].CommentID != 3 {
		t.Fatalf("unexpected blocking findings: %+v", report.BlockingFindings)
	}
}

func TestRenderReviewSyncComment(t *testing.T) {
	const head = "0123456789abcdef0123456789abcdef01234567"
	body, err := renderReviewSyncComment("REVIEW-001", "Coordinator", writerSession{}, "pr-review", "https://github.com/o/r/pull/4", reviewSyncReport{
		OK:                true,
		PR:                4,
		PRURL:             "https://github.com/o/r/pull/4",
		SubjectRevision:   head,
		RevisionSource:    "github-pull-request-head:4",
		RationaleComments: 2,
		PassedChecks:      []reviewCheck{{Name: "DCO", State: "completed", Conclusion: "success"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Type: REVIEW", "ID: REVIEW-001", "Status: done", "Review sync passed", "DCO", "PROCESS Evidence Observation", "MUST NOT be treated as final readiness"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	parsed := model.ParseTypedComment(body)
	if parsed.SubjectRevision != head {
		t.Fatalf("subject revision = %q, want %q", parsed.SubjectRevision, head)
	}
}

func TestBuildReviewSyncReportCapturesAuthoritativePullRequestHead(t *testing.T) {
	pr := github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}
	pr.Head.SHA = "0123456789abcdef0123456789abcdef01234567"
	report := buildReviewSyncReport(pr, nil, nil, github.CombinedStatus{}, nil)
	if report.SubjectRevision != pr.Head.SHA || report.RevisionSource != "github-pull-request-head:4" {
		t.Fatalf("subject revision was not captured from PR head: %+v", report)
	}
}

func TestCreateReviewFindingIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := &fakeReviewClient{
		files: []github.PullRequestFile{{
			Filename: "internal/foo.go",
			Patch: `@@ -1,2 +1,3 @@
 package foo
+var X = 1
`,
		}},
		pr: github.PullRequest{Number: 7},
	}
	client.pr.Head.SHA = "abc123"
	result, err := createReviewFinding(ctx, client, "o/r", 7, "internal/foo.go", 2, "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Review Agent", writerSession{}, "Fix this.")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.CommentID == 0 || result.Severity != "P1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	result, err = createReviewFinding(ctx, client, "o/r", 7, "internal/foo.go", 2, "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Review Agent", writerSession{}, "Fix this.")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created {
		t.Fatalf("expected idempotent existing result: %+v", result)
	}
}

func TestCreateReviewFindingWithGHBackendUsesPatchLines(t *testing.T) {
	ctx := context.Background()
	runner := &commandSequenceRunner{results: []github.ExternalCLIResult{
		{Stdout: []byte(`[{"filename":"internal/foo.go","patch":"@@ -1,2 +1,3 @@\n package foo\n+var X = 1\n"}]`)},
		{Stdout: []byte(`[]`)},
		{Stdout: []byte(`{"number":7,"html_url":"https://github.com/o/r/pull/7","head":{"sha":"abc123","ref":"feature"},"base":{"ref":"main"}}`)},
		{Stdout: []byte(`{"id":100,"html_url":"https://github.com/o/r/pull/7#discussion_r100","body":"created","path":"internal/foo.go","line":2,"commit_id":"abc123"}`)},
	}}
	client := newCommandTestGHBackend(t, runner)

	result, err := createReviewFinding(ctx, client, "o/r", 7, "internal/foo.go", 2, "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Review Agent", writerSession{}, "Fix this.")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.CommentID != 100 || result.Severity != "P1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got, want := len(runner.commands), 4; got != want {
		t.Fatalf("gh commands = %d, want %d", got, want)
	}
}

func TestReplyReviewFindingIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := &fakeReviewClient{
		comments: []github.PullRequestReviewComment{{
			ID:      10,
			HTMLURL: "https://github.com/o/r/pull/7#discussion_r10",
			Body:    "P1: fix this",
			Path:    "internal/foo.go",
			Line:    2,
		}},
	}
	result, err := replyReviewFinding(ctx, client, "o/r", 7, 10, "FINDING-001", "PROCESS-001", "resolved", "Worker Agent", writerSession{}, "Fixed.")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.CommentID == 0 || result.ParentCommentID != 10 {
		t.Fatalf("unexpected result: %+v", result)
	}
	result, err = replyReviewFinding(ctx, client, "o/r", 7, 10, "FINDING-001", "PROCESS-001", "resolved", "Worker Agent", writerSession{}, "Fixed.")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created {
		t.Fatalf("expected idempotent existing reply: %+v", result)
	}
}

func TestReplyReviewFindingAllowsOwningReviewerAfterWorker(t *testing.T) {
	ctx := context.Background()
	client := &fakeReviewClient{comments: []github.PullRequestReviewComment{{
		ID: 10, HTMLURL: "https://github.com/o/r/pull/7#discussion_r10", Body: "P1: fix this",
		Path: "internal/foo.go", Line: 2,
	}}}
	worker, err := replyReviewFinding(ctx, client, "o/r", 7, 10, "FINDING-001", "PROCESS-001", "resolved", "Worker Agent", writerSession{}, "Fixed.")
	if err != nil || !worker.Created {
		t.Fatalf("worker reply = %+v, %v", worker, err)
	}
	reviewer, err := replyReviewFinding(ctx, client, "o/r", 7, 10, "FINDING-001", "PROCESS-001", "resolved", "Review Agent", writerSession{}, "Re-checked.")
	if err != nil || !reviewer.Created || reviewer.CommentID == worker.CommentID {
		t.Fatalf("reviewer reply = %+v, %v", reviewer, err)
	}
	reviewer, err = replyReviewFinding(ctx, client, "o/r", 7, 10, "FINDING-001", "PROCESS-001", "resolved", "Review Agent", writerSession{}, "Re-checked.")
	if err != nil || reviewer.Created {
		t.Fatalf("idempotent reviewer reply = %+v, %v", reviewer, err)
	}
}

type fakeReviewClient struct {
	pr       github.PullRequest
	files    []github.PullRequestFile
	comments []github.PullRequestReviewComment
}

func (f *fakeReviewClient) GetPullRequest(context.Context, string, int) (github.PullRequest, error) {
	return f.pr, nil
}

func (f *fakeReviewClient) ListPullRequestFiles(context.Context, string, int) ([]github.PullRequestFile, error) {
	return f.files, nil
}

func (f *fakeReviewClient) ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error) {
	return f.comments, nil
}

func (f *fakeReviewClient) CreatePullRequestReviewComment(_ context.Context, _ string, _ int, body, commitID, path string, line int, side string) (github.PullRequestReviewComment, error) {
	if commitID == "" || path == "" || line == 0 || side != "RIGHT" {
		panic("invalid create review comment args")
	}
	marker, ok, err := model.FindFindingMarker(body)
	if err != nil || !ok || marker.ID != "FINDING-001" || marker.Severity != "P1" {
		panic("missing finding marker")
	}
	comment := github.PullRequestReviewComment{
		ID:       int64(len(f.comments) + 1),
		HTMLURL:  "https://github.com/o/r/pull/7#discussion_r1",
		Body:     body,
		Path:     path,
		Line:     line,
		CommitID: commitID,
	}
	f.comments = append(f.comments, comment)
	return comment, nil
}

func (f *fakeReviewClient) ReplyPullRequestReviewComment(_ context.Context, _ string, prNumber int, parentCommentID int64, body string) (github.PullRequestReviewComment, error) {
	if prNumber != 7 {
		panic("invalid pull request number")
	}
	marker, ok, err := model.FindFindingReplyMarker(body)
	if err != nil || !ok || marker.Finding != "FINDING-001" || marker.Status != "resolved" {
		panic("missing finding reply marker")
	}
	comment := github.PullRequestReviewComment{
		ID:          int64(len(f.comments) + 1),
		InReplyToID: parentCommentID,
		HTMLURL:     "https://github.com/o/r/pull/7#discussion_r2",
		Body:        body,
	}
	f.comments = append(f.comments, comment)
	return comment, nil
}
