# Implement execution brief

Help a reviewer validate the selected direct or managed execution strategy and monitor PROCESS records only when managed coordination exists.

Open with one concrete acceptance case and show how the selected execution path carries it from trigger to verified outcome. For a direct path, show the single writer, bounded work package, and focused verification. For a managed path, use the candidate or current PROCESS sequence as the technical explanation of that case, not the first concept a reviewer must decode.

The top level must show:

- the representative acceptance case and its observable outcome;
- the invariant-based direct work package or current typed PROCESS DAG;
- counts for planned/ready/active/blocked/completed work only when authoritative records provide them;
- the critical path and safe parallel groups only when managed coordination was selected;
- the selected single writer or managed Agent allocation and its rationale;
- actual typed blockers;
- shared touchpoints and provider-review/configured-check obligations.

If managed coordination is selected before PROCESS records exist, label work packages and dependencies `Candidate planning`. After records exist, replace candidates with the current typed PROCESS records and links. For a direct path, present the bounded single-writer plan without inventing candidate PROCESS records. Never leave a conflicting candidate DAG looking authoritative.

For the direct work package or each selected PROCESS drill-down, show:

- owned Design invariant and acceptance outcome;
- parent TASK and covered SPEC/scenarios when selected;
- dependencies, predecessor handoff when one exists, and downstream consumers;
- major entry points, owned areas, and shared touchpoints;
- role recommendation and whether parallel execution is safe;
- focused tests, generators, review, and verification obligations;
- code-volume range with confidence and stated basis;
- correctness complexity separately from change-surface, verification, rollout, and coordination complexity;
- human review focus and authoritative links.

Explain correctness complexity as the reasoning difficulty of preserving the invariant across states, failures, concurrency, compatibility, or trust boundaries. It is not a synonym for lines changed.
