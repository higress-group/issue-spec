# self-hosted-server-operations

## Purpose

Define production packaging, observability, recovery and executable end-to-end acceptance requirements for self-hosted capabilities.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160

## Requirements

### Requirement: Every self-hosted capability has an executable end-to-end acceptance case

The project MUST provide a reproducible conformance and security harness that boots the production server, database, identity fixtures, Runner, external source repository, code-provider bridge, and configured external-authority stub, drives production CLI and browser surfaces, proves every active SPEC through executable tests, and evaluates delivery readiness only through read-only merge-check, provider-issued complete authority-token conditional merge, and idempotent post-merge issue reconciliation rather than final, evidence, VERIFY, or Archive gates.

#### Scenario: issue workflow and protocol run without external GitHub

- **WHEN** the harness starts an isolated server and points a production CLI profile at it
- **THEN** simple-Issue and optional proposal/design/implement planning, typed planning upsert/link/status/verify-links, labels/reactions, marker fidelity, pagination, ETag/304, rate headers, compatible errors, provider review/check normalization, merge-check, and conditional merge pass without calling github.com

#### Scenario: authority and merge fail and pass for the right generation

- **WHEN** the harness supplies valid and invalid exact-subject review, check, finding, conversation, policy, external-authority, and merge generations
- **THEN** only the current trusted complete authority generation passes read-only merge-check and conditional merge, while stale, untrusted, mismatched, pending, failed, same-head-drifted, or incomplete authority blocks deterministically and no legacy evidence or Archive artifact can satisfy it

#### Scenario: post-merge bookkeeping is independently retryable

- **WHEN** conditional merge succeeds and issue reconciliation initially fails or races
- **THEN** the harness preserves the observed merged state and proves idempotent reconciliation can retry without a receipt, distributed lock, final gate, or rollback of merge authority

#### Scenario: packaging and operations conformance remain reproducible

- **WHEN** CI runs the release matrix target
- **THEN** frontend packaging, migrations, health/readiness, delivery retry and HA behavior, provider-mode regression suites, and executable coverage of every active SPEC pass without reconstructing final VERIFY or Archive gates

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

### Requirement: The server is packaged and observable as a recoverable production service

The project MUST ship a reproducible single-server artifact with embedded frontend assets, PostgreSQL migration and compatibility controls, health/readiness, structured audit and operational telemetry, safe secret configuration, and documented backup, restore, upgrade and worker high-availability procedures.

#### Scenario: build and startup are reproducible

- **WHEN** an operator builds the release or starts the documented container/compose setup
- **THEN** the pinned frontend build MUST be embedded in the Go server, configuration validation and migration locking MUST run deterministically, and startup MUST fail before readiness on invalid config or incompatible schema

#### Scenario: health and readiness reflect real dependencies

- **WHEN** an orchestrator probes the service
- **THEN** liveness MUST report process health, readiness MUST require database connectivity and required migration state, and delivery workers MUST stop accepting work during graceful shutdown

#### Scenario: operators can diagnose without exposing secrets

- **WHEN** the service emits logs, metrics, request diagnostics or audit records
- **THEN** it MUST include request/delivery identifiers and useful state while redacting PATs, sessions, delegated tokens, webhook secrets and OAuth material

#### Scenario: database and encryption keys form one recoverable unit

- **WHEN** an operator backs up, restores or upgrades the deployment
- **THEN** documentation and smoke tests MUST cover database plus encryption-key backup, migration rollback compatibility, webhook worker leases, and restoration of encrypted subscription secrets

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4932918429
