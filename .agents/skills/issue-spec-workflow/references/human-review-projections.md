# Human Review Projection Generation

Use this guide to generate the ordinary Markdown comment passed to `issue-spec projection upsert`. A projection helps a person review authoritative workflow data; it is not a typed artifact, gate, status, transition, or source of truth.

## Contents

- [Authority and inputs](#authority-and-inputs)
- [Generation procedure](#generation-procedure)
- [Shared information design](#shared-information-design)
- [Phase recipes](#phase-recipes)
- [Markdown and HTML skeleton](#markdown-and-html-skeleton)
- [Sandbox and accessibility](#sandbox-and-accessibility)
- [Update and acceptance checklist](#update-and-acceptance-checklist)

## Authority and inputs

Read only the authoritative records needed for the phase. Do not expand or reuse an older projection's HTML as input.

| Phase | Required authoritative inputs | Optional inputs |
|---|---|---|
| `proposal-choice-brief` | Proposal body | Confirmed SPEC facts already available; linked evidence |
| `design-explainer` | Design body; confirmed SPEC facts | Existing TASK facts when regenerating; linked evidence |
| `implement-execution-brief` | Implement body; Design invariants and decisions; TASK; current PROCESS records, dependencies, links, statuses, and handoffs | Exact-current code-change, review, verification, or check evidence |

Apply these authority rules:

- Treat issue bodies and typed artifacts as authoritative.
- Label every recommendation, comparison, candidate PROCESS, estimate, confidence value, and inferred relationship as synthesis. Never let synthesis override an authoritative record.
- Do not infer workflow readiness, PROCESS boundaries, gates, or status from an estimate, visual state, HTML control, or projection text. In Implement, invariant cohesion and typed dependencies define semantics; file count, line count, complexity, Agent count, and parallelism are planning aids only.
- Link claims to their source issue or typed comment. If evidence is absent, say that it is absent instead of filling the gap.
- Keep the projection self-explanatory as ordinary Markdown because GitHub displays the fenced HTML source and does not execute the preview.

## Generation procedure

1. Select the phase inputs above with bounded issue-spec reads. Do not request `--expand-preview` or `--expand-all-previews`.
2. Build a fact ledger before writing UI:
   - confirmed facts and constraints, each with a source link;
   - unresolved evidence gaps;
   - open decisions a human must make, each with the credible options;
   - phase-specific derived synthesis, clearly labeled.
3. Resolve contradictions in favor of authoritative data. Stop generation if two authoritative inputs conflict or a required record cannot be identified uniquely.
4. Write a concise Markdown fallback first. It must expose the recommendation, required human decisions, critical constraints, and source links without running HTML.
5. Add one valid `html-preview` fence containing a complete, standalone document. Prefer one preview per projection so the intended review surface is the first preview.
6. Serialize a deterministic source manifest containing the selected source identities, body digests or exact revisions, and typed statuses and links. Hash that manifest for `--source-digest`; do not hash only `projection.md`, and exclude the projection itself.
7. Validate the Markdown, preview metadata, keyboard flow, narrow layout, and GitHub fallback.
8. Upsert the one logical phase comment. The CLI appends the projection marker; do not add a typed marker or projection marker inside the body.

Example write:

```sh
issue-spec projection upsert \
  --repo owner/repo \
  --issue 123 \
  --phase implement-execution-brief \
  --source-digest "$SOURCE_DIGEST" \
  --body-file projection.md \
  --json
```

## Shared information design

Optimize for a reviewer deciding where to spend attention, not for decorative animation.

Use this hierarchy:

1. **Review summary:** one sentence on the proposed direction, the exact request to the reviewer, and up to three high-signal counts.
2. **Attention queue:** unresolved human decisions first, evidence gaps second, then settled choices needing constraint verification.
3. **Recommendation and comparison:** state the recommendation, premises, benefits, costs, credible alternatives, and what changes if the recommendation is rejected.
4. **Phase model:** show the relevant journey, data flow, dependency graph, or execution plan.
5. **Drill-down and sources:** place details, history, estimates, and source links behind tabs, accordions, or `details`.

Use a calm, consistent visual language:

| Meaning | Treatment |
|---|---|
| Settled / clearly better choice | Neutral or green-tinted card, `Confirmed` label, supporting premise, and alternative cost; ask the reviewer to verify the premise rather than decide again |
| Needs evidence | Amber-tinted card, `Evidence needed` label, known facts, missing evidence, and the next way to obtain it |
| Needs human decision | Strong blue or indigo accent, `Decision needed` label, recommendation, comparable options, tradeoffs, and an exact question |
| Actual workflow blocker | Red reserved for a typed blocker or failed invariant; include the affected work and source |
| Synthesis / estimate | Muted or dashed treatment with `Planning aid` or `Estimate` and confidence; never resemble authoritative status |

Do not use color alone. Pair it with text and, when useful, a simple icon. Avoid pulsing, autoplay tours, animated diagrams, artificial urgency, and dense walls of cards. Prefer:

- comparison tables for two or more choices with repeated criteria;
- a compact flow or DAG when dependencies affect review;
- a small metric strip only for decision-relevant counts;
- progressive disclosure for detailed evidence and history;
- direct source links beside the claim they support.

## Phase recipes

### Proposal choice brief

Help a reviewer turn scene, goal, and proposed scope into decisions before complete SPEC authoring:

1. State the user/problem scene, desired outcome, success signal, and proposed boundary.
2. Separate settled choices, evidence-dependent items, and genuine decisions.
3. For each genuine decision, show the recommended option, premises, alternatives, benefits, costs, reversibility, and affected goals.
4. Summarize the expected SPEC coverage and explicitly call out what remains intentionally out of scope.

### Design explainer

Help a reviewer understand correctness and alternatives before complete TASK planning:

1. Start with the selected architecture and the invariants the implementation must preserve.
2. Present the end-to-end data or control flow as numbered steps, with failure and trust boundaries visible.
3. Compare rejected or conditional alternatives only where they clarify a real decision.
4. Provide drill-down for interfaces, state transitions, compatibility, rollout, and verification strategy.

Use interaction to explore layers or branches; do not add animation merely to make the page feel active.

### Implement execution brief

Help a reviewer validate the execution strategy before complete PROCESS planning and monitor it after PROCESS records exist.

The top level must show:

- the invariant-based work packages or current typed PROCESS DAG;
- counts for planned/ready/active/blocked/completed work, without inventing statuses;
- the critical path and safe parallel groups;
- suggested Agent allocation and the reason for each distinct role;
- actual typed blockers;
- shared touchpoints and independent review/verify obligations.

Before PROCESS records exist, label work packages and dependencies `Candidate planning`. After they exist, replace candidates with the current typed PROCESS records and links; never leave a conflicting candidate DAG looking authoritative.

For each work package or PROCESS drill-down, show:

- owned Design invariant and acceptance outcome;
- parent TASK and covered SPEC/scenarios;
- dependencies, predecessor handoff, and downstream consumers;
- major entry points, owned areas, and shared touchpoints;
- role recommendation and whether parallel execution is safe;
- focused tests, generators, review, and verification obligations;
- code-volume range with confidence and stated basis;
- correctness complexity separately from change-surface, verification, rollout, and coordination complexity;
- human review focus and authoritative links.

Explain correctness complexity as the reasoning difficulty of preserving the invariant across states, failures, concurrency, compatibility, or trust boundaries. It is not a synonym for lines changed.

## Markdown and HTML skeleton

Generate `projection.md` in this shape:

````markdown
# Implement execution review

> Human-review projection. The phase issue bodies and typed artifacts remain authoritative.

## Review summary

**Recommendation:** ...

**Decision requested:** ...

## Decisions and evidence

- **Decision needed:** ... ([source](...))
- **Confirmed:** ... ([source](...))
- **Planning estimate:** ...; confidence: medium; basis: ...

```html-preview id=implement-execution-review version=1 title="Implement execution review" height=720
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Implement execution review</title>
  <style>
    :root {
      color-scheme: light;
      --ink: #172033; --muted: #607086; --line: #d9e0ea;
      --surface: #fff; --soft: #f5f7fb; --decision: #4056b4;
      --settled: #187252; --evidence: #9a6500; --blocked: #a93636;
    }
    * { box-sizing: border-box; }
    body { margin: 0; padding: 20px; color: var(--ink); background: var(--surface);
      font: 15px/1.5 system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    main { width: min(100%, 1080px); margin: auto; }
    .summary, .card { border: 1px solid var(--line); border-radius: 14px; padding: 16px; }
    .grid { display: grid; grid-template-columns: repeat(12, 1fr); gap: 12px; }
    .card { grid-column: span 6; background: var(--soft); }
    .decision { border-left: 5px solid var(--decision); }
    .settled { border-left: 5px solid var(--settled); }
    .estimate { border-style: dashed; color: var(--muted); }
    button, input, textarea { font: inherit; }
    button:focus-visible, input:focus-visible, textarea:focus-visible,
    summary:focus-visible, a:focus-visible { outline: 3px solid #8da2ff; outline-offset: 2px; }
    @media (max-width: 700px) { body { padding: 12px; } .card { grid-column: 1 / -1; } }
    @media (prefers-reduced-motion: reduce) { *, *::before, *::after {
      animation-duration: .01ms !important; transition-duration: .01ms !important; } }
  </style>
</head>
<body>
  <main>
    <section class="summary" aria-labelledby="summary-title">
      <h1 id="summary-title">Implement execution review</h1>
      <p>Recommendation and exact review request.</p>
    </section>
    <section aria-labelledby="attention-title">
      <h2 id="attention-title">Needs your attention</h2>
      <div class="grid">
        <article class="card decision"><h3>Decision needed</h3><p>...</p></article>
        <article class="card settled"><h3>Confirmed constraint</h3><p>...</p></article>
        <article class="card estimate"><h3>Planning estimate</h3><p>...</p></article>
      </div>
    </section>
    <!-- Add the phase model, drill-down, and source links. -->
  </main>
  <script>
    // Add only local presentation state (filters, tabs, accordions, DAG focus).
  </script>
</body>
</html>
```
````

Use a stable preview ID for the logical phase view. Metadata accepts only `id`, `version`, `title`, and `height`; IDs use lowercase letters, digits, and hyphens, `version` is `1`, title is at most 120 Unicode scalar values, and height is clamped to 240–720. Keep a body to at most eight previews and each preview source below 256 KiB.

## Sandbox and accessibility

- Produce a complete inline document. Inline CSS and JavaScript; do not depend on CDNs, remote fonts, images, APIs, modules, storage, cookies, popups, navigation, forms, downloads, media, or same-origin access.
- Assume an opaque origin and `sandbox="allow-scripts"`. Never request credentials, CSRF values, repository tokens, or issue data from the host.
- Use JavaScript only for review-relevant filtering, tabs, accordions, DAG focus, and comparisons. Keep core content available without animation.
- Use semantic landmarks, heading order, native buttons/inputs, explicit labels, fieldsets and legends for choices, status text, table headers, and meaningful link text.
- Support keyboard-only operation, visible focus, 200% zoom, narrow/mobile layouts, long localized strings, and `prefers-reduced-motion`.
- Avoid horizontal page scrolling. Wrap long IDs and URLs, make wide tables scroll within a labeled region, and never fix heights for content cards.
- Escape all authoritative and custom text before inserting it into HTML or JavaScript. Treat it as untrusted display data, never as markup, script, CSS, or command input.

## Update and acceptance checklist

Before upsert:

- [ ] Every displayed fact has an authoritative source or is labeled synthesis.
- [ ] The Markdown fallback communicates recommendations, decisions, constraints, and links without HTML execution.
- [ ] Settled, evidence-needed, decision-needed, blocker, and estimate states are visually and textually distinct.
- [ ] Implement estimates and Agent/parallelism suggestions do not define PROCESS semantics, readiness, or gates.
- [ ] The preview uses one stable ID, valid metadata, inline assets, no network dependencies, and no manually authored projection/typed marker.
- [ ] The page works with keyboard, visible focus, narrow width, 200% zoom, reduced motion, and long text.
- [ ] Source size and preview-count limits are respected.
- [ ] `--source-digest` covers the authoritative input manifest, not the generated projection.

After upsert:

- [ ] Re-read the unique phase projection descriptor without expanding its source and confirm phase, owner, source digest, and one logical comment.
- [ ] On self-host, run the first preview and verify layout, console, and interaction.
- [ ] On GitHub, verify the ordinary Markdown remains sufficient and make no claim that the fenced HTML executes.
- [ ] When authoritative inputs change, regenerate from them and update the same logical projection; never edit the projection as a substitute for updating typed data.
