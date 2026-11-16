# Workflow feedback

Use this reference when a consumer project enables feedback in its shared guidance and authorized work exposes a concrete CLI limitation, workflow gap, or project policy problem. Continue the original task and use an ordinary Issue and comments. A skill or available token alone grants no publication authority.

## Route and record

1. Identify the owner from the observed behavior. Route CLI limitations and reusable workflow improvements to the configured upstream repository. Route project-specific guidance, validation, and release problems to the originating project. Keep uncertain ownership as a draft. Resolve the destination's Issue profile and repository before reading or writing.
2. Search open and closed discussions for the same cause with `issue-spec search issues --repo OWNER/REPO --scope discussion --state all --query 'specific behavior'`. Read plausible matches with `issue-spec read issue --comments`. Reuse an existing Issue for the same unresolved cause and supplement a closed decision with authorized evidence when appropriate. Link a distinct regression after a fix to its predecessor.
3. For a new report, use `issue-spec issue create simple` with a self-contained body describing the dated observation, originating project and revisions, expected and actual result, impact, owning layer, suggested correction and acceptance. Follow the destination's classification policy. An initial taxonomy uses `improvement/feedback` and one of `improvement/cli`, `improvement/workflow-generic`, or `improvement/workflow-project`. If classification needs a separate authorized label action, retain the original Issue identity and retry that action.
4. Record each independent event with its original context. Use the provider's stable source instance identifier, source repository and original task or release identifier. For a retried release, preserve its original key even when the run or attempt changes. A maintainer or configured trusted reporter can map each verifiable historical event to its own record and link the original evidence. Count independent events across Issue bodies and ordinary comments. Treat discussion, handoffs and repair verification as context for those events.
5. Preview before writing. Omit `--dry-run` when the write is authorized and the requested target and count are correct.

```sh
issue-spec feedback record --repo OWNER/REPO --issue 42 --event-file occurrence.json --dry-run --json
```

```json
{
  "source_instance": "github:github.com",
  "source_repository": "example/project",
  "event_id": "release:v1.2.3@abc1234",
  "observed_at": "2026-11-16T10:30:00+08:00",
  "revision": "abc1234",
  "summary": "The catalog deployment ran before publication and its download check returned HTTP 404.",
  "evidence": ["https://github.com/example/project/actions/runs/123"]
}
```

Include only sanitized evidence. `revision` is optional. A preview reports the event key and projected confirmed count using the current identity. An event can be recorded by a contributor and remain unconfirmed. A configured reporter can confirm that event by verifying the evidence and recording the same key. The backend supplies the comment author. A submitted field claiming confirmation or identity carries no authority.

An ambiguous submission is reconciled by rereading the Issue. Retry the same event file if needed. If the result shows a pending label action, an authorized caller can retry that action with the same event without increasing the count. Preserve the Issue URL in handoffs.

## Enable and maintain a destination policy

Use a dedicated ordinary policy Issue in the **destination** repository. Its body contains a single JSON object, optionally fenced as JSON. For each trusted account, get the stable ID from `issue-spec auth status --json` under the intended Issue profile (`.auth.user_id`). Then write the IDs as strings.

```json
{
  "threshold": 3,
  "priority_label": "priority/high",
  "trusted_reporter_ids": ["123"],
  "higher_priority_labels": ["priority/critical"],
  "hold_labels": ["priority/deferred"]
}
```

The default mode is `suggest`. Set `"mode": "auto"` explicitly to permit automatic escalation. The repository must predefine the configured priority label before an automatic assignment can succeed. Higher-priority and hold labels are optional. `issue-spec/feedback-hold` and `wontfix` are recognized holds. A closed or locked Issue is ineligible.

```sh
issue-spec feedback configure --repo OWNER/REPO --issue 17 --dry-run --json
issue-spec feedback configure --repo OWNER/REPO --issue 17
```

Publishing requires permission to manage repository label definitions. The command binds the exact policy body to the repository's `issue-spec/feedback-policy` label definition. Change the policy Issue body and publish again to activate a new revision. Remove the policy label definition to disable feedback. During automatic escalation, issue-label permission is independently enforced by the selected backend. On self-hosted, comment recording uses contribution permissions and assigning labels uses triage permissions. Promotion adds only the configured label.

The commands use the active Issue profile or an explicit global `--profile` verified for the destination. A Source Binding or external code host is independent of where policies, Issues and comments are stored.

## Enable the project workflow

Add a project file to `guidance.shared` in `issue-spec/config.yaml`. The file documents when to use this reference, the project and upstream destinations, sanitization rules, and the scope of standing publication authorization if any. Run `issue-spec workflow update --dry-run --repo OWNER/REPO` before adopting the generated reference. Project guidance stays in the project-owned file.

For a requested interval review, include open and closed feedback Issues and compare their creation dates and dated evidence. Group by root cause and count distinct keys. Record the upstream fix, local adoption revision and comparable verification as they become available in relevant authorized work. Prefer the original Issue and its evidence links for handoff across sessions.
