package templates

import (
	"strings"
	"testing"
)

func TestIssueSpecSkillAndCommandTemplates(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	if got, want := len(skills), 7; got != want {
		t.Fatalf("skills = %d, want %d", got, want)
	}
	if !strings.Contains(skills[0].Content, `generatedBy: "issue-spec"`) {
		t.Fatalf("skill missing generatedBy:\n%s", skills[0].Content)
	}

	commands := IssueSpecCommandContents("owner/repo")
	if got, want := len(commands), 5; got != want {
		t.Fatalf("commands = %d, want %d", got, want)
	}
	if commands[0].ID != "propose" {
		t.Fatalf("first command ID = %q, want propose", commands[0].ID)
	}
	if !strings.Contains(commands[0].Body, "issue-spec issue create proposal --repo owner/repo") {
		t.Fatalf("command body missing repo-specific issue-spec usage:\n%s", commands[0].Body)
	}
}

func TestIssueSpecSkillsDocumentSafeWorkflowAndProcessEvidence(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillContent(t, skills, "issue-spec-workflow")
	for _, want := range []string{
		"--gate <proposal|design|implement|final|archive>",
		"comment transition --id <id> --to <status>",
		"--allow-nonatomic", "--expected-digest", "atomic: false",
		"workflow reconcile --plan <plan.json> --checkpoint <checkpoint.json>",
		"doctor agent --repo owner/repo --operation <operation>",
		"operator-owned short-lived issuer", "legacy_long_lived",
		"change-bearing, review, verification, orchestration, or external",
		"matching GitHub path/line rationale or an exact-current self-hosted code-change rationale backed by a fresh REVIEW completion", "existing finding-backed consumed binding retained only for legacy compatibility", "done REVIEW or resolved finding",
		"done VERIFY or required passing check with test evidence",
		"non-empty coordination handoff", "consumed exact-revision provider evidence",
		"workflow workspace prepare, inspect, complete, integrate, reconcile, and cleanup",
		"coordinator owns every PROCESS workspace lifecycle operation",
		"review and verification use detached immutable workflow snapshots and fail closed when dirty",
		"external uses mode none", "consumed provider-neutral exact-revision evidence",
		"single runner-managed coordinator session", "supplied session checkout", "otherwise operate standalone",
		"ISSUE_SPEC_PROCESS_INTEGRATION_ROOT", "ISSUE_SPEC_PROCESS_WORKSPACE_ROOT",
		"current runtime's real native child/subagent facility", "exact worktree path, branch, write ownership, PROCESS id",
		"runtime-native child is not a coordinator session", "does not launch a nested coordinator session or claim a separate per-child OS sandbox",
		"children share the runner-managed coordinator session's outer sandbox", "unsafe-no-sandbox has no filesystem isolation",
		"result commit", "runs complete and integrate",
		"runtime recovers only the runner-managed coordinator session", "inspects or reconciles the exact managed PROCESS lease",
		"Runner-managed session retention cleanup consults git worktree list", "retaining the session checkout when runtime metadata is dirty or uncertain, a linked worktree exists, or git worktree inspection fails",
		"does not own, persist, or retry child PROCESS cleanup",
		"workflow workspace cleanup is always an explicit owner-token-authorized destructive operation",
		"does not decide or enforce integration/retention eligibility for its caller",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing %q:\n%s", want, workflow)
		}
	}
	for _, forbidden := range []string{
		"/resume <public-session-id> --process",
		"bubblewrap adds a read-only bind",
		"Target runner work with",
		"provider adapter", "adapter readiness", "readiness gate",
		"Runner terminal, cancellation, and reconciliation paths record cleanup intent",
		"A restart reconciles durable workspace lifecycle state",
		"record cleanup intent durably", "apply integration/retention eligibility",
		"retrying pending cleanup", "performs eligible cleanup",
		"runner external execution additionally", "configured provider adapter",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("workflow skill contains stale runner PROCESS dispatch guidance %q:\n%s", forbidden, workflow)
		}
	}
	apply := skillContent(t, skills, "issue-spec-apply")
	for _, want := range []string{"--gate implement", "doctor agent", "execution_class", "only for change-bearing", "same digest/checkpoint", "workspace prepare, inspect, complete, integrate, reconcile, and cleanup", "single runner-managed coordinator session", "otherwise operate standalone", "coordinator owns the managed workspace lifecycle", "current runtime's real native child/subagent facility", "exact worktree path as cwd", "one result commit, focused tests, and a bounded handoff", "there is no nested coordinator session or separate per-child OS sandbox", "workspace complete and integrate from its unchanged checkout", "detached immutable workflow snapshots and fail closed when dirty", "external uses mode none and requires consumed provider-neutral exact-revision evidence", "runtime recovers only the runner-managed coordinator session", "coordinator inspects or reconciles the exact managed PROCESS lease", "Runner-managed session retention cleanup consults git worktree list", "fails closed by retaining the session checkout when runtime metadata is dirty or uncertain, a linked worktree exists, or git worktree inspection fails", "does not own, persist, or retry child PROCESS cleanup", "workflow workspace cleanup is destructive and does not decide or enforce integration/retention eligibility"} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply skill missing %q:\n%s", want, apply)
		}
	}
	if strings.Contains(apply, "/resume <public-session-id> --process") {
		t.Fatalf("apply skill still advertises runner PROCESS selection:\n%s", apply)
	}
	for _, forbidden := range []string{"provider adapter", "adapter readiness", "readiness gate", "performs eligible cleanup", "retrying pending cleanup", "runner external execution additionally", "A restart reconciles durable workspace lifecycle state", "record cleanup intent durably", "apply integration/retention eligibility"} {
		if strings.Contains(apply, forbidden) {
			t.Fatalf("apply skill contains stale runner-owned PROCESS lifecycle guidance %q:\n%s", forbidden, apply)
		}
	}
	for _, incomplete := range []string{"retaining the clone when a linked worktree exists or inspection fails", "retains the clone when a linked worktree exists or inspection fails"} {
		if strings.Contains(workflow, incomplete) || strings.Contains(apply, incomplete) {
			t.Fatalf("generated skill contains incomplete session-clone retention guidance %q", incomplete)
		}
	}
	review := skillContent(t, skills, "issue-spec-review")
	if !strings.Contains(review, "per-PROCESS execution class") || !strings.Contains(review, "linked done REVIEW or resolved finding") {
		t.Fatalf("review skill lacks class carrier guidance:\n%s", review)
	}
	verify := skillContent(t, skills, "issue-spec-verify")
	if !strings.Contains(verify, "--gate final") ||
		!strings.Contains(verify, "backend-appropriate rationale and REVIEW completion evidence") {
		t.Fatalf("verify skill lacks proportional evidence guidance:\n%s", verify)
	}
}

func TestIssueSpecSkillsStateSelfContainedAuthoringInvariant(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillContent(t, skills, "issue-spec-workflow")
	for _, want := range []string{
		"Self-contained authoring: write proposal, design, SPEC, and TASK artifacts for a reader with no shared session context",
		"issue-spec:fill sentinel",
		"distinct from the ### Handoff PROCESS serial-chain evidence section and from the /resume session handle",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing %q:\n%s", want, workflow)
		}
	}
	if strings.Contains(workflow, "Do not leave active proposal/design/implement issue bodies as TBD placeholders.") {
		t.Fatalf("workflow skill still contains the stale TBD-placeholder line")
	}
}

func TestIssueSpecSkillsRequireNonCoordinatorChangeBearingWorkers(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillContent(t, skills, "issue-spec-workflow")
	for _, want := range []string{
		"Every agent-executed change-bearing PROCESS MUST use workspace_management: managed",
		"real non-coordinator runtime-native child",
		"MUST NOT implement/test/commit such a node inline",
		"One real worker MAY execute multiple compatible serial change-bearing or code-repair nodes",
		"a fresh worker is not required for every PROCESS",
		"each node retains distinct status, dependencies, workspace lifecycle, evidence, and handoff",
		"An external or human independent PROCESS stays in its executor-owned workspace",
		"For each distinct change-bearing author Agent",
		"one reviewer MAY cover multiple authors",
		"does not add a 1:1 final gate",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing mandatory worker guidance %q:\n%s", want, workflow)
		}
	}
	for _, forbidden := range []string{
		"Coding MAY be inline or delegated",
		"Inline: declare workspace_management: independent",
		"inline coding is allowed for any node",
		"Execute an inline (independent) coding node in the coordinator's integration checkout",
		"run each node in its own worker",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("workflow skill contains stale coordinator-inline guidance %q:\n%s", forbidden, workflow)
		}
	}

	apply := skillContent(t, skills, "issue-spec-apply")
	for _, want := range []string{
		"Every agent-executed change-bearing node MUST declare workspace_management: managed",
		"The worker's logical Agent MUST differ from the PROCESS coordinator Agent",
		"A different Agent name without a real dispatched child is insufficient",
		"MUST NOT be fabricated or relabeled only to pass process.executor.coordinator_conflict",
		"MUST NOT implement/test/commit such a node inline",
		"One real worker MAY execute multiple compatible serial change-bearing or code-repair PROCESS nodes",
		"a fresh worker is not required for every node",
		"each PROCESS keeps distinct state, dependencies, managed workspace lifecycle, evidence, and handoff",
		"One reviewer MAY cover multiple authors when it authored none of their code",
		"final verification remains per SPEC and MUST NOT require a 1:1 implementation-author-to-reviewer mapping",
		"external or human independent PROCESS remains in its executor-owned workspace",
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply skill missing mandatory worker guidance %q:\n%s", want, apply)
		}
	}
	for _, forbidden := range []string{
		"The coordinator MAY implement coding nodes inline",
		"Inline coordinator-authored node",
		"inline coding is allowed for any node",
		"coordinator-inline",
		"run each in its own worker",
	} {
		if strings.Contains(apply, forbidden) {
			t.Fatalf("apply skill contains stale coordinator-inline guidance %q:\n%s", forbidden, apply)
		}
	}
}

func TestIssueSpecSkillsPlaceFinalRationaleAfterIndependentReviewConvergence(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	for _, name := range []string{"issue-spec-workflow", "issue-spec-apply"} {
		content := skillContent(t, skills, name)
		parts := strings.SplitN(content, "## Coordinator DAG Execution", 2)
		if len(parts) != 2 {
			t.Fatalf("%s skill missing Coordinator DAG Execution section", name)
		}
		dag := parts[1]
		reviewAt := strings.Index(dag, "independent review")
		rationaleAt := strings.Index(dag, "Only after independent review/fix convergence")
		if reviewAt < 0 || rationaleAt < 0 || rationaleAt <= reviewAt {
			t.Fatalf("%s skill does not place final rationale after independent review convergence:\n%s", name, dag)
		}
		for _, forbidden := range []string{
			"Both paths record change-bearing rationale",
			"Both record change-bearing rationale",
			"then records rationale and (for a serial predecessor)",
		} {
			if strings.Contains(dag, forbidden) {
				t.Fatalf("%s skill contains rationale-before-review guidance %q:\n%s", name, forbidden, dag)
			}
		}
	}
}

func TestIssueSpecSkillsIncludeGitHubCLISupportSkill(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	if hasSkill(skills, "github") {
		t.Fatal("generic github skill should not be generated")
	}
	github := skillContent(t, skills, "issue-spec-github")
	for _, want := range []string{
		"name: issue-spec-github",
		"compatibility: Requires GitHub CLI (gh).",
		"Use GitHub CLI for GitHub issues",
		"gh auth login",
		"gh pr checks",
		"gh api",
		"Ordinary issue discussion writes",
		"issue-spec comment create --repo owner/repo --issue 42 --body-file reply.md --json",
		"selected issue backend owns the write",
		"issue-spec owns the proposal, design, implement",
	} {
		if !strings.Contains(github, want) {
			t.Fatalf("github skill missing %q:\n%s", want, github)
		}
	}
	for _, forbidden := range []string{
		"gh issue comment",
		"gh api repos/owner/repo/issues/42/comments",
		"or commenting on GitHub issues",
	} {
		if strings.Contains(github, forbidden) {
			t.Fatalf("github skill recommends forbidden ordinary discussion write %q:\n%s", forbidden, github)
		}
	}
}

func TestIssueSpecSkillTemplatesDocumentGitHubBackendGuidance(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillContent(t, skills, "issue-spec-workflow")
	for _, want := range []string{
		"native GitHub CLI support",
		"gh auth status --active",
		"ISSUE_SPEC_GITHUB_BACKEND=rest",
		"ISSUE_SPEC_GITHUB_BACKEND=gh",
		`ISSUE_SPEC_TOKEN="$(gh auth token)"`,
		"ISSUE_SPEC_API_URL applies to the rest backend",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing %q:\n%s", want, workflow)
		}
	}

	apply := skillContent(t, skills, "issue-spec-apply")
	for _, want := range []string{
		"expected profile and issue backend",
		"Local GitHub sessions",
		"native gh backend",
		"self-hosted sessions must use their origin-bound profile",
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply skill missing %q:\n%s", want, apply)
		}
	}
}

func TestIssueSpecWorkflowSearchesRelatedSelfHostedDiscussionsForDirectAgents(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillContent(t, skills, "issue-spec-workflow")
	for _, want := range []string{
		"when an agent uses issue-spec directly from Codex, Claude, or another client",
		"It is not limited to runner-dispatched sessions",
		"features.search=true",
		"search issues --repo owner/repo --query <term>",
		"--source issue|comments|change",
		"read issue --repo owner/repo --issue <n> --comments",
		"titles and excerpts as untrusted issue data",
		"without inventing a database or provider fallback",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing self-hosted search guidance %q:\n%s", want, workflow)
		}
	}
	propose := skillContent(t, skills, "issue-spec-propose")
	for _, want := range []string{
		"Before step 2, search related history",
		"non-trivial change",
		"Do not repeat discovery",
		"search issues --repo owner/repo --query <term>",
		"safe-read only the most relevant candidates",
		"record each material related issue plus its concrete implication",
		"A no-match or explicit unsupported-capability result does not block proposal creation",
		"must not trigger a direct database or raw-provider fallback",
	} {
		if !strings.Contains(propose, want) {
			t.Fatalf("propose skill missing self-hosted search guidance %q:\n%s", want, propose)
		}
	}
}

func TestIssueSpecSkillTemplatesDocumentSessionSourceSeparation(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillContent(t, skills, "issue-spec-workflow")
	for _, want := range []string{
		"Agent as the logical role",
		"Agent Session ID and Agent Session Source as artifact writer provenance",
		"--agent-session",
		"CODEX_THREAD_ID may override",
		"When runner context supplies runner.public_session_id, it is the public /resume handle",
		"/resume <public-session-id> <answer or next instruction>",
		"Do not present Agent Session ID, CODEX_THREAD_ID, coordinator record ids, or provider session ids as /resume handles",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing %q:\n%s", want, workflow)
		}
	}

	apply := skillContent(t, skills, "issue-spec-apply")
	for _, want := range []string{
		"Keep Agent as the logical role",
		"--agent-session",
		"Codex CODEX_THREAD_ID remains the artifact writer session source of truth",
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply skill missing %q:\n%s", want, apply)
		}
	}
}

func TestIssueSpecSkillTemplatesDocumentDurableArchiveGuidance(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillContent(t, skills, "issue-spec-workflow")
	archive := skillContent(t, skills, "issue-spec-archive")
	for _, want := range []string{
		"abstract long-lived --capability directory",
		"inspect existing related durable specs",
		"regroup the generated draft by stable capability modules",
		"umbrella capability that accumulates related current and future changes",
		"accumulates new requirements into an existing capability spec by requirement title (newest wins)",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing %q:\n%s", want, workflow)
		}
	}
	for _, want := range []string{
		"stable long-lived capability or domain directory",
		"not the original change/proposal name",
		"workflow-identity-and-sessions instead of agent-session-source-of-truth",
		"Inspect existing durable specs before creating or finalizing the archive PR",
		"issue-spec/specs/<capability>/spec.md",
		"issue-spec/specs/*/spec.md",
		"update, merge, or reorganize existing durable requirements",
		"Reconcile it with any existing related durable specs",
		"regroup related source SPEC content into durable capability modules",
		"Source SPEC links for traceability",
		"umbrella capability",
		"accumulates the new proposal's requirements into the existing spec by requirement title (newest wins)",
	} {
		if !strings.Contains(archive, want) {
			t.Fatalf("archive skill missing %q:\n%s", want, archive)
		}
	}
}

func TestIssueSpecSkillsDirectAgentsToGenerators(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")

	propose := skillContent(t, skills, "issue-spec-propose")
	for _, want := range []string{
		"issue-spec comment generate --type SPEC",
		"--allow-noncanonical",
		"issue-spec comment generate --type TASK",
		"standardized `Proposal: <subject>`, `Design: <subject>`, and `Implement: <subject>` family",
		"Use --title only for an explicit user-requested custom title",
		"do not apply style-only issue update rewrites after creation",
		"Historical issues with `issue-spec proposal: <change>`",
	} {
		if !strings.Contains(propose, want) {
			t.Fatalf("propose skill missing generator guidance %q:\n%s", want, propose)
		}
	}

	workflow := skillContent(t, skills, "issue-spec-workflow")
	if !strings.Contains(workflow, "issue-spec comment generate") {
		t.Fatalf("workflow skill missing generator guidance:\n%s", workflow)
	}

	// The generic REVIEW guidance must preserve review sync ownership.
	review := skillContent(t, skills, "issue-spec-review")
	if !strings.Contains(review, "Review Sync Summary") || !strings.Contains(review, "issue-spec comment generate --type REVIEW") {
		t.Fatalf("review skill missing generate/review-sync guidance:\n%s", review)
	}

	verify := skillContent(t, skills, "issue-spec-verify")
	if !strings.Contains(verify, "issue-spec comment generate --type VERIFY") {
		t.Fatalf("verify skill missing VERIFY generator guidance:\n%s", verify)
	}
}

func TestIssueSpecSkillTemplatesEnforceAgentOwnedReviewWorkflow(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")

	review := skillContent(t, skills, "issue-spec-review")
	if strings.Contains(review, "the coordinator converts actionable line findings") {
		t.Fatalf("review skill still tells the coordinator to author findings:\n%s", review)
	}
	for _, want := range []string{
		"Each review agent authors its own",
		"The coordinator does not create findings on a review agent's behalf",
		"The worker that owns the affected code fixes it and replies",
		"The review agent that opened the finding then re-checks",
		"a worker reply alone does not resolve a finding",
		"--from REVIEW-<n> --from-issue <implement-issue> --to PROCESS-<n>",
		"--from REVIEW-<n> --from-issue <implement-issue> --to SPEC-<n>",
		"Run these commands after the final review sync",
		"Related Comments contains the review PROCESS URL, every covered change-bearing PROCESS URL, and every covered active SPEC URL",
	} {
		if !strings.Contains(review, want) {
			t.Fatalf("review skill missing ownership guidance %q:\n%s", want, review)
		}
	}

	apply := skillContent(t, skills, "issue-spec-apply")
	for _, want := range []string{
		"Add final rationale only after review/fix convergence and only for change-bearing PROCESS nodes",
		"Follow issue-spec-workflow for the backend-appropriate rationale command",
		"Each owning worker authors its own rationale under that worker's --agent and --agent-session",
		"MUST NOT create worker rationale or relabel its identity on the worker's behalf",
		"does not author implementation commits, review findings, worker fix replies, review resolutions, or rationale on another agent's behalf",
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply skill missing ownership guidance %q:\n%s", want, apply)
		}
	}
}

func TestIssueSpecSkillTemplatesDocumentSelfHostedReviewCompletionContract(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")

	workflow := skillContent(t, skills, "issue-spec-workflow")
	for _, want := range []string{"persists and reloads provider facts", "exact-current completion stamp", "finding-backed consumed binding retained only for legacy compatibility"} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing detailed backend routing guidance %q:\n%s", want, workflow)
		}
	}

	review := skillContent(t, skills, "issue-spec-review")
	for _, want := range []string{
		"On GitHub add --pr <number>; on a self-hosted profile omit --pr and add --revision <exact-head>",
		"Sync authoritatively captures current rationale",
		"one stable done REVIEW completion even with zero findings",
		"never hand-edit either, fabricate a finding, or substitute a generic approval framework",
		"Never rely on PROCESS/SPEC IDs in prose or auto-infer links",
		"Status and final verify validate the same backend-appropriate completion",
		"they do not refresh REVIEW",
	} {
		if !strings.Contains(review, want) {
			t.Fatalf("review skill missing completion safety guidance %q:\n%s", want, review)
		}
	}

	verify := skillContent(t, skills, "issue-spec-verify")
	for _, want := range []string{
		"Status forecast and final verify use the same authoritative validator",
		"The validator owns exact identity, revision, freshness, and legacy compatibility",
		"Neither command creates, updates, or refreshes REVIEW",
	} {
		if !strings.Contains(verify, want) {
			t.Fatalf("verify skill missing shared completion guidance %q:\n%s", want, verify)
		}
	}

	archive := skillContent(t, skills, "issue-spec-archive")
	for _, want := range []string{
		"Archive may read an existing required REVIEW completion when implementation merge policy requires it",
		"Archive never creates, updates, or refreshes REVIEW",
		"adds archive-specific review state",
	} {
		if !strings.Contains(archive, want) {
			t.Fatalf("archive skill missing read-only completion guidance %q:\n%s", want, archive)
		}
	}

	for name, forbidden := range map[string][]string{
		"issue-spec-apply": {
			"Successful sync writes the exact-current completion",
			"finding-backed consumed binding accepted only for legacy compatibility",
			"finding-backed consumed native-ledger PROCESS/SPEC binding",
		},
		"issue-spec-review":  {"persists and reloads provider facts", "exact-current completion stamp", "For separate GitHub manual review evidence", "GitHub conversation"},
		"issue-spec-archive": {"code_change", "archive_change"},
	} {
		content := skillContent(t, skills, name)
		for _, phrase := range forbidden {
			if strings.Contains(content, phrase) {
				t.Fatalf("%s skill exposes backend protocol detail %q:\n%s", name, phrase, content)
			}
		}
	}
}

func TestIssueSpecSkillTemplatesDispatchSearchAndCodeChangeByBackend(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillContent(t, skills, "issue-spec-workflow")
	for _, want := range []string{
		"Active change artifacts live in the selected issue backend",
		"The selected profile chooses the adapter",
		"GitHub supports issue/comment/stage search but rejects `--source change`",
		"Self-hosted workflows take provider and external repository identity from the active Source Binding",
		"code-change attach --repo owner/repo",
		"does not create a PR/MR or ingest review/CI evidence",
		"`--refresh` and `--expected-version` must be supplied together",
		"code-change link-process --repo owner/repo",
		"explicitly delete only the unwanted active reference",
		"Do not call a GitHub PR endpoint",
		"GitHub-backed workflows keep the existing `pr link-process`",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow skill missing backend guidance %q:\n%s", want, workflow)
		}
	}

	propose := skillContent(t, skills, "issue-spec-propose")
	for _, want := range []string{"selected by the active profile", "GitHub profiles search issues/comments/stages", "reject `--source change`"} {
		if !strings.Contains(propose, want) {
			t.Fatalf("propose skill missing backend search guidance %q:\n%s", want, propose)
		}
	}

	apply := skillContent(t, skills, "issue-spec-apply")
	for _, want := range []string{
		"following the backend-appropriate routing in issue-spec-workflow",
		"authoritative final sync by following issue-spec-review",
		"Follow issue-spec-workflow for backend-appropriate implementation-change closure",
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply skill missing backend code-change guidance %q:\n%s", want, apply)
		}
	}
	for _, forbidden := range []string{"issue-spec pr link-process", "code-change attach", "code-change link-process", "code-change rationale"} {
		if strings.Contains(apply, forbidden) {
			t.Fatalf("apply skill duplicates workflow backend routing %q:\n%s", forbidden, apply)
		}
	}
}

func skillContent(t *testing.T, skills []RenderedSkill, name string) string {
	t.Helper()
	for _, skill := range skills {
		if skill.Name == name {
			return skill.Content
		}
	}
	t.Fatalf("skill %q not found", name)
	return ""
}

func hasSkill(skills []RenderedSkill, name string) bool {
	for _, skill := range skills {
		if skill.Name == name {
			return true
		}
	}
	return false
}
