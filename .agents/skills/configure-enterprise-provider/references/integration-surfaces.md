# Enterprise integration surfaces

Choose one owner for each surface before writing a wrapper.

| Surface | Owner | Integration | Secret boundary |
|---|---|---|---|
| Issues, typed planning, Runner commands | issue-spec Server | native Server API | issue-spec login/session |
| Source, PR/MR, discussion, CI, approval, merge | company code platform | Source Binding plus optional `issue-spec.code-provider/v1` operations | operator bridge environment/token file |
| Runner clone and push | Git transport | `git-credential-v1` or trusted host SSH | Runner-only credential boundary |
| Work-item projection | company tracker | separate idempotent adapter | adapter credential boundary |

The code bridge supports three independent capabilities:

- `change.create`: create the provider-native PR/MR for an exact pushed head;
- `change.comment`: add ordinary provider-native review discussion;
- `evidence.snapshot`: optionally read exact-head audit/navigation facts.

Advertise any implemented subset. Missing capabilities constrain only those
operations. They do not disable planning, implementation, Runner dispatch, Git
push, or a human handoff that can be completed manually.

There is intentionally no merge-authority capability, normalized provider
policy, canonical-principal mapping, readiness state, merge action, or
post-merge reconciliation. The code platform and human reviewer make those
decisions directly from the current PR/MR.

Keep the operator registry available to every Server or CLI process that needs
one of the advertised operations. Repository workflow configuration names only
the provider key and never supplies executable paths, environment, or secrets.

For each integration record:

- owner and authoritative system;
- requested operation and advertised capability;
- credential location and process boundary;
- exact repository/change/head identity;
- timeout, output, and redaction bounds;
- non-production contract-test evidence;
- failure behavior and manual fallback;
- rollback steps.
