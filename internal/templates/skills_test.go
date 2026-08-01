package templates

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssueSpecSkillAndCommandTemplates(t *testing.T) {
	skills := IssueSpecSkills("owner/repo")
	if got, want := len(skills), 6; got != want {
		t.Fatalf("skills = %d, want %d", got, want)
	}
	if !strings.Contains(skills[0].Content, `generatedBy: "issue-spec"`) {
		t.Fatalf("skill missing generatedBy:\n%s", skills[0].Content)
	}
	commands := IssueSpecCommandContents("owner/repo")
	if got, want := len(commands), 4; got != want {
		t.Fatalf("commands = %d, want %d", got, want)
	}
	if commands[0].ID != "propose" || !strings.Contains(commands[0].Body, "owner/repo") {
		t.Fatalf("unexpected first command: %+v", commands[0])
	}
	for _, skill := range skills {
		if skill.Name == "issue-spec-archive" || strings.Contains(skill.Content, "Issue Spec Archive") {
			t.Fatalf("normal generated skills still expose Archive: %s", skill.Name)
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

func TestCoordinatorGuidanceKeepsActionsStopsAndRecovery(t *testing.T) {
	workflow := skillContent(t, IssueSpecSkills("owner/repo"), "issue-spec-workflow")
	wants := []string{
		"status --repo owner/repo", "--summary --json", "structured detail action", "full --json",
		"comment get", "active/status/history filters", "narrow direct-PR fast path",
		"<TYPE>-<issue><three-digit sequence>", "QUESTION-1001", "QUESTION-44001",
		"do not add another type digit or search the whole repository", "never renumber a legacy ID",
		"one independently verifiable Design invariant", "bounded context and working set", "stable interface",
		"stop the Implement transition as blocked", "acceptance consequences", "request human direction",
		"workspace prepare -> real non-Coordinator", "never implements/tests/commits", "sealed assignment",
		"design_context.source_url", "without comments, timeline, history, or gates", "stops on any conflict",
		"Exact revision, ownership, DCO, required tests", "independent review", "review/fix convergence",
		"same plan digest and checkpoint", "Cleanup is explicit", "destructive", "retain uncertain or unintegrated work",
		"verify --summary --json", "authoritative full final verify", "pr link-issues is the final PR-body write",
		"durable-spec preview/apply", "issue-spec/durable-spec", "issue close-change",
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
				"assigned generators", "focused verification", "exactly one DCO commit", "bounded handoff/result receipt",
				"result-revision binding", "assigned_selector and resolved_revision", "Any amendment changes the result revision",
				"Literal selectors retain their exact command identity"},
			forbidden: []string{"workflow workspace prepare", "workflow reconcile", "pr link-issues", "code-change attach", "archive durable-spec", "SPEC <-> TASK"},
		},
		{
			name: "issue-spec-review",
			required: []string{"sealed review assignment", "exact subject revision", "immutable snapshot/diff",
				"design_context.read_mode=complete-issue-body", "read the complete Design", "Stop on conflict",
				"subject-revision-bound required test", "assigned_selector plus resolved_revision", "Preserve literal selectors byte-for-byte",
				"actionable findings", "explicit no-finding verdict", "bounded review receipt/sync result",
				"issue-spec link --repo owner/repo --from REVIEW-<n> --from-issue <implement-issue> --to PROCESS-<n> --to-issue <implement-issue>",
				"issue-spec link --repo owner/repo --from REVIEW-<n> --from-issue <implement-issue> --to SPEC-<n> --to-issue <proposal-issue>"},
			forbidden: []string{"workflow workspace prepare", "workflow reconcile", "pr link-issues", "code-change attach", "archive durable-spec", "SPEC <-> TASK"},
		},
		{
			name: "issue-spec-verify",
			required: []string{"sealed verification assignment", "exact immutable subject revision", "required focused tests/checks",
				"subject-revision-bound required test", "assigned_selector plus resolved_revision", "Preserve literal selectors byte-for-byte",
				"provider-owned check identity", "bounded VERIFY receipt", "Do not collect or pass runtime-specific session IDs"},
			forbidden: []string{"workflow workspace prepare", "workflow reconcile", "pr link-issues", "code-change attach", "archive durable-spec", "SPEC <-> TASK"},
		},
	}
	for _, tc := range cases {
		content := skillContent(t, skills, tc.name)
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
}

func TestGeneratedGuidanceDeterministicSizeBudgets(t *testing.T) {
	type budget struct {
		maxBytes    int
		maxHeadings int
		maxItems    int
	}
	budgets := map[string]budget{
		"issue-spec-workflow": {maxBytes: 10000, maxHeadings: 8, maxItems: 40},
		"issue-spec-propose":  {maxBytes: 5000, maxHeadings: 4, maxItems: 16},
		"issue-spec-apply":    {maxBytes: 5500, maxHeadings: 5, maxItems: 12},
		"issue-spec-review":   {maxBytes: 5000, maxHeadings: 5, maxItems: 12},
		"issue-spec-verify":   {maxBytes: 4000, maxHeadings: 5, maxItems: 10},
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
	workflow := skillContent(t, skills, "issue-spec-workflow")
	for _, want := range []string{
		"persist the phase issue body, perform the first QUESTION discovery/create pass, upsert the human review projection",
		"SPEC for Proposal, TASK for Design, PROCESS for Implement",
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
		"build the Markdown fallback, the single `html-preview` review surface, source digest, coverage audit, and validation checks",
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
		"generate a coverage-complete `projection.md` from current authoritative inputs",
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

	apply := skillContent(t, skills, "issue-spec-apply")
	assertTextOrder(t, apply,
		"persist the Implement issue",
		"perform its first QUESTION discovery/create pass",
		"upsert the ordinary statusless `implement-execution-brief`",
		"before completing PROCESS planning")
	for _, want := range []string{
		"opens with a concrete acceptance case",
		"human-visible outcome",
		"PROCESS sequence carries it from trigger to verification",
		"current DAG, state counts, critical path, safe parallelism",
		"roles, per-node SPEC/scenario coverage",
		"shared touchpoints, blockers, tests, generators",
		"Estimates never define workflow semantics",
		"Issue bodies and typed artifacts remain authoritative",
		"projection source stays outside default Agent context",
		"read [Human Review Projection Generation](../issue-spec-workflow/references/human-review-projections.md) completely",
		"generate a coverage-complete `projection.md` from current authoritative inputs",
		"never emit only the increment since the Design",
		"tests, generators, and independent review/verify obligations",
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply guidance missing %q:\n%s", want, apply)
		}
	}
	assertTextOrder(t, apply,
		"concrete acceptance case",
		"PROCESS sequence",
		"current DAG")
}

func TestHumanReviewProjectionReferenceIsActionableAndBounded(t *testing.T) {
	reference := skillResourceContent(t, IssueSpecSkills("owner/repo"), "issue-spec-workflow", "references/human-review-projections.md")
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
	for _, skill := range IssueSpecSkills("higress-group/issue-spec") {
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
	for _, command := range IssueSpecCommandContents("higress-group/issue-spec") {
		path := filepath.Join(root, ".claude", "commands", "issue-spec", command.ID+".md")
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		_ = got // PROCESS-008 refreshes managed generated assets from these templates.
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
	for _, want := range []string{"Requires GitHub CLI (gh).", "gh auth login", "gh pr checks", "issue-spec comment create", "Never use GitHub CLI"} {
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
