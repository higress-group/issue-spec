# sandboxed-html-preview

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/331

## Requirements

### Requirement: provider-neutral HTML preview source and isolated Web execution

issue-spec MUST preserve an explicit HTML preview fence as lossless issue content, MUST keep it inert and collapsed by default, and MUST execute it only on an enabled Web surface inside a sandbox that cannot acquire issue-spec application privileges; a renderer without that capability SHALL present the source without execution.

#### Scenario: GitHub native rendering degrades to source

- **WHEN** a GitHub-backed issue contains an HTML preview fence and a user opens the issue on GitHub
- **THEN** GitHub presents the fenced source without executing its HTML, CSS, or JavaScript, and issue-spec can later read the original source unchanged

#### Scenario: enabled issue-spec Web preview is explicit

- **WHEN** a user opens an issue containing a valid HTML preview on an issue-spec Web surface where preview execution is enabled
- **THEN** the page initially shows a collapsed inert descriptor and creates the isolated preview environment only after the user explicitly runs it

#### Scenario: preview cannot inherit application authority

- **WHEN** preview JavaScript attempts to access the parent DOM, issue-spec credentials or storage, privileged APIs, forms, top-level navigation, or disallowed network destinations
- **THEN** the sandbox and content security policy deny the attempt without weakening the issue page Markdown or application security policy

#### Scenario: disabled execution capability remains readable

- **WHEN** the instance or current Web surface does not advertise executable HTML preview support
- **THEN** the issue remains readable as folded or ordinary source and no execution environment is created

#### Scenario: editing round-trips a folded preview

- **WHEN** a comment containing a preview is read or edited without expanding or running the preview
- **THEN** the stored preview fence and source bytes remain unchanged except for an explicit body replacement

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/331#issuecomment-5058654766

### Requirement: progressive CLI disclosure and bounded workflow context

The issue-spec CLI and runner MUST replace HTML preview bodies with explicit bounded descriptors before default human output, JSON output, or agent context assembly, MUST support deliberate expansion of a selected preview and an explicit lossless raw path, and MUST NOT make preview source part of core typed workflow operations unless the caller requests that exact content.

#### Scenario: default CLI output folds preview bodies

- **WHEN** a caller reads an issue or comment containing one or more HTML previews without an expansion option
- **THEN** the CLI emits each preview's stable identity, type, size, digest, omission state, and expansion guidance without emitting its HTML, CSS, or JavaScript source

#### Scenario: one preview is expanded deliberately

- **WHEN** a caller requests one exact HTML preview by its stable identity
- **THEN** the CLI returns that preview source inside the existing untrusted-content boundary while keeping unrelated previews folded

#### Scenario: machine output distinguishes omitted from absent

- **WHEN** a JSON caller reads content containing a folded preview
- **THEN** the result explicitly reports that source was omitted and includes stable metadata sufficient to request it

#### Scenario: workflow commands do not accumulate rich source

- **WHEN** proposal, design, apply, review, verify, status, or runner context assembly operates on an issue containing many HTML previews
- **THEN** the operation selects its required typed artifacts and prose without placing preview bodies in command output or agent context

#### Scenario: raw access remains explicit and lossless

- **WHEN** an authorized caller deliberately requests the raw issue or comment body for debugging, export, or exact editing
- **THEN** the CLI returns the complete original backend body under the existing untrusted-content and secret-redaction contract

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/331#issuecomment-5058655346

### Requirement: opaque preview parsing and resource budgets

issue-spec MUST treat HTML preview contents as opaque non-typed data for marker and command recognition, and an executable preview implementation MUST enforce configured source, concurrency, lifecycle, output, and external-resource budgets without blocking normal issue reading or workflow progress.

#### Scenario: workflow-shaped preview text stays inert

- **WHEN** an HTML preview contains issue-spec markers, canonical SPEC headings, slash commands, review text, or agent-like instructions
- **THEN** typed-artifact projection, runner command intake, workflow transitions, and agent context selection do not recognize those strings as semantic input

#### Scenario: previews never auto-run

- **WHEN** an issue with multiple previews loads, rerenders, receives a clock update, or is processed by search, notification, or workflow code
- **THEN** no preview JavaScript executes until a user explicitly runs one preview

#### Scenario: active preview count remains bounded

- **WHEN** a user runs previews beyond the configured per-page active limit or collapses an active preview
- **THEN** issue-spec tears down or refuses excess sandbox environments while the issue page and stored source remain available

#### Scenario: oversized or long-running content fails locally

- **WHEN** preview source exceeds the configured size limit or an active preview exceeds its lifecycle or output budget
- **THEN** issue-spec stops or declines that preview with an actionable local diagnostic and does not fail the issue, comment, or workflow operation

#### Scenario: external resources are denied by default

- **WHEN** preview content requests a network, navigation, form, media, or embedded resource not allowed by instance policy
- **THEN** the preview sandbox blocks the request and reports a bounded diagnostic without forwarding application credentials

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/331#issuecomment-5058655974

### Requirement: dual-audience Design authoring with QUESTION and ANSWER history

When the selected repository profile advertises Interactive Design Explainer support, issue-spec Design authoring guidance MUST maintain one ordinary human-facing explainer comment after the persisted Design has completed its first QUESTION discovery pass. The explainer MAY precede TASK planning, MUST surface each current Design-phase QUESTION with its latest typed ANSWER and optional answer history, MUST have no independent persisted lifecycle status, and MUST remain non-authoritative and outside typed workflow gates and default Agent context.

#### Scenario: Design projection follows first QUESTION discovery and precedes TASK planning

- **WHEN** a Design body has been persisted, its first QUESTION discovery pass has created all currently identified QUESTION artifacts, and TASK planning is not complete
- **THEN** the authoring flow may create the one logical explainer from the Design, confirmed SPEC facts, current QUESTION set, and any linked ANSWER comments without waiting for TASK artifacts

#### Scenario: unanswered Design questions receive human decision treatment

- **WHEN** the Design has an active QUESTION with no valid ANSWER
- **THEN** the explainer presents the question, grounded options or evidence, and exact requested decision prominently instead of treating questions as Proposal-only content

#### Scenario: answered Design questions show the latest choice

- **WHEN** an active Design QUESTION has one or more valid typed ANSWER comments
- **THEN** the explainer shows the latest ANSWER as effective with actor and time and allows earlier ANSWER comments to be inspected as decision history

#### Scenario: QUESTION and ANSWER changes update one logical explainer

- **WHEN** a Design-phase QUESTION or ANSWER is created or the QUESTION is updated or the Design or confirmed SPEC set changes
- **THEN** regeneration binds the current aggregate inputs and updates the same logical explainer comment instead of appending a duplicate active explainer

#### Scenario: the explainer has no independent status

- **WHEN** the explainer is created, rendered, or regenerated
- **THEN** it does not persist draft, review-ready, stale, or another projection lifecycle state and normal current content is shown without a status badge

#### Scenario: the explainer remains non-authoritative

- **WHEN** a workflow gate, Agent, or reviewer encounters disagreement between Design or typed QUESTION and ANSWER artifacts and the explainer
- **THEN** the Design and typed artifacts remain authoritative and the explainer is ignored as semantic evidence

#### Scenario: core workflow context stays bounded

- **WHEN** a core issue-spec workflow command assembles Agent context for a Design with an explainer
- **THEN** it excludes the explainer source and selects only required typed Design-phase QUESTION and effective ANSWER data

#### Scenario: unsupported profiles keep the canonical Design complete

- **WHEN** a Design is viewed on a surface without executable preview support
- **THEN** the canonical Design, QUESTION, and ANSWER comments remain complete and reviewable as Markdown and the workflow does not claim that an executable explainer is available

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/331#issuecomment-5060253266

### Requirement: dual-audience Proposal choice briefs with QUESTION and ANSWER history

When the selected repository profile advertises Proposal Choice Brief support, issue-spec Proposal authoring guidance MUST maintain one ordinary human-facing Choice Brief after the persisted Proposal has completed its first QUESTION discovery pass. The brief MAY precede complete SPEC authoring, MUST surface each current Proposal-phase QUESTION with its latest typed ANSWER and optional answer history, MUST have no independent persisted lifecycle status, and MUST remain non-authoritative and outside workflow gates and default Agent context.

#### Scenario: Proposal projection follows first QUESTION discovery and precedes complete SPEC authoring

- **WHEN** a Proposal body has been persisted, its first QUESTION discovery pass has created all currently identified QUESTION artifacts, and SPEC authoring is not complete
- **THEN** the authoring flow may create the one logical Choice Brief from current Proposal, QUESTION, ANSWER, and SPEC facts without waiting for the full SPEC set

#### Scenario: unanswered Proposal questions request human judgment

- **WHEN** the Proposal has an active QUESTION with no valid ANSWER
- **THEN** the Choice Brief presents the question, grounded options or evidence gaps, and exact requested decision prominently before the Proposal advances

#### Scenario: answered Proposal questions use the latest choice

- **WHEN** an active Proposal QUESTION has one or more valid typed ANSWER comments
- **THEN** the Choice Brief presents the latest ANSWER as the effective decision with actor and time and allows earlier answers to be inspected as history

#### Scenario: settled choices are not presented as open decisions

- **WHEN** a choice is confirmed by the Proposal, active typed artifacts, or the effective ANSWER and has an explicitly established better option
- **THEN** the brief presents it as settled, explains the supporting premise and alternative costs, and asks the reviewer to verify the boundary rather than decide it again

#### Scenario: unresolved and evidence-dependent choices stay distinct

- **WHEN** a choice has an unanswered active QUESTION, is delegated to a later stage, or depends on unavailable evidence
- **THEN** the brief separates it from settled choices, labels recommendations as recommendations, compares credible options, shows evidence gaps, and states the decision still required

#### Scenario: QUESTION and ANSWER changes update one logical brief

- **WHEN** a Proposal-phase QUESTION or ANSWER is created or the QUESTION is updated or the Proposal body, active SPEC set, or relevant evidence changes
- **THEN** regeneration updates the same logical Choice Brief comment instead of appending a duplicate active brief

#### Scenario: decision state remains content semantics

- **WHEN** the brief presents settled, needs-evidence, and needs-decision choices
- **THEN** those labels describe the authoritative decisions being reviewed rather than a lifecycle of the Choice Brief comment

#### Scenario: generated decision support remains faithful

- **WHEN** Proposal authoring generates recommendations, alternatives, benefits, or costs
- **THEN** each item is grounded in current authoritative records, synthesis is labeled, confirmed choices are not reopened, and unanswered recommendations are not presented as confirmed

#### Scenario: core workflow context excludes the Choice Brief source

- **WHEN** a core issue-spec workflow command assembles Agent context
- **THEN** it excludes the Choice Brief source and selects required typed Proposal-phase QUESTION and effective ANSWER data

#### Scenario: unsupported profiles keep the Proposal complete

- **WHEN** the selected surface does not advertise Proposal Choice Brief support
- **THEN** the canonical Proposal, SPEC, QUESTION, and ANSWER comments remain complete and reviewable without claiming an executable Choice Brief

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/331#issuecomment-5060518492

### Requirement: dual-audience Implement execution briefs with QUESTION and ANSWER history

When the selected repository profile advertises Implementation Execution Brief support, issue-spec Implement authoring guidance MUST maintain one ordinary human-facing Execution Brief after the persisted Implement issue has completed its first QUESTION discovery pass. The brief MAY precede complete PROCESS planning, MUST surface each current Implement-phase QUESTION with its latest typed ANSWER and optional answer history, MUST derive execution data from Design invariants, TASK, PROCESS, and evidence records, and MUST remain non-authoritative and outside workflow gates and default Agent context.

#### Scenario: Implement projection follows first QUESTION discovery and precedes complete PROCESS planning

- **WHEN** an Implement issue has been persisted, its first QUESTION discovery pass has created all currently identified QUESTION artifacts, and PROCESS planning is not complete
- **THEN** the authoring flow may create the one logical Execution Brief from current QUESTION and ANSWER data with candidate PROCESS boundaries and Agent allocation labeled as synthesis

#### Scenario: unanswered Implement questions block affected decisions

- **WHEN** implementation planning exposes an active QUESTION with no valid ANSWER
- **THEN** the Execution Brief presents the question, its DAG or acceptance impact, grounded options, and exact requested decision prominently before affected work is dispatched

#### Scenario: answered Implement questions use the latest choice

- **WHEN** an active Implement QUESTION has one or more valid typed ANSWER comments
- **THEN** the Execution Brief uses the latest ANSWER as the effective planning decision and makes earlier answers available as history

#### Scenario: QUESTION and ANSWER changes update execution planning

- **WHEN** an Implement-phase QUESTION or ANSWER is created or the QUESTION is updated
- **THEN** regeneration updates the same logical Execution Brief and recalculates blocked, ready, and decision-dependent presentation from authoritative workflow data

#### Scenario: persisted PROCESS data replaces candidate planning

- **WHEN** PROCESS artifacts, dependencies, links, or handoffs are added or transitioned
- **THEN** regeneration updates the same logical Execution Brief from the current typed DAG and does not retain a conflicting candidate plan as authoritative

#### Scenario: top-level execution review directs attention

- **WHEN** a human opens the Execution Brief
- **THEN** the view presents unanswered questions, effective answers, completed, active, blocked, next, conditional, safely parallel, and independent-check work with structural blockers and shared-touchpoint warnings

#### Scenario: PROCESS drill-down explains review obligations

- **WHEN** a reviewer selects one PROCESS
- **THEN** the view discloses its Design invariant, dependencies, role recommendation, change surface, shared touchpoints, SPEC coverage, verification obligations, estimates, and human-review focus with authoritative links

#### Scenario: correctness complexity is distinct from size

- **WHEN** the view presents complexity for a PROCESS
- **THEN** it labels semantic reasoning difficulty as correctness complexity and explains it separately from change-surface, verification, rollout, and coordination complexity

#### Scenario: estimates do not define workflow semantics

- **WHEN** the brief displays code-volume estimates, confidence, complexity, Agent count, or parallelization suggestions
- **THEN** it marks them as non-authoritative planning aids and does not use them to define PROCESS boundaries, readiness, or gates

#### Scenario: core workflow context excludes the Execution Brief source

- **WHEN** apply, review, verify, status, runner, scheduling, or context assembly runs
- **THEN** it excludes the Execution Brief source and selects authoritative TASK, PROCESS, QUESTION, and effective ANSWER data directly

#### Scenario: unsupported profiles keep Implement authoritative

- **WHEN** the selected surface does not advertise Implementation Execution Brief support
- **THEN** the canonical Implement, TASK, PROCESS, QUESTION, and ANSWER comments remain complete and reviewable without claiming an interactive execution view

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/331#issuecomment-5060801079

### Requirement: self-host interactive QUESTION answering with append-only typed ANSWER comments

When a capable self-host Web surface renders a phase projection containing active typed QUESTION artifacts, issue-spec MUST support bounded single-choice, multiple-choice, and optional custom-answer controls. Each confirmed submission MUST create one new immutable typed ANSWER comment containing the QUESTION snapshot and human choice. For an active QUESTION the latest valid ANSWER by provider-authoritative comment order MUST be the effective decision consumed by later Agents and gates.

#### Scenario: QUESTION choice metadata supports single and multiple selection

- **WHEN** a typed QUESTION declares stable options and answer mode single or multiple
- **THEN** the projection renders radio controls requiring exactly one option for single mode or checkbox controls requiring one or more unique options for multiple mode while preserving option ID, label, description, and tradeoff

#### Scenario: custom input replaces unsatisfactory predefined options

- **WHEN** a QUESTION allows a custom answer and the user chooses the custom path because no predefined option is satisfactory
- **THEN** the projection clears predefined selections, requires non-empty custom text, and records the answer as custom without inventing an option ID

#### Scenario: QUESTION updates do not rewrite answer history

- **WHEN** a QUESTION comment or its generated review view is updated and the user submits a choice again
- **THEN** issue-spec creates a new ANSWER that snapshots the then-current QUESTION while every prior ANSWER preserves its earlier QUESTION snapshot, without introducing a QUESTION revision or overwriting an ANSWER

#### Scenario: QUESTIONS work in every authoring phase

- **WHEN** an active typed QUESTION belongs to a Proposal, Design, or Implement issue
- **THEN** the corresponding human-facing projection can present the same selection and submission behavior without changing phase authority

#### Scenario: sandbox emits only a structured answer intent

- **WHEN** the user submits a valid selection inside the preview iframe
- **THEN** the iframe sends a versioned message containing issue binding, QUESTION ID, answer mode, selected option IDs, and custom text and receives no credentials, CSRF token, same-origin privilege, or direct issue API access

#### Scenario: trusted host confirms and authorizes the answer

- **WHEN** the parent page receives a QUESTION answer intent
- **THEN** it validates message source and schema, reloads the active QUESTION by ID, verifies permission and CSRF protection, validates the option model, shows the user a confirmation, and only then creates a typed ANSWER through a same-origin endpoint

#### Scenario: ANSWER captures the complete decision snapshot

- **WHEN** an authorized user confirms an answer
- **THEN** the new typed ANSWER records a unique ANSWER ID, QUESTION ID and issue, complete QUESTION text and choice-model snapshot, selected IDs and labels or custom text, authenticated actor, and provider-authoritative creation time

#### Scenario: a later answer changes the effective choice without rewriting history

- **WHEN** a user submits another valid answer for the same active QUESTION
- **THEN** issue-spec appends another typed ANSWER, preserves every earlier ANSWER unchanged, and treats the latest valid ANSWER by provider comment order with stable comment-ID tie-break as effective

#### Scenario: repeated identical submissions are semantically harmless

- **WHEN** an accidental retry or repeated click creates another identical ANSWER
- **THEN** the latest-answer rule yields the same effective decision and the client disables the submit control after success to reduce duplicate timeline noise without requiring an idempotency protocol

#### Scenario: active blocking QUESTION is satisfied by an ANSWER

- **WHEN** an active blocking QUESTION has at least one valid typed ANSWER
- **THEN** workflow gates and Agents treat it as answered using the latest valid ANSWER for that QUESTION ID, while earlier ANSWER comments remain immutable timeline history

#### Scenario: later Agents select typed ANSWER directly

- **WHEN** a later Agent or gate needs the user's decision
- **THEN** it selects the active QUESTION and latest typed ANSWER without scanning ordinary comments, earlier answers unless history is requested, or rich preview source

#### Scenario: custom text remains untrusted display data

- **WHEN** a custom answer contains Markdown, HTML, command-shaped text, or issue-spec marker-shaped text
- **THEN** the typed ANSWER preserves it as safely encoded data and prevents it from creating commands, previews, or additional typed artifacts

#### Scenario: unsupported surfaces do not expose a false submit action

- **WHEN** the projection is viewed on GitHub or another surface without a trusted ANSWER bridge
- **THEN** QUESTION and ANSWER source remains readable as Markdown, no functional submit control is promised, and answers may be created through a supported provider workflow

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/331#issuecomment-5060936620
