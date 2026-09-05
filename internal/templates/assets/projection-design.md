# Design explainer

Help a reviewer understand correctness and alternatives before complete TASK planning:

1. Lead with a concrete request, event, or operator action and its expected observable outcome; then name the selected architecture and every invariant it must preserve.
2. Trace that case through the end-to-end data or control flow as numbered steps, with a meaningful failure path and trust boundaries visible.
3. Cover interfaces, shared state, cache or persistence behavior, state transitions, and downstream consumers where applicable.
4. Compare rejected or conditional alternatives and the premises that made the selected design preferable.
5. Cover compatibility, migration, rollout, rollback, risks, verification strategy, and traceability to every active SPEC.

Use interaction to explore layers or branches; do not add animation merely to make the page feel active.
