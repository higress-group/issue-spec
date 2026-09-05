package templates

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIssueSpecSkillAndCommandTemplates(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	if got, want := len(skills), 4; got != want {
		t.Fatalf("skills = %d, want %d", got, want)
	}
	if !strings.Contains(skills[0].Content, `generatedBy: "issue-spec"`) {
		t.Fatalf("skill missing generatedBy:\n%s", skills[0].Content)
	}
	commands := IssueSpecCommandContents("owner/repo")
	if got, want := len(commands), 2; got != want {
		t.Fatalf("commands = %d, want %d", got, want)
	}
	if commands[0].ID != "propose" || !strings.Contains(commands[0].Body, "owner/repo") {
		t.Fatalf("unexpected first command: %+v", commands[0])
	}
	for _, skill := range skills {
		if skill.Name == "issue-spec-archive" || skill.Name == "issue-spec-review" || skill.Name == "issue-spec-verify" ||
			strings.Contains(skill.Content, "Issue Spec Archive") {
			t.Fatalf("normal generated skills still expose retired gates: %s", skill.Name)
		}
	}
	for _, command := range commands {
		if command.ID == "archive" || strings.Contains(command.Body, "Issue Spec Archive") {
			t.Fatalf("normal generated commands still expose Archive: %s", command.ID)
		}
	}
	propose := skillContent(t, skills, "issue-spec-propose")
	for _, want := range []string{
		"rules.language",
		"rules.language_instructions",
		"pass an explicit `--title` for Proposal, Design, and Implement",
		"retains an English stage prefix",
		"issue create simple",
		"never promote an investigation issue into the proposal or attach SPEC/Design to it",
	} {
		if !strings.Contains(propose, want) {
			t.Fatalf("issue-spec-propose missing title guidance %q", want)
		}
	}
}

func TestCheckedInWorkflowAssetsMatchAuthoritativeTemplates(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	options := WorkflowAuthoringOptions{HTMLReviewEnabled: false}
	for _, skill := range IssueSpecSkillsWithOptions("higress-group/issue-spec", options) {
		path := filepath.Join(root, ".agents", "skills", skill.Name, "SKILL.md")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got, want := string(body), strings.TrimSpace(skill.Content)
		if got != want+"\n" && !strings.HasPrefix(got, want+"\n\n## Project Workflow\n") {
			t.Fatalf("checked-in skill %s differs from its authoritative template at %s", path, firstTextDifference(got, want))
		}
	}
	for _, command := range IssueSpecCommandContentsWithOptions("higress-group/issue-spec", options) {
		path := filepath.Join(root, ".claude", "commands", "issue-spec", command.ID+".md")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(string(body), "---\n\n", 2)
		if len(parts) != 2 {
			t.Fatalf("checked-in command %s has no front-matter boundary", path)
		}
		got, want := parts[1], strings.TrimSpace(command.Body)
		if got != want+"\n" && !strings.HasPrefix(got, want+"\n\n## Project Workflow\n") {
			t.Fatalf("checked-in command %s differs from its authoritative template at %s", path, firstTextDifference(got, want))
		}
	}
}

func firstTextDifference(got, want string) string {
	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(want, "\n")
	for index := 0; index < len(gotLines) && index < len(wantLines); index++ {
		if gotLines[index] != wantLines[index] {
			return fmt.Sprintf("line %d: got %q, want %q", index+1, gotLines[index], wantLines[index])
		}
	}
	return fmt.Sprintf("line count: got %d, want %d", len(gotLines), len(wantLines))
}

func TestHTMLReviewAuthoringOptionsPreserveEnabledDefaultsAndOmitDisabledGuidance(t *testing.T) {
	enabled := WorkflowAuthoringOptions{HTMLReviewEnabled: true}
	if got, want := IssueSpecSkillsWithOptions("owner/repo", enabled), IssueSpecSkills("owner/repo"); !reflect.DeepEqual(got, want) {
		t.Fatal("explicitly enabled skills differ from backward-compatible defaults")
	}
	if got, want := IssueSpecCommandContentsWithOptions("owner/repo", enabled), IssueSpecCommandContents("owner/repo"); !reflect.DeepEqual(got, want) {
		t.Fatal("explicitly enabled commands differ from backward-compatible defaults")
	}

	disabled := WorkflowAuthoringOptions{HTMLReviewEnabled: false}
	skills := IssueSpecSkillsWithOptions("owner/repo", disabled)
	for _, skill := range skills {
		for _, resource := range skill.Resources {
			if skill.Name != "issue-spec-workflow" || resource.Path != "references/implementation-review.md" {
				t.Fatalf("disabled HTML review emitted projection resource for %s: %s", skill.Name, resource.Path)
			}
		}
		for _, forbidden := range []string{
			"Human Review Projection", "human-review-projections.md", "proposal-choice-brief",
			"design-explainer", "implement-execution-brief", "projection upsert",
		} {
			if strings.Contains(skill.Content, forbidden) {
				t.Fatalf("disabled skill %s retains HTML review authoring guidance %q:\n%s", skill.Name, forbidden, skill.Content)
			}
		}
	}
	workflow := skillWithReview(t, skills, "issue-spec-workflow")
	for _, want := range []string{"Keep proposal, Design, SPEC, and TASK self-contained", "blocking typed QUESTION", "built-in phase sequence and canonical artifact carriers are authoritative", "never reorder/omit steps or move open decisions", "exact-head human review handoff", "### Implementation Rationale"} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("disabled workflow lost typed obligation %q:\n%s", want, workflow)
		}
	}
	propose := skillContent(t, skills, "issue-spec-propose")
	for _, want := range []string{"Generate canonical SPEC comments", "Generate TASK comments", "--covers-issue"} {
		if !strings.Contains(propose, want) {
			t.Fatalf("disabled propose skill lost typed obligation %q:\n%s", want, propose)
		}
	}
	apply := skillWithReview(t, skills, "issue-spec-apply")
	for _, want := range []string{"finalize the plan", "Author PROCESS only for managed coordination", "real non-Coordinator worker", "exact result commit", "Do not create a role receipt", "### Implementation Rationale"} {
		if !strings.Contains(apply, want) {
			t.Fatalf("disabled apply skill lost implementation obligation %q:\n%s", want, apply)
		}
	}

	for _, command := range IssueSpecCommandContentsWithOptions("owner/repo", disabled) {
		for _, forbidden := range []string{"Human Review Projection", "proposal-choice-brief", "design-explainer", "implement-execution-brief", "projection upsert"} {
			if strings.Contains(command.Body, forbidden) {
				t.Fatalf("disabled command %s retains HTML review authoring guidance %q:\n%s", command.ID, forbidden, command.Body)
			}
		}
	}
}

func TestGeneratedGuidanceMakesBuiltInPhaseProtocolAuthoritative(t *testing.T) {
	const authority = "For selected issue-spec steps, the built-in phase sequence and canonical artifact carriers are authoritative; never reorder/omit steps or move open decisions."
	for _, skill := range IssueSpecSkills("owner/repo") {
		if skill.Name == "issue-spec-github" {
			continue
		}
		if !strings.Contains(skill.Content, authority) {
			t.Fatalf("%s lacks authoritative phase protocol %q:\n%s", skill.Name, authority, skill.Content)
		}
	}
}

func TestTypedCommentWriterGuidanceRepeatsIssueScopedIDAllocation(t *testing.T) {
	for _, skill := range IssueSpecSkillsWithOptions("owner/repo", WorkflowAuthoringOptions{HTMLReviewEnabled: false}) {
		if skill.Name == "issue-spec-github" {
			continue
		}
		for _, want := range []string{
			"<TYPE>-<issue><three-digit sequence>",
			"QUESTION-1001",
			"QUESTION-44001",
			"New writes reject wrong Issue prefixes",
			"--allow-legacy-id",
			"never renumber a legacy ID",
		} {
			if !strings.Contains(skill.Content, want) {
				t.Fatalf("%s lacks typed ID allocation guidance %q:\n%s", skill.Name, want, skill.Content)
			}
		}
	}
	for _, command := range IssueSpecCommandContentsWithOptions("owner/repo", WorkflowAuthoringOptions{HTMLReviewEnabled: false}) {
		if !strings.Contains(command.Body, "<TYPE>-<issue><three-digit sequence>") || !strings.Contains(command.Body, "--allow-legacy-id") {
			t.Fatalf("%s command lacks typed ID allocation guidance:\n%s", command.ID, command.Body)
		}
	}
}

func TestCoordinatorGuidanceKeepsActionsStopsAndRecovery(t *testing.T) {
	workflow := skillWithReview(t, IssueSpecSkills("owner/repo"), "issue-spec-workflow")
	wants := []string{
		"auth status --json", "workflow validate --repo owner/repo", "bounded simple Issue", "one code writer",
		"single child or subagent is an execution choice", "concurrent code writers", "protection of pre-existing work",
		"enforced path ownership", "restartable cross-session handoff", "dependency-ordered integration",
		"Select execution mode before assigning writers", "Once Design or TASK is selected", "Coordinator MUST NOT write code on delegated or managed paths",
		"user explicitly requests an independent worker", "Without managed PROCESS, exactly one real non-Coordinator worker",
		"With managed PROCESS", "every change-bearing work package/PROCESS has one real non-Coordinator owner",
		"distinct packages MAY use concurrent writers", "Coordinator dispatches and waits",
		"narrow direct-PR fast path", "no selected Design/TASK and no user delegation request",
		"File count never selects this exception", "Do not create PROCESS solely because a child is used",
		"<TYPE>-<issue><three-digit sequence>", "QUESTION-1001", "QUESTION-44001",
		"do not add another type digit or search the whole repository", "never renumber a legacy ID",
		"one independently verifiable Design invariant", "bounded context and working set", "stable interface",
		"exact-head human review handoff", "provider-native PR/MR", "Stop before approval or merge",
		"current provider-native CI", "human and code provider own approval and merge",
		"exact head, PR/MR link, tests and results", "Deprecated review sync/submit completion",
		"### Implementation Rationale", "Every actual code writer owns zero or more line-rationale drafts",
		"Before requesting human review", "Comments and status are human review context and never certify mergeability",
		"actual code writer", "line-rationale drafts", "stable symbol plus changed-line anchor", "why/tradeoff/risk",
		"Writers need no provider credentials", "MUST NOT guess final diff positions",
		"needs no draft, quota, coverage target, or placeholder", "maps it to a changed line",
		"valid worker text", "non-blocking inline discussion", "decisions/tradeoffs", "boundaries/risks", "validation/results", "indexes inline rationale",
		"would create an unresolved merge blocker", "path:symbol/line",
		"secret, raw payload, or credential", "confirms the text still applies and contains no sensitive data",
		"Invalid, stale, or sensitive drafts", "never rewrites and impersonates the writer",
		"Each worker owns one package's code changes, focused tests, exact result commit", "Coordinator owns dispatch and wait, exact-commit inspection, integration",
		"proportionate final validation, anchor validation, and provider publication", "Do not give provider credentials to workers",
		"real read-only reviewer that is independent of every code writer", "exact base and current exact head", "no write path or provider credentials",
		"actionable P0, P1, or P2 findings", "Route every P0/P1 unchanged to the original writer", "same reviewer recheck it",
		"Repeat automatically until that reviewer reports zero P0/P1", "still-applicable P2 findings from the final reviewed head",
		"provider-native non-blocking line comment", "ordinary change-level `change.comment`", "P2 never enters the repair loop and never pauses completion",
		"report the rendered comment body and continue", "no typed REVIEW/VERIFY, finding evidence, receipt, readiness gate, or reviewer merge authority",
	}
	for _, want := range wants {
		if !strings.Contains(workflow, want) {
			t.Fatalf("coordinator guidance missing %q:\n%s", want, workflow)
		}
	}
	for _, stale := range []string{
		"Codex CODEX_THREAD_ID remains the artifact writer session source of truth",
		"CODEX_THREAD_ID may override",
		"split into parallel worker PROCESS nodes only when file/module write ownership is provably disjoint",
		"one repair PROCESS per finding",
	} {
		if strings.Contains(workflow, stale) {
			t.Fatalf("coordinator guidance contains stale rule %q", stale)
		}
	}
}

func TestApplyGuidanceDistinguishesUnmanagedWorkerFromManagedPackages(t *testing.T) {
	apply := skillWithReview(t, IssueSpecSkills("owner/repo"), "issue-spec-apply")
	for _, want := range []string{
		"select execution mode before assigning writers",
		"If Design or TASK is selected", "user explicitly requests an independent worker",
		"Coordinator MUST NOT write code on delegated or managed paths",
		"Without managed PROCESS, exactly one real non-Coordinator worker owns the bounded implementation",
		"With managed PROCESS", "every change-bearing work package/PROCESS has one real non-Coordinator owner",
		"distinct packages MAY use concurrent writers",
		"## Delegated Paths and Narrow Coordinator Path",
		"Unmanaged delegated path", "dispatch exactly one real non-Coordinator worker",
		"one real non-Coordinator owner per change-bearing package", "proven-independent packages may run concurrently",
		"waits and writes no code on either path",
		"narrow direct-PR fast path", "no selected Design/TASK", "no user delegation request",
		"file count does not select it",
		"Do not manufacture Implement, PROCESS, workspace lifecycle, role receipt, typed rationale, evidence, or another phase artifact",
		"Each worker owns package code, focused tests, exact result commit", "Coordinator owns exact-commit inspection, integration",
		"### Implementation Rationale", "Every actual code writer owns zero or more line-rationale drafts",
		"No Implement, TASK, PROCESS, or SPEC is required", "Comments and status are human review context and never certify mergeability",
		"On an unmanaged delegated path this is the single non-Coordinator worker", "on the narrow Coordinator fast path it is the Coordinator", "under managed PROCESS each package owner owns its drafts",
		"real read-only reviewer that is independent of every code writer", "exact base and current exact head", "no write path or provider credentials",
		"actionable P0, P1, or P2 findings", "Route every P0/P1 unchanged to the original writer", "same reviewer recheck it",
		"Repeat automatically until that reviewer reports zero P0/P1", "still-applicable P2 findings from the final reviewed head",
		"provider-native non-blocking line comment", "ordinary change-level `change.comment` that preserves `path:symbol/line`",
		"P2 never enters the repair loop and never pauses completion", "report the rendered comment body and continue",
		"no typed REVIEW/VERIFY, finding evidence, receipt, readiness gate, or reviewer merge authority",
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply guidance missing direct delegation rule %q:\n%s", want, apply)
		}
	}
	for _, forbidden := range []string{"issue-spec code-change rationale", "issue-spec:code-change-rationale", "Rationale ID:"} {
		if strings.Contains(apply, forbidden) {
			t.Fatalf("apply guidance restores legacy rationale mechanism %q:\n%s", forbidden, apply)
		}
	}
	for _, forbidden := range []string{"coordinator MAY implement directly or dispatch", "whether Coordinator or child", "For selected Design/TASK or a user delegation request, dispatch exactly one", "select one writer before editing"} {
		if strings.Contains(apply, forbidden) {
			t.Fatalf("apply guidance retains ambiguous writer rule %q:\n%s", forbidden, apply)
		}
	}
}

func TestRoleGuidanceIsBoundedAndRuntimeNeutral(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	cases := []struct {
		name      string
		required  []string
		forbidden []string
	}{
		{
			name: "issue-spec-apply",
			required: []string{"sealed implementation assignment", "design_context.read_mode=complete-issue-body",
				"issue-spec read issue --repo owner/repo", "Stop and report any conflict", "assigned worktree and owned paths",
				"assigned generators", "exactly one DCO commit", "exact result commit", "changed paths", "command outcomes",
				"Collect zero or more line-rationale drafts", "stable symbol plus changed-line anchor", "Do not guess a provider diff position",
				"Provider access and final diff positions are not worker responsibilities", "Do not create a role receipt",
				"secret, raw payload, or credential", "An amendment invalidates the returned revision", "line-rationale drafts", "anchor validation", "publishes worker-authored text"},
			forbidden: []string{"issue-spec role complete", "receipt.json", "decision-file", "workflow workspace prepare", "workflow reconcile", "pr link-issues", "code-change attach", "archive durable-spec", "SPEC <-> TASK"},
		},
	}
	for _, tc := range cases {
		content := skillWithReview(t, skills, tc.name)
		for _, want := range tc.required {
			if !strings.Contains(content, want) {
				t.Fatalf("%s missing bounded role contract %q:\n%s", tc.name, want, content)
			}
		}
		for _, forbidden := range tc.forbidden {
			if strings.Contains(content, forbidden) {
				t.Fatalf("%s leaks coordinator policy %q:\n%s", tc.name, forbidden, content)
			}
		}
		for _, stale := range []string{"CODEX_THREAD_ID", "--agent-session", "session source of truth", "agent-session value is required"} {
			if strings.Contains(content, stale) {
				t.Fatalf("%s depends on runtime-specific session metadata %q", tc.name, stale)
			}
		}
	}
	for _, retired := range []string{"issue-spec-review", "issue-spec-verify", "issue-spec-archive"} {
		for _, skill := range skills {
			if skill.Name == retired {
				t.Fatalf("retired authority skill %q was generated", retired)
			}
		}
	}
}

func TestRoleGuidanceRemovesFormerReceiptRecipe(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	former := map[string]string{
		"issue-spec-apply": `## Implementation Role Packet
1. Accept only the sealed implementation assignment for the exact PROCESS, base revision, worktree, write ownership, focused tests, generators, result schema, and design_context. Do not load proposal bodies, the complete DAG, link matrices, post-merge policy, provider routing, or unrelated artifacts.
2. Before code changes, require design_context.read_mode=complete-issue-body and conflict_policy=design-authoritative-stop. Read the complete Design without comments, timeline, history, or gates. Stop and report any conflict.
3. Work only in the assigned worktree and owned paths. Preserve the named invariant, decisions, must_preserve, must_not, and minimum_verification exactly.
4. Implement the owned invariant, run the assigned generators exactly, and run focused verification. If the assignment cannot fit a bounded end-to-end working set, stop with the concrete stable-interface split options and acceptance consequences.
5. For each focused test with a result-revision binding, first produce the exact final DCO result commit, resolve the sealed declarative selector against that commit, run the resulting command, and record both assigned_selector and resolved_revision with the executed command and outcome. Any amendment changes the result revision, invalidates the earlier test evidence, and requires resolution and execution again. Literal selectors retain their exact command identity.
6. Manually construct the complete receipt schema from assignment identity, Git revision, changed paths, selectors, outcomes, assurance, decisions, risks, rationale, and provenance. For every literal selector copy the exact command. For every bound selector copy assigned_selector, resolved_revision, and the expanded command. Set route=role-owned, assurance=self-reported, writer and subject to the role agent, and the manual source. Canonically sort changed paths, decisions, risks, and tests, omit receipt_digest, hash the canonical JSON, restore receipt_digest, and avoid framing changes. Re-open the output and manually compare assignment id, digest, generation, role, base, result revision, tests, receipt id, and receipt digest. Return the result commit, changed paths, generator outputs, focused tests, decisions, risks, and bounded handoff/result receipt.`,
	}
	for name, old := range former {
		content := rolePacketOnly(skillContent(t, skills, name))
		for _, forbidden := range []string{"issue-spec role complete", "receipt_digest", "sha256sum", "jq -S", "hash canonical JSON", "newline framing"} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("%s retains manual receipt recipe %q", name, forbidden)
			}
		}
		if len([]byte(content)) >= len([]byte(old)) {
			t.Fatalf("%s role packet did not compact: new=%d former=%d", name, len([]byte(content)), len([]byte(old)))
		}
	}
}

func rolePacketOnly(content string) string {
	start := strings.Index(content, "## Implementation Role Packet")
	if start < 0 {
		start = strings.Index(content, "## Review Role Packet")
	}
	if start < 0 {
		start = strings.Index(content, "## Verification Role Packet")
	}
	if start < 0 {
		return ""
	}
	content = content[start:]
	if end := strings.Index(content, "\n## "); end >= 0 {
		content = content[:end]
	}
	return content
}

func TestGeneratedGuidanceDeterministicSizeBudgets(t *testing.T) {
	type budget struct {
		maxBytes    int
		maxHeadings int
		maxItems    int
	}
	budgets := map[string]budget{
		"issue-spec-workflow": {maxBytes: 12800, maxHeadings: 8, maxItems: 40},
		"issue-spec-propose":  {maxBytes: 5000, maxHeadings: 4, maxItems: 16},
		"issue-spec-apply":    {maxBytes: 8900, maxHeadings: 5, maxItems: 12},
	}
	for _, skill := range IssueSpecSkills("owner/repo") {
		limit, ok := budgets[skill.Name]
		if !ok {
			continue
		}
		if got := len([]byte(skill.Content)); got > limit.maxBytes {
			t.Errorf("%s UTF-8 bytes = %d, budget %d", skill.Name, got, limit.maxBytes)
		}
		if got := countInstructionLines(skill.Content, "#"); got > limit.maxHeadings {
			t.Errorf("%s headings = %d, budget %d", skill.Name, got, limit.maxHeadings)
		}
		if got := countListItems(skill.Content); got > limit.maxItems {
			t.Errorf("%s list items = %d, budget %d", skill.Name, got, limit.maxItems)
		}
	}
}

func TestGeneratedGuidanceDefinesThreeStatuslessProjectionCheckpoints(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	workflow := skillWithReview(t, skills, "issue-spec-workflow")
	for _, want := range []string{
		"persist the phase issue body, perform the first QUESTION discovery/create pass, upsert the human review projection",
		"SPEC for Proposal, TASK for Design, and PROCESS for Implement only when managed coordination was selected",
		"one source-digest-bound logical comment",
		"issue-spec projection upsert --repo owner/repo --issue <phase-issue>",
		"--phase <proposal-choice-brief|design-explainer|implement-execution-brief> --source-digest <sha256>",
		"ordinary statusless synthesis, not gate or Agent authority",
		"no typed marker, status, or transition",
		"only the latest effective ANSWER remain authoritative",
		"Keep projection HTML source out of default Agent context",
		"without atomic conditional projection creation",
		"first create after observing no matching projection",
		"--allow-nonatomic --expected-absence",
		"remains non-atomic",
		"full post-create re-observation proves exactly one matching logical projection with the planned body",
		"without CAS, replacement after observing the unique current body",
		"--allow-nonatomic --expected-digest <observed-sha256>",
		"exact post-write re-observation guards the digest-bound update",
		"absence and digest preconditions are mutually exclusive",
		"GitHub stores source only and never executes the preview or interactive answer intent",
		"read [Human Review Projection Generation](references/human-review-projections.md) completely",
		"Build a coverage ledger from authoritative inputs",
		"coverage-complete review surface rather than a delta, changelog, or executive summary",
		"Markdown fallback, single `html-preview`, source digest, coverage audit, and validation checks",
		"Record every genuine unresolved decision as a blocking typed QUESTION before the phase projection upsert",
		"issue-body or projection prose never carries an open decision",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow guidance missing %q:\n%s", want, workflow)
		}
	}
	assertTextOrder(t, workflow,
		"first create after observing no matching projection",
		"--allow-nonatomic --expected-absence",
		"replacement after observing the unique current body",
		"--allow-nonatomic --expected-digest <observed-sha256>",
		"absence and digest preconditions are mutually exclusive")
	if strings.Contains(workflow, "On a backend without CAS, add explicit `--allow-nonatomic --expected-digest") {
		t.Fatalf("workflow guidance still collapses create and replacement preconditions:\n%s", workflow)
	}

	propose := skillContent(t, skills, "issue-spec-propose")
	assertTextOrder(t, propose,
		"Perform the Proposal's first QUESTION discovery/create pass",
		"Upsert `proposal-choice-brief` after that pass",
		"Generate canonical SPEC comments")
	assertTextOrder(t, propose,
		"Persist the authoritative self-contained Design",
		"perform its first QUESTION discovery/create pass",
		"upsert `design-explainer` before complete TASK planning",
		"Generate TASK comments")
	for _, want := range []string{
		"Do not manufacture a question or reopen a settled choice",
		"Record each genuine unresolved decision as a blocking typed QUESTION with issue-spec question create",
		"never leave an open decision as body or projection prose",
		"keep unresolved decisions distinct from evidence-dependent items",
		"settled, needs-evidence, and needs-decision",
		"read [Human Review Projection Generation](../issue-spec-workflow/references/human-review-projections.md) completely",
		"Generate a coverage-complete `projection.md` from current authoritative inputs",
		"never rely on the reviewer already knowing omitted design information",
		"Lead with a representative human or operator scene and a concrete before/after case",
		"show how options change the case",
		"With no open decision, keep the other review dimensions visible",
		"Lead with a concrete request or operator case and observable outcome",
		"trace its normal and failure paths through architecture",
		"compatibility, rollout, risks, verification, and active SPEC traceability",
		"purposeful interaction to make the complete review surface easier to navigate",
	} {
		if !strings.Contains(propose, want) {
			t.Fatalf("propose guidance missing %q:\n%s", want, propose)
		}
	}
	assertTextOrder(t, propose,
		"representative human or operator scene",
		"problem, outcome, success signal",
		"expected SPEC coverage")
	assertTextOrder(t, propose,
		"concrete request or operator case",
		"trace its normal and failure paths",
		"architecture")

	apply := skillWithReview(t, skills, "issue-spec-apply")
	assertTextOrder(t, apply,
		"If Implement is selected, persist it",
		"perform its first QUESTION pass",
		"upsert the ordinary statusless `implement-execution-brief`",
		"then finalize the plan",
		"Author PROCESS only for managed coordination")
	for _, want := range []string{
		"typed planning state remains authoritative",
		"read [Human Review Projection Generation](../issue-spec-workflow/references/human-review-projections.md) completely",
		"Generate a coverage-complete `projection.md` from current authoritative inputs",
		"never emit only the increment since the Design",
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply guidance missing %q:\n%s", want, apply)
		}
	}
}

func TestHumanReviewProjectionReferenceIsActionableAndBounded(t *testing.T) {
	var reference string
	for _, resource := range HumanReviewProjectionResources() {
		reference += resource.Content + "\n"
	}
	for _, want := range []string{
		"issue bodies and typed artifacts as authoritative",
		"GitHub displays the fenced HTML source and does not execute",
		"proposal-choice-brief",
		"design-explainer",
		"implement-execution-brief",
		"Candidate planning",
		"correctness complexity",
		"planning aids only",
		"open decisions a human must make",
		"coverage-complete review surface",
		"not as a delta, changelog, executive summary, or component inventory",
		"human or operator scene and a concrete case",
		"Treat decision integrity as the non-negotiable constraint",
		"least cognitive effort needed to build the correct mental model and make the right decision",
		"Never make a projection easier to consume by omitting a material fact",
		"Preserve decision sufficiency",
		"what would make a choice wrong",
		"move supporting detail behind drill-down instead of deleting it",
		"not for decorative animation, novelty, or information density",
		"Build a coverage ledger before writing UI",
		"Concrete case walkthrough",
		"What the person sees",
		"What the system does",
		"What the reviewer should verify",
		"first viewport must make sense without repository-specific acronyms",
		"meaningful failure path",
		"explicit not-applicable rationale",
		"no open decision must still expose settled premises",
		"traceability to every active SPEC",
		"Decision needed",
		"```html-preview id=implement-execution-review version=1",
		"sandbox=\"allow-scripts\"",
		"prefers-reduced-motion",
		"--source-digest",
		"projection upsert",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("human review projection reference missing %q", want)
		}
	}
	assertTextOrder(t, reference,
		"Treat decision integrity as the non-negotiable constraint",
		"Within that constraint",
		"least cognitive effort needed to build the correct mental model and make the right decision")
	assertTextOrder(t, reference,
		"Preserve fidelity to authoritative facts",
		"Preserve decision sufficiency",
		"Then maximize comprehension")
	for _, forbidden := range []string{
		"projection is authoritative",
		"estimate defines PROCESS",
		"call the issue API from the iframe",
		"issue-spec-preview-init",
		"submitAnswer(",
		"QUESTION",
		"ANSWER",
	} {
		if strings.Contains(reference, forbidden) {
			t.Fatalf("human review projection reference contains forbidden guidance %q", forbidden)
		}
	}
}

func TestTopLevelProjectionUsageDistinguishesCreateAndReplacementFallbacks(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "commands", "commands.go"))
	if err != nil {
		t.Fatal(err)
	}
	usage := string(body)
	assertTextOrder(t, usage,
		"issue-spec projection upsert",
		"--allow-nonatomic --expected-absence",
		"--allow-nonatomic --expected-digest SHA256",
		"non-atomic first create uses --expected-absence",
		"replacement uses the observed-body --expected-digest SHA256")
}

func TestCheckedInCodexClaudeGuidanceMatchesTemplates(t *testing.T) {
	root := filepath.Join("..", "..")
	options := WorkflowAuthoringOptions{HTMLReviewEnabled: false}
	for _, skill := range IssueSpecSkillsWithOptions("higress-group/issue-spec", options) {
		var variants [][]byte
		for _, base := range []string{filepath.Join(".agents", "skills"), filepath.Join(".claude", "skills")} {
			path := filepath.Join(root, base, skill.Name, "SKILL.md")
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			variants = append(variants, got)
		}
		if !bytes.Equal(variants[0], variants[1]) {
			t.Fatalf("Codex/Claude skill variants differ: %s", skill.Name)
		}
		if skill.Name == "issue-spec-workflow" {
			wantPrefix := []byte(strings.TrimRight(skill.Content, "\n") + "\n\n## Project Workflow\n")
			for index, variant := range variants {
				if !bytes.HasPrefix(variant, wantPrefix) {
					t.Fatalf("checked-in workflow skill variant %d differs from the authoritative template before its generated Project Workflow notice", index)
				}
			}
		}
		for _, resource := range skill.Resources {
			var resourceVariants [][]byte
			for _, base := range []string{filepath.Join(".agents", "skills"), filepath.Join(".claude", "skills")} {
				path := filepath.Join(root, base, skill.Name, filepath.FromSlash(resource.Path))
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				resourceVariants = append(resourceVariants, got)
				if !bytes.Equal(got, []byte(resource.Content)) {
					t.Fatalf("checked-in skill resource differs from authoritative template: %s", path)
				}
			}
			if !bytes.Equal(resourceVariants[0], resourceVariants[1]) {
				t.Fatalf("Codex/Claude skill resources differ: %s/%s", skill.Name, resource.Path)
			}
		}
	}
	for _, command := range IssueSpecCommandContentsWithOptions("higress-group/issue-spec", options) {
		path := filepath.Join(root, ".claude", "commands", "issue-spec", command.ID+".md")
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		separator := []byte("\n---\n\n")
		index := bytes.Index(got, separator)
		if index < 0 {
			t.Fatalf("checked-in Claude command has no frontmatter boundary: %s", path)
		}
		body := got[index+len(separator):]
		wantPrefix := []byte(strings.TrimRight(command.Body, "\n") + "\n\n## Project Workflow\n")
		if !bytes.HasPrefix(body, wantPrefix) {
			t.Fatalf("checked-in Claude command differs from the authoritative template before its generated Project Workflow notice: %s", path)
		}
	}
	for _, base := range []string{filepath.Join(".agents", "skills"), filepath.Join(".claude", "skills")} {
		path := filepath.Join(root, base, "issue-spec-workflow", "references", "human-review-projections.md")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("disabled checked-in workflow retains projection reference %s, err=%v", path, err)
		}
	}
}

func assertTextOrder(t *testing.T, content string, values ...string) {
	t.Helper()
	offset := 0
	for _, value := range values {
		index := strings.Index(content[offset:], value)
		if index < 0 {
			t.Fatalf("content missing ordered value %q after byte %d:\n%s", value, offset, content)
		}
		offset += index + len(value)
	}
}

func TestReferenceOwnershipExamplesUseRecursiveDirectoryDeclarations(t *testing.T) {
	for _, relative := range []string{"reference.md", "reference.zh-CN.md"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "docs", relative))
		if err != nil {
			t.Fatal(err)
		}
		content := string(body)
		if !strings.Contains(content, `"owned_areas": ["internal/templates/**", "docs/reference.md"]`) ||
			!strings.Contains(content, `"write_ownership": ["internal/templates/**", "docs/reference.md"]`) {
			t.Fatalf("%s missing recursive ownership examples", relative)
		}
	}
}

func TestIssueSpecSkillsIncludeBoundedGitHubSupport(t *testing.T) {
	github := skillContent(t, IssueSpecSkills("owner/repo"), "issue-spec-github")
	for _, want := range []string{"Requires GitHub CLI (gh).", "gh auth login", "gh pr checks", "issue-spec comment create", "Never use GitHub CLI",
		"### Implementation Rationale", "gh pr comment <pr> --body-file <file>", "without treating any rationale comment as evidence or delivery acceptance",
		"line-rationale drafts", "stable path/symbol/changed-line anchor", "commit_id", "side=RIGHT", "summary/index", "path:symbol/line",
		"secret, raw payload, or credential", "invalid, stale, or sensitive drafts", "never rewrite them while claiming worker authorship"} {
		if !strings.Contains(github, want) {
			t.Fatalf("github skill missing %q", want)
		}
	}
}

func countInstructionLines(content, prefix string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

func countListItems(content string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || (len(trimmed) > 2 && trimmed[0] >= '0' && trimmed[0] <= '9' && strings.Contains(trimmed[:3], ".")) {
			count++
		}
	}
	return count
}

// Review assertions use the explicitly selected review resource, not a larger entrypoint.
func skillWithReview(t *testing.T, skills []RenderedSkill, name string) string {
	t.Helper()
	return skillContent(t, skills, name) + "\n" + skillResourceContent(t, skills, "issue-spec-workflow", "references/implementation-review.md")
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

func skillResourceContent(t *testing.T, skills []RenderedSkill, skillName, path string) string {
	t.Helper()
	for _, skill := range skills {
		if skill.Name != skillName {
			continue
		}
		for _, resource := range skill.Resources {
			if resource.Path == path {
				return resource.Content
			}
		}
		t.Fatalf("resource %q not found in skill %q", path, skillName)
	}
	t.Fatalf("skill %q not found", skillName)
	return ""
}
