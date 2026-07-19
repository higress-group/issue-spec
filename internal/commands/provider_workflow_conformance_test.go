package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/workflow"
)

func TestGitHubSearchContinuesThroughExistingPRWorkflow(t *testing.T) {
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Setenv("ISSUE_SPEC_TOKEN", "github-workflow-token")
	profile := auth.BuiltinGitHubProfile("github.com")
	profile.Name = "github-workflow-conformance"
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}

	processBody := codeChangeLinkProcessBody(t, "PROCESS-008", "N/A")
	updatedBody := ""
	backend := &fakeGitHubIssueSearchBackend{
		fakeGitHubBackend: fakeGitHubBackend{
			getPullRequest: func(context.Context, string, int) (github.PullRequest, error) {
				return github.PullRequest{Number: 7, HTMLURL: "https://github.com/acme/widgets/pull/7"}, nil
			},
			listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
				return []github.Comment{{ID: 17, Body: processBody}}, nil
			},
			updateComment: func(_ context.Context, _ string, id int64, body string) (github.Comment, error) {
				if id != 17 {
					t.Fatalf("comment id = %d, want 17", id)
				}
				updatedBody = body
				return github.Comment{ID: id, Body: body}, nil
			},
		},
		page: github.IssueSearchPage{Items: []github.IssueSearchResult{{
			Organization: "acme", Repository: "widgets", Number: 9, State: "open",
			Title: "Existing workflow", URL: "https://github.com/acme/widgets/issues/9",
		}}, Page: 1, PerPage: 10, Total: 1},
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.profileName = profile.Name
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
		return backend, nil
	}
	app.newNativeSearchProvider = func(auth.Profile, string) (nativeSearchProvider, error) {
		t.Fatal("GitHub search selected the self-hosted search backend")
		return nil, nil
	}
	app.newNativeCodeChangeBackend = func(auth.Profile, string) (nativeCodeChangeBackend, error) {
		t.Fatal("GitHub PR workflow selected the self-hosted code-change backend")
		return nil, nil
	}

	if code := app.runSearch(t.Context(), []string{"issues", "--repo", "acme/widgets", "--query", "workflow"}); code != 0 || errOut.Len() != 0 ||
		!strings.Contains(out.String(), "issue: #9") {
		t.Fatalf("search exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := app.runPRLinkProcess(t.Context(), []string{"--repo", "acme/widgets", "--issue", "9", "--process", "PROCESS-008", "--pr", "7", "--json"}); code != 0 || errOut.Len() != 0 || !strings.Contains(updatedBody, "- PR: https://github.com/acme/widgets/pull/7") {
		t.Fatalf("link exit=%d updated=%q stdout=%q stderr=%q", code, updatedBody, out.String(), errOut.String())
	}
}

func TestSelfHostedAttachLinkContinuesThroughProviderNeutralGates(t *testing.T) {
	profile := setupCodeChangeProfile(t)
	backend := newFakeCodeChangeBackend()
	backend.upsert = func(input github.NativeUpsertReferenceInput) (github.NativeReference, error) {
		attached := backend.reference(input, 1)
		backend.references = []github.NativeReference{attached}
		return attached, nil
	}
	provider := newFakeNavigationProvider()
	app, out, errOut := setupCodeChangeApp(t, profile, backend, provider)
	if code := app.runCodeChange(t.Context(), []string{"attach", "--repo", "acme/widgets", "--implement", "9",
		"--change-id", "change-42", "--revision", "head-abc", "--json"}); code != 0 || errOut.Len() != 0 {
		t.Fatalf("attach exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if provider.snapshotCalls != 1 || provider.mutationCalls != 0 || len(backend.references) != 1 {
		t.Fatalf("snapshot=%d mutation=%d references=%+v", provider.snapshotCalls, provider.mutationCalls, backend.references)
	}
	attached := backend.references[0]
	reference := codereview.Reference{ProviderKey: attached.ProviderKey,
		ExternalRepository: attached.ExternalRepositoryID, ChangeID: attached.ExternalID}
	if reference != provider.requests[0].Reference || provider.requests[0].SubjectRevision != "head-abc" {
		t.Fatalf("attached reference=%+v snapshot request=%+v", reference, provider.requests[0])
	}

	processBody := codeChangeLinkProcessBody(t, "PROCESS-008", "N/A")
	currentBody, version := processBody, int64(3)
	backend.issueBackend = conditionalTransitionBackend{fakeGitHubBackend: fakeGitHubBackend{
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 17, Body: currentBody}}, nil
		}}, observe: func(context.Context, string, int64) (github.CommentRepresentation, error) {
		return github.CommentRepresentation{Comment: github.Comment{ID: 17, Body: currentBody},
			RepresentationVersion: version, Guarantee: github.CommentMutationStrictConditional}, nil
	}, update: func(_ context.Context, _ string, _ int64, expected int64, body string) (github.CommentRepresentation, error) {
		if expected != version {
			t.Fatalf("expected version = %d, current = %d", expected, version)
		}
		currentBody, version = body, version+1
		return github.CommentRepresentation{Comment: github.Comment{ID: 17, Body: body},
			RepresentationVersion: version, Guarantee: github.CommentMutationStrictConditional}, nil
	}}
	out.Reset()
	errOut.Reset()
	if code := app.runCodeChange(t.Context(), []string{"link-process", "--repo", "acme/widgets", "--implement", "9",
		"--process", "PROCESS-008", "--expected-version", "3", "--json"}); code != 0 || errOut.Len() != 0 ||
		!strings.Contains(currentBody, "- PR: "+attached.CanonicalURL) {
		t.Fatalf("link exit=%d body=%q stdout=%q stderr=%q", code, currentBody, out.String(), errOut.String())
	}

	now := time.Now().UTC()
	ledger := &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
		Reference: reference, SubjectRevision: "head-abc", CapturedAt: now}}
	native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: attached.RepresentationVersion, SubjectRevision: "head-abc",
		Policy: coreevidence.NativePolicy{Requirements: []coreevidence.NativeRequirement{
			{Kind: codereview.EvidenceReview, Freshness: time.Hour},
			{Kind: codereview.EvidenceCheck, Freshness: time.Hour},
		}}, Provider: ledger, IssueID: backend.issueID, OrgID: backend.scope.OrgID, RepoID: backend.scope.RepoID}}
	app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
	if _, hosted, err := app.externalGate(t.Context(), "github.com", "attach-secret", "acme/widgets", 9,
		"code_change", "head-abc", coreevidence.GateVerify); !hosted || err == nil {
		t.Fatalf("attach-time navigation fact unexpectedly satisfied trusted verify gate: hosted=%t err=%v", hosted, err)
	}

	review := testEvidenceRecord("review-ledger", codereview.EvidenceReview, "resolved", "head-abc", now)
	review.CanonicalURL = "https://code.example/reviews/1"
	check := testEvidenceRecord("check-ledger", codereview.EvidenceCheck, "passed", "head-abc", now)
	check.Name = "unit"
	ledger.snapshot.Records = []codereview.EvidenceRecord{check}
	zeroFindingGate, hosted, err := app.externalGate(t.Context(), "github.com", "attach-secret", "acme/widgets", 9,
		"code_change", "head-abc", coreevidence.GateVerify)
	if err != nil || !hosted || !zeroFindingGate.Evaluation.Passed || !zeroFindingGate.ReviewCompletionPolicy.Required ||
		zeroFindingGate.ReviewCompletionPolicy.Freshness != time.Hour || len(zeroFindingGate.Consumption.Bindings) != 0 {
		t.Fatalf("zero-finding verify gate=%+v hosted=%t err=%v", zeroFindingGate, hosted, err)
	}
	open := review
	open.State, open.Severity = "open", "P1"
	ledger.snapshot.Records = []codereview.EvidenceRecord{open, check}
	if _, _, err := app.externalGate(t.Context(), "github.com", "attach-secret", "acme/widgets", 9,
		"code_change", "head-abc", coreevidence.GateVerify); err == nil || !strings.Contains(err.Error(), "blocking_review") {
		t.Fatalf("open authoritative finding did not block verify: %v", err)
	}
	ledger.snapshot.Records = []codereview.EvidenceRecord{review, check}
	gate, hosted, err := app.externalGate(t.Context(), "github.com", "attach-secret", "acme/widgets", 9,
		"code_change", "head-abc", coreevidence.GateVerify)
	if err != nil || !hosted || !gate.Evaluation.Passed || gate.Consumption.ProviderKey != reference.ProviderKey ||
		gate.Consumption.ExternalRepository != reference.ExternalRepository || gate.Consumption.ChangeID != reference.ChangeID ||
		gate.Consumption.ReferenceVersion != attached.RepresentationVersion || gate.Consumption.SubjectRevision != "head-abc" {
		t.Fatalf("verify gate=%+v hosted=%t err=%v", gate, hosted, err)
	}
	reviewBody, err := renderExternalReviewSyncComment("REVIEW-008", "Independent Reviewer", writerSession{},
		"provider conformance", gate)
	if err != nil || !strings.Contains(reviewBody, `"process_id": "PROCESS-001"`) ||
		!strings.Contains(reviewBody, `"subject_revision":"head-abc"`) {
		t.Fatalf("review body error=%v body=%q", err, reviewBody)
	}

	archiveProvider := &commandEvidenceProvider{capabilities: []codereview.Capability{codereview.CapabilityChangeCreate},
		mutation: codereview.MutationResult{Reference: codereview.Reference{ProviderKey: reference.ProviderKey,
			ExternalRepository: reference.ExternalRepository, ChangeID: "archive-7"},
			CanonicalURL: "https://code.example/archive/7", ExternalID: "archive-7"}}
	created, err := createExternalArchiveChange(t.Context(), archiveProvider, native, native.target, codereview.MutationRequest{
		Kind:      codereview.MutationCreateChange,
		Reference: codereview.Reference{ProviderKey: reference.ProviderKey, ExternalRepository: reference.ExternalRepository},
		Title:     "Archive", HeadRevision: "archive-head", BaseRevision: "archive-base",
	})
	if err != nil || native.upserts != 1 || created.Reference.ProviderKey != reference.ProviderKey ||
		created.Reference.ExternalRepository != reference.ExternalRepository {
		t.Fatalf("archive result=%+v upserts=%d err=%v", created, native.upserts, err)
	}
}

func TestGeneratedWorkflowAssetsDescribeSameBackendSplit(t *testing.T) {
	root := t.TempDir()
	if _, err := writeWorkflowArtifacts(root, "owner/repo", "codex,claude", "both"); err != nil {
		t.Fatal(err)
	}
	workflowSkill := readTestFile(t, root+"/.agents/skills/issue-spec-workflow/SKILL.md")
	applyCommand := readTestFile(t, root+"/.claude/commands/issue-spec/apply.md")
	reviewCommand := readTestFile(t, root+"/.claude/commands/issue-spec/review.md")
	verifyCommand := readTestFile(t, root+"/.claude/commands/issue-spec/verify.md")
	archiveCommand := readTestFile(t, root+"/.claude/commands/issue-spec/archive.md")
	checks := []struct {
		name    string
		content string
		wants   []string
	}{
		{"workflow", workflowSkill, []string{"search issues", "GitHub-backed workflows keep the existing `pr link-process`",
			"code-change attach", "code-change link-process", "review sync", "zero findings", "code-change rationale", "fresh REVIEW completion", "Do not call a GitHub PR endpoint",
			"persists and reloads provider facts", "exact-current completion stamp", "finding-backed consumed binding retained only for legacy compatibility"}},
		{"review", reviewCommand, []string{"On GitHub add --pr <number>; on a self-hosted profile omit --pr and add --revision <exact-head>",
			"Sync authoritatively captures current rationale", "one stable done REVIEW completion even with zero findings"}},
		{"apply", applyCommand, []string{"following the backend-appropriate routing in issue-spec-workflow",
			"authoritative final sync by following issue-spec-review",
			"After that sync, explicitly link the REVIEW to its review PROCESS, every covered change-bearing PROCESS, and every covered active SPEC",
			"Follow issue-spec-workflow for the backend-appropriate rationale command",
			"Each owning worker authors its own rationale under that worker's --agent"}},
		{"verify", verifyCommand, []string{"backend-appropriate rationale and REVIEW completion evidence",
			"Status forecast and final verify use the same authoritative validator",
			"The validator owns exact identity, revision, freshness, and legacy compatibility"}},
		{"archive", archiveCommand, []string{"Archive may read an existing required REVIEW completion when implementation merge policy requires it",
			"Archive never creates, updates, or refreshes REVIEW or adds archive-specific review state"}},
	}
	for _, check := range checks {
		for _, want := range check.wants {
			if !strings.Contains(check.content, want) {
				t.Fatalf("generated %s workflow missing %q:\n%s", check.name, want, check.content)
			}
		}
		if strings.Contains(check.content, "remain in GitHub issue-native storage") {
			t.Fatalf("generated %s workflow retained GitHub-only storage footer:\n%s", check.name, check.content)
		}
	}
	for name, content := range map[string]string{
		"review":  reviewCommand,
		"apply":   applyCommand,
		"verify":  verifyCommand,
		"archive": archiveCommand,
	} {
		for _, forbidden := range []string{
			"provider facts",
			"completion stamp",
			"finding-backed consumed",
			"native-ledger",
			"append-only Issue Backend comment",
			"provider/repository/change/version/revision identity",
			"repository freshness",
			"code-change attach",
			"code-change link-process",
			"code-change rationale",
			"code_change",
			"archive_change",
		} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("generated %s workflow duplicates workflow-owned backend protocol %q:\n%s", name, forbidden, content)
			}
		}
	}

	providerRoot := t.TempDir()
	provider := workflow.ProviderPlan{ProviderKey: "code.example", DisplayName: "Example Code",
		CodeChangeLabel: "change", EvidenceSnapshot: true}
	if _, err := writeWorkflowArtifactsWithProvider(providerRoot, "owner/repo", "codex", "skills", provider); err != nil {
		t.Fatal(err)
	}
	providerSkill := readTestFile(t, providerRoot+"/.agents/skills/issue-spec-code-provider/SKILL.md")
	attachAt, linkAt, verifyAt, reviewAt, rationaleAt := strings.Index(providerSkill, "code-change attach --repo owner/repo"),
		strings.Index(providerSkill, "code-change link-process --repo owner/repo"), strings.Index(providerSkill, "Before verification gates"),
		strings.Index(providerSkill, "review sync --repo owner/repo"),
		strings.Index(providerSkill, "code-change rationale --repo owner/repo")
	if attachAt < 0 || linkAt <= attachAt || verifyAt <= linkAt || reviewAt <= verifyAt || rationaleAt <= reviewAt ||
		!strings.Contains(providerSkill, "fresh exact-current REVIEW completion") ||
		!strings.Contains(providerSkill, "zero findings") ||
		!strings.Contains(providerSkill, "do not substitute GitHub PR endpoints") {
		t.Fatalf("provider workflow does not preserve attach -> link -> verify -> rationale/backend split:\n%s", providerSkill)
	}
}

func TestProviderBridgeContractRequiresStableCurrentHeadSnapshot(t *testing.T) {
	raw, err := os.ReadFile("../../docs/self-hosting/bridges/code-provider-v1.md")
	if err != nil {
		t.Fatal(err)
	}
	contract := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{
		"For every evidence snapshot request",
		"MUST read the change's current HEAD",
		"MUST read it again after fact collection",
		"both observations equal the requested revision",
		"`revision_mismatch` and no snapshot",
		"no provider facts to persist",
	} {
		if !strings.Contains(contract, want) {
			t.Fatalf("provider bridge contract missing %q:\n%s", want, contract)
		}
	}
}

func TestCheckedInCodexWorkflowSkillsMatchGenerator(t *testing.T) {
	generatedRoot := t.TempDir()
	if _, err := writeWorkflowArtifacts(generatedRoot, "higress-group/issue-spec", "codex", "skills"); err != nil {
		t.Fatal(err)
	}
	projectRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range []string{"apply", "archive", "github", "propose", "review", "verify", "workflow"} {
		relative := filepath.Join(".agents", "skills", "issue-spec-"+skill, "SKILL.md")
		generated, err := os.ReadFile(filepath.Join(generatedRoot, relative))
		if err != nil {
			t.Fatal(err)
		}
		checkedIn, err := os.ReadFile(filepath.Join(projectRoot, relative))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(generated, checkedIn) {
			t.Fatalf("checked-in Codex workflow skill is stale: %s", relative)
		}
	}
}
