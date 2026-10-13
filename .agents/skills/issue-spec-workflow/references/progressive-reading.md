# Bounded issue-spec reads

Use for reading an assigned change. Read the complete current Design body and applicable effective answers, then discover and expand only the TASKs/SPECs required for the current work and its dependencies. Progressive reading must not omit shared invariants or blocking decisions.

```bash
# Complete Issue body, without every comment
issue-spec read issue --repo higress-group/issue-spec --issue <issue-number>

# Compact typed directory; also use SPEC, QUESTION or ANSWER as needed
issue-spec comment list --repo higress-group/issue-spec --issue <issue-number> --type TASK --json

# One selected artifact, expanded deliberately
issue-spec comment get --repo higress-group/issue-spec --issue <issue-number> --id <TASK-or-SPEC-id> --json --include-body
```

`comment get --json` without `--include-body` returns metadata, including a representation digest and links. When a prior read supplies the backend numeric `comment_id`, add `--comment-id` alongside `--id`; supported backends can retrieve the exact comment directly after verifying its Issue identity. A stable typed ID alone may still require an internal list/scan, but does not print all comment bodies.

Use `read issue --comment <numeric-comment-id>` for a specific ordinary comment or exact comment URL anchor. Avoid `read issue --comments --typed-only` and bulk `comment list --include-body` as default discovery: they expand many bodies. A selected comment is still returned whole; these commands do not provide general body-section pagination. Split oversized implementation artifacts at authoring time.

Inspect dependency links as needed. Compact output caps links per relation; request `--include-all-links` when a needed relation is truncated. Status filters and `--active-only` are not substitutes for resolving applicable QUESTION/ANSWER authority; do not assume a generic filter identifies the latest effective answer in every history.

After an announced contract change or conflicting evidence, refresh the affected artifact and applicable answers before proceeding; do not repeatedly fetch unchanged complete histories. If a local CLI differs, inspect its specific command help instead of guessing flags.
