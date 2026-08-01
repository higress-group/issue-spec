# sandboxed-html-preview

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/331

## Requirements

### Requirement: provider-neutral HTML preview source and isolated Web execution

issue-spec MUST preserve an explicit HTML preview fence as lossless issue content, MUST render every valid preview directly on an enabled Web surface inside a sandbox that cannot acquire issue-spec application privileges, and MUST let a renderer without that capability present the source without execution.

#### Scenario: GitHub native rendering degrades to source

- **WHEN** a GitHub-backed issue contains an HTML preview fence and a user opens the issue on GitHub
- **THEN** GitHub presents the fenced source without executing its HTML, CSS, or JavaScript, and issue-spec can later read the original source unchanged

#### Scenario: enabled issue-spec Web previews render directly

- **WHEN** a user opens an issue containing a valid HTML preview on an issue-spec Web surface where preview execution is enabled
- **THEN** the page renders the isolated preview iframe in place without a descriptor card, lifecycle controls, or a per-page active-preview limit

#### Scenario: preview cannot inherit application authority

- **WHEN** preview JavaScript attempts to access the parent DOM, issue-spec credentials or storage, privileged APIs, forms, top-level navigation, or disallowed network destinations
- **THEN** the sandbox and content security policy deny the attempt without weakening the issue page Markdown or application security policy

#### Scenario: disabled execution capability remains readable

- **WHEN** the instance or current Web surface does not advertise executable HTML preview support
- **THEN** the issue remains readable as folded or ordinary source and no execution environment is created

#### Scenario: editing round-trips a folded preview

- **WHEN** a comment containing a preview is read or edited without expanding its source
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

issue-spec MUST treat HTML preview contents as opaque non-typed data for marker and command recognition, and an executable preview implementation MUST enforce configured source, dimensions, and external-resource budgets without blocking normal issue reading or workflow progress.

#### Scenario: workflow-shaped preview text stays inert

- **WHEN** an HTML preview contains issue-spec markers, canonical SPEC headings, slash commands, review text, or agent-like instructions
- **THEN** typed-artifact projection, runner command intake, workflow transitions, and agent context selection do not recognize those strings as semantic input

#### Scenario: every valid preview renders without lifecycle controls

- **WHEN** an issue with multiple previews loads, rerenders, receives a clock update, or is processed by search, notification, or workflow code
- **THEN** every valid preview on the enabled issue page renders directly, unchanged frames survive unrelated rerenders, and search, notification, and workflow code still do not execute preview source

#### Scenario: preview count does not suppress valid review surfaces

- **WHEN** an issue or comment contains multiple individually valid previews
- **THEN** issue-spec renders each preview without a first-preview selection rule or active-frame slot limit

#### Scenario: oversized content fails locally

- **WHEN** one preview source exceeds the configured size or dimension limit
- **THEN** issue-spec declines that preview with an actionable local diagnostic and does not fail the issue, comment, or workflow operation

#### Scenario: external resources are denied by default

- **WHEN** preview content requests a network, navigation, form, media, or embedded resource not allowed by instance policy
- **THEN** the preview sandbox blocks the request and reports a bounded diagnostic without forwarding application credentials

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/331#issuecomment-5058655974

### Requirement: dual-audience Design authoring explainer

When repository HTML review authoring is enabled and the selected repository profile advertises Interactive Design Explainer support, issue-spec Design authoring guidance MUST maintain one ordinary human-facing explainer comment after the persisted Design has completed its first QUESTION discovery pass. The explainer MAY precede TASK planning, MUST stay QUESTION-free because each typed QUESTION comment owns its presentation and answering through the native answer panel, MUST have no independent persisted lifecycle status, and MUST remain non-authoritative and outside typed workflow gates and default Agent context.

#### Scenario: disabled Design review skips projection loading

- **WHEN** repository configuration sets `html_review.enabled: false`
- **THEN** generated Design authoring guidance and the built-in Design issue body SHALL omit the projection checkpoint and reference loading while QUESTION discovery proceeds directly to TASK planning

#### Scenario: enabled Design review keeps the current checkpoint

- **WHEN** `html_review` is absent or explicitly enabled and the selected profile supports the Design explainer
- **THEN** Design authoring SHALL keep the projection checkpoint after its first QUESTION discovery pass and before complete TASK planning

#### Scenario: Design projection follows first QUESTION discovery and precedes TASK planning

- **WHEN** a Design body has been persisted, its first QUESTION discovery pass has created all currently identified QUESTION artifacts, and TASK planning is not complete
- **THEN** the authoring flow may create the one logical explainer from the Design and confirmed SPEC facts without waiting for TASK artifacts

#### Scenario: Design questions live on their typed comments, not in the explainer

- **WHEN** the Design has an active QUESTION, answered or not
- **THEN** the typed QUESTION comment's native answer panel presents the options and the latest effective ANSWER while the explainer explains data, flow, alternatives, and correctness constraints without duplicating the QUESTION or embedding answer controls

#### Scenario: Design and SPEC changes update one logical explainer

- **WHEN** the Design body or confirmed SPEC set changes
- **THEN** regeneration binds the current aggregate inputs and updates the same logical explainer comment instead of appending a duplicate active explainer, while QUESTION and ANSWER changes refresh the native panel without requiring explainer regeneration

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
- https://github.com/higress-group/issue-spec/pull/338

### Requirement: dual-audience Proposal choice briefs

When repository HTML review authoring is enabled and the selected repository profile advertises Proposal Choice Brief support, issue-spec Proposal authoring guidance MUST maintain one ordinary human-facing Choice Brief after the persisted Proposal has completed its first QUESTION discovery pass. The brief MAY precede complete SPEC authoring, MUST stay QUESTION-free because each typed QUESTION comment owns its presentation and answering through the native answer panel, MUST have no independent persisted lifecycle status, and MUST remain non-authoritative and outside workflow gates and default Agent context.

#### Scenario: disabled Proposal review skips projection loading

- **WHEN** repository configuration sets `html_review.enabled: false`
- **THEN** generated Proposal authoring guidance and the built-in Proposal issue body SHALL omit the projection checkpoint and reference loading while QUESTION discovery proceeds directly to SPEC authoring

#### Scenario: enabled Proposal review keeps the current checkpoint

- **WHEN** `html_review` is absent or explicitly enabled and the selected profile supports the Proposal Choice Brief
- **THEN** Proposal authoring SHALL keep the projection checkpoint after its first QUESTION discovery pass and before complete SPEC authoring

#### Scenario: Proposal projection follows first QUESTION discovery and precedes complete SPEC authoring

- **WHEN** a Proposal body has been persisted, its first QUESTION discovery pass has created all currently identified QUESTION artifacts, and SPEC authoring is not complete
- **THEN** the authoring flow may create the one logical Choice Brief from current Proposal and SPEC facts without waiting for the full SPEC set

#### Scenario: Proposal questions live on their typed comments, not in the brief

- **WHEN** the Proposal has an active QUESTION, answered or not
- **THEN** the typed QUESTION comment's native answer panel presents the options and the latest effective ANSWER while the Choice Brief presents the underlying decision context without duplicating the QUESTION or embedding answer controls

#### Scenario: settled choices are not presented as open decisions

- **WHEN** a choice is confirmed by the Proposal, active typed artifacts, or the effective ANSWER and has an explicitly established better option
- **THEN** the brief presents it as settled, explains the supporting premise and alternative costs, and asks the reviewer to verify the boundary rather than decide it again

#### Scenario: unresolved and evidence-dependent choices stay distinct

- **WHEN** a choice has an unanswered active QUESTION, is delegated to a later stage, or depends on unavailable evidence
- **THEN** the brief separates it from settled choices, labels recommendations as recommendations, compares credible options, shows evidence gaps, and states the decision still required

#### Scenario: QUESTION and ANSWER changes stay on the native panel

- **WHEN** a Proposal-phase QUESTION or ANSWER is created or the QUESTION is updated
- **THEN** the typed QUESTION comment's native panel reflects the change, while the same logical Choice Brief is regenerated only when the Proposal body, active SPEC set, or relevant evidence changes

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
- https://github.com/higress-group/issue-spec/pull/338

### Requirement: dual-audience Implement execution briefs

When repository HTML review authoring is enabled and the selected repository profile advertises Implementation Execution Brief support, issue-spec Implement authoring guidance MUST maintain one ordinary human-facing Execution Brief after the persisted Implement issue has completed its first QUESTION discovery pass. The brief MAY precede complete PROCESS planning, MUST stay QUESTION-free because each typed QUESTION comment owns its presentation and answering through the native answer panel, MUST derive execution data from Design invariants, TASK, PROCESS, and evidence records, and MUST remain non-authoritative and outside workflow gates and default Agent context.

#### Scenario: disabled Implement review skips projection loading

- **WHEN** repository configuration sets `html_review.enabled: false`
- **THEN** generated Implement authoring guidance and the built-in Implement issue body SHALL omit the projection checkpoint and reference loading while QUESTION discovery proceeds directly to PROCESS planning and independent code review remains mandatory

#### Scenario: enabled Implement review keeps the current checkpoint

- **WHEN** `html_review` is absent or explicitly enabled and the selected profile supports the Implementation Execution Brief
- **THEN** Implement authoring SHALL keep the projection checkpoint after its first QUESTION discovery pass and before complete PROCESS planning

#### Scenario: Implement projection follows first QUESTION discovery and precedes complete PROCESS planning

- **WHEN** an Implement issue has been persisted, its first QUESTION discovery pass has created all currently identified QUESTION artifacts, and PROCESS planning is not complete
- **THEN** the authoring flow may create the one logical Execution Brief with candidate PROCESS boundaries and Agent allocation labeled as synthesis

#### Scenario: Implement questions live on their typed comments, not in the brief

- **WHEN** implementation planning exposes an active QUESTION, answered or not
- **THEN** the typed QUESTION comment's native answer panel presents the options and the latest effective ANSWER while the Execution Brief presents actual typed blockers and execution context without duplicating the QUESTION or embedding answer controls

#### Scenario: persisted PROCESS data replaces candidate planning

- **WHEN** PROCESS artifacts, dependencies, links, or handoffs are added or transitioned
- **THEN** regeneration updates the same logical Execution Brief from the current typed DAG and does not retain a conflicting candidate plan as authoritative

#### Scenario: top-level execution review directs attention

- **WHEN** a human opens the Execution Brief
- **THEN** the view presents completed, active, blocked, next, conditional, safely parallel, and independent-check work with structural blockers and shared-touchpoint warnings

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
- https://github.com/higress-group/issue-spec/pull/338

### Requirement: self-host interactive QUESTION answering with append-only typed ANSWER comments

When a capable self-host Web surface shows a typed QUESTION comment carrying a choice model, issue-spec MUST render a native, default-visible answer panel under that comment with bounded single-choice, multiple-choice, and optional custom-answer controls, and MUST show the latest effective ANSWER when one exists. Generic HTML previews are display-only and MUST NOT participate in QUESTION submission. Each valid native submission MUST create one new immutable typed ANSWER comment containing the QUESTION snapshot and human choice. For an active QUESTION the latest valid ANSWER by provider-authoritative comment order MUST be the effective decision consumed by later Agents and gates.

#### Scenario: QUESTION choice metadata supports single and multiple selection

- **WHEN** a typed QUESTION declares stable options and answer mode single or multiple
- **THEN** the native answer panel renders radio controls requiring exactly one option for single mode or checkbox controls requiring one or more unique options for multiple mode while preserving option ID, label, description, and tradeoff

#### Scenario: custom input replaces unsatisfactory predefined options

- **WHEN** a QUESTION allows a custom answer and the user chooses the custom path because no predefined option is satisfactory
- **THEN** the answer surface clears predefined selections, requires non-empty custom text, and records the answer as custom without inventing an option ID

#### Scenario: QUESTION updates do not rewrite answer history

- **WHEN** a QUESTION comment or its generated review view is updated and the user submits a choice again
- **THEN** issue-spec creates a new ANSWER that snapshots the then-current QUESTION while every prior ANSWER preserves its earlier QUESTION snapshot, without introducing a QUESTION revision or overwriting an ANSWER

#### Scenario: QUESTIONS work in every authoring phase

- **WHEN** an active typed QUESTION belongs to a Proposal, Design, or Implement issue
- **THEN** the native answer panel presents the same selection and submission behavior on the QUESTION comment without changing phase authority

#### Scenario: generic preview cannot emit an answer intent

- **WHEN** generic preview JavaScript posts QUESTION-shaped data to its parent
- **THEN** the host ignores the message, exposes no answer capability to the iframe, and creates no ANSWER

#### Scenario: trusted host validates and authorizes the native answer

- **WHEN** the native QUESTION panel submits the selected option IDs or custom text
- **THEN** it reloads the active QUESTION by ID, verifies permission and CSRF protection, validates the current option model, and creates a typed ANSWER through the same-origin endpoint without a second confirmation modal

#### Scenario: ANSWER captures the complete decision snapshot

- **WHEN** an authorized user submits an answer
- **THEN** the new typed ANSWER records a unique ANSWER ID, QUESTION ID and issue, complete QUESTION text and choice-model snapshot, selected IDs and labels or custom text, authenticated actor, and provider-authoritative creation time

#### Scenario: a later answer changes the effective choice without rewriting history

- **WHEN** a user submits another valid answer for the same active QUESTION
- **THEN** issue-spec appends another typed ANSWER, preserves every earlier ANSWER unchanged, and treats the latest valid ANSWER by provider comment order with stable comment-ID tie-break as effective

#### Scenario: repeated identical submissions are semantically harmless

- **WHEN** an accidental retry or repeated click creates another identical ANSWER
- **THEN** the latest-answer rule yields the same effective decision and the native panel disables duplicate clicks while a request is pending without requiring an idempotency protocol

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

- **WHEN** a QUESTION or projection is viewed on GitHub or another surface without the trusted answer surface
- **THEN** QUESTION and ANSWER source remains readable as Markdown, no functional submit control is promised, and answers may be created through a supported provider workflow

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/331#issuecomment-5060936620
- https://github.com/higress-group/issue-spec/pull/338

### Requirement: trusted self-hosted CLI QUESTION answers

The system MUST route self-hosted CLI QUESTION answers through the native trusted answer service using a bounded current-QUESTION intent, MUST authorize PATs by live scope and repository authority without weakening browser session protections, and MUST preserve existing GitHub-backed ANSWER behavior.

#### Scenario: self-hosted selected option is appended canonically

- **WHEN** a repository-authorized self-hosted PAT with sufficient issue scopes runs question answer with valid selected option IDs
- **THEN** the CLI confirms the current QUESTION through the native API and the server atomically appends an ANSWER whose identity, actor, body, timestamp, and ordering are server-generated

#### Scenario: self-hosted custom answer is appended canonically

- **WHEN** the current QUESTION permits custom input and an authorized self-hosted CLI submits valid non-empty custom text
- **THEN** the same native service validates the current choice model and appends a canonical ANSWER without accepting typed ANSWER Markdown from the client

#### Scenario: changed or malformed QUESTION intent fails closed

- **WHEN** the submitted QUESTION digest is stale or the option IDs or custom answer violate the current QUESTION choice model
- **THEN** the native service rejects the request and appends no ANSWER

#### Scenario: PAT scope and repository boundaries are enforced

- **WHEN** a PAT lacks required issue scope, is capped away from the repository, cannot see the repository, or lacks contribution authority
- **THEN** the native QUESTION read or ANSWER write is denied without disclosing concealed repository data or accepting a compatibility-comment fallback

#### Scenario: browser answer protections remain intact

- **WHEN** a browser session submits an ANSWER through the native endpoint
- **THEN** the request still requires the configured Web Origin and a valid CSRF token and reaches the same canonical trusted service

#### Scenario: GitHub-backed behavior remains compatible

- **WHEN** question answer runs with a GitHub or GitHub Enterprise profile
- **THEN** the CLI continues to render and append the caller-identified ANSWER through the existing provider comment path

#### Scenario: choice-enabled QUESTION rewrite remains forbidden

- **WHEN** a caller attempts question resolve for a choice-enabled QUESTION
- **THEN** the CLI continues to reject the rewrite and directs the caller to the append-only answer command

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/361#issuecomment-5118701687
