# QUESTION decision commands

## Authoring questions in Proposal and Design

Prefer a structured choice question when the user must decide product direction,
scope, authority or a shared interface tradeoff. Supply `question create
--choice-file` with a valid model and stable option IDs, not just prose bullets.
Offer only credible alternatives; explain their relevant behavior, scope, cost or
risk differences, and state a recommendation with reasons in the question text.
Choose single/multiple selection to fit the actual decision. Enable custom text
when a meaningful alternative outside the listed options should remain possible.

This is a preference for decision questions, not a quota or mandatory question
format. Missing facts or parameters may use a plain open question. Investigate
facts already available in code or records first. Do not invent implausible
alternatives to fill a list, disguise an unanswered factual question as a choice,
or reopen an explicit decision merely to create a choice model. Record settled
decisions in the authorized artifact; resolve an existing QUESTION using its
actual model and the commands below.

## Recording decisions

Read this before recording or correcting a QUESTION decision. Decide from the
target's current structured body, not its title, status, displayed UI or the fact
that someone supplied an answer in conversation.

```sh
issue-spec comment get --repo higress-group/issue-spec --issue ISSUE_NUMBER --id QUESTION_ID --json --include-body
```

| Current QUESTION body | Command | Decision carrier |
| --- | --- | --- |
| No `Choice Model` section | `question resolve --resolution-file` | Existing QUESTION status and resolution log |
| Valid `Choice Model` with stable option IDs | `question answer --select OPTION_ID` | New append-only ANSWER; backend selects the effective answer |
| Valid model with `allow_custom: true`, and the actual decision is custom text | `question answer --custom-file` | New append-only ANSWER |
| Malformed or ambiguous model | Inspect the exact artifact and command help; report or repair within authoring authority | Do not guess a mode or attempt a write |

Prose bullets describing alternatives are not a structured choice model.
`--custom`/`--custom-file` does not bypass the model requirement and cannot be
combined with `--select`. Never invent an option ID or add a model retroactively
just to force a plain QUESTION through ANSWER. A genuine redesign of the question
belongs to its author and must preserve the actual decision and history.

For a plain QUESTION, write the settled decision and its source/evidence into a
resolution file, then resolve that same QUESTION:

```sh
issue-spec question resolve --repo higress-group/issue-spec --issue ISSUE_NUMBER --id QUESTION_ID --resolution-file resolution.md --json
```

For a choice-enabled QUESTION, use a stable option ID read from its current model
and allocate a new canonical ANSWER ID after inspecting the existing typed index:

```sh
issue-spec question answer --repo higress-group/issue-spec --issue ISSUE_NUMBER --question-id QUESTION_ID --id NEW_ANSWER_ID --select EXISTING_OPTION_ID --json
```

When the model permits a custom decision, replace `--select EXISTING_OPTION_ID`
with `--custom-file decision.md`. Do not also resolve the choice-enabled QUESTION
or rewrite its historical ANSWER comments. Do not create ANSWER through generic
comment commands.

The command choice does not grant decision authority. Record an explicit user or
authorized coordinator decision, or an implementation detail within the assigned
scope; do not resolve an unanswered product decision to unblock yourself. Cite
the decision source, do not present your own judgment as a human answer, and do
not use `--accepted-assumption` unless that assumption was actually accepted.

After a write, check its result and reread the affected QUESTION/decision once to
confirm the expected carrier changed. If the CLI rejects an ANSWER because there
is no choice model, reread the target and use `question resolve` only when the
decision is authorized; do not retry with custom text or synthesize options.
These are CLI usage rules, independent of whether HTML review projections are
enabled. They require no new approval round trip for an already authorized decision.
