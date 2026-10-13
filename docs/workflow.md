# Planning, implementation and human handoff

The current agent can own authorized planning, implementation and self-tests in
one session. Design, TASK and PROCESS do not require another writer. Projects
may add a coordinator-only role and stage review in their own guidance; those
roles are not issue-spec defaults. Host capabilities and actual user authorization
still determine delegation, publication, push, approval and merge.

## Select only useful planning

Use a simple Issue for a bounded change. Add Proposal, Design, Implement, SPEC,
TASK or PROCESS when product decisions, durable contracts or concrete execution
risk justify them. File count and agent count are not semantic boundaries.
Managed PROCESS serves concurrent writers, isolation protecting existing work,
path ownership, restartable handoff or dependency-ordered integration.

For a selected phase: persist its body, discover genuine blocking QUESTIONs,
create the human review projection when enabled, then author selected typed
children. Plain QUESTION decisions use resolution; choice-model questions use
ANSWER. Do not invent questions or choices to record settled private details.

## Read, implement and validate

Read the complete current Design and applicable decisions. Discover TASKs in a
compact index, then expand selected TASKs, required SPECs and dependencies. A
summary never replaces normative content. Use `search issues --scope discussion`
for related ordinary discussions; default Proposal discovery remains available.

The owner implements and self-tests in its assigned checkout, updates durable
specs and reports the actual candidate. Managed work preserves exact base,
ownership, worktree isolation, DCO, generators, tests, dependency order and
bounded handoff. These controls do not certify delivery acceptance.

Select checks for changed behavior and explicit project gates. Expand after a
change, failure or concrete unresolved risk. Keep evidence tied to its actual
revision; repairs require affected checks. Self-tests are not independent review.

## Project rules and review

Shared workflow text is generated under `.agents/skills` and linked individually
into `.claude/skills`. Read `issue-spec/config.yaml` for context/rules and load only
`guidance.shared` plus the current `propose`, `apply` or `review` guidance. Projects
own those supplements and their specialized skills. See [safe updates](reference.md#shared-skills-and-safe-updates).

Follow the requested review milestone. A project may centralize review under its
coordinator after several changes; another may request a separate reviewer.
Issue-spec does not automatically spawn reviewers or require a PR/MR. Review the
actual candidate and existing evidence, name concrete failures, route repairs
to responsible owners and recheck affected behavior.

## Authorized publication and handoff

Report candidate/base, outcome, checks/results, gaps, risks and remaining review
needs. Create a provider change or publish comments only when authorized and
supported. Requested rationale may use an ordinary `### Implementation Rationale`
comment, with stable `path:symbol/line` anchors for non-obvious decisions. Preserve
actual authorship and validate anchors against the published head. There is no
quota or mandatory rationale ceremony. Report failed writes with their content.

Approval and merge follow actual authorization and current provider policy.
No readiness receipt or agent-authored merge authority is created.

## Provider operations and historical data

`issue-spec.code-provider/v1` exposes operation-scoped `change.create`,
`change.comment` and `evidence.snapshot`. Any implemented subset is valid;
missing capabilities constrain only those actions. Snapshots are audit/navigation
context, not a reconstruction of provider merge policy.

Historical REVIEW, VERIFY, rationale evidence, finalization, receipts and Archive
remain explicit audit data where supported. Retired writers return
`deprecated_workflow` before mutation. They are not current delivery steps.
