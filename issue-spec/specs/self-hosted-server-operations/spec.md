# self-hosted-server-operations

## Purpose

Define production packaging, observability, recovery and executable end-to-end acceptance requirements for self-hosted capabilities.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160

## Requirements

### Requirement: Every self-hosted capability has an executable end-to-end acceptance case

The project MUST provide a reproducible conformance and security harness that boots the production server, database, identity fixtures, Runner, external source repository, and code-provider bridge, drives production CLI and browser surfaces, proves every active SPEC through executable tests, and exercises issue planning, implementation dispatch, change creation, ordinary discussion, rationale, and human handoff without final, evidence, VERIFY, Archive, merge-authority, or post-merge lifecycle gates.

#### Scenario: human-handoff workflow runs without external GitHub

- **WHEN** the harness starts an isolated server and points a production CLI profile at it
- **THEN** planning, typed authoring, Runner implementation, change attachment or creation, ordinary discussion, and handoff pass through the configured bridge without requiring normalized review, authoritative checks, or conditional merge

#### Scenario: packaging and operations remain reproducible

- **WHEN** CI runs the release matrix target
- **THEN** frontend packaging, migrations, health, readiness, delivery retry, HA behavior, provider-mode regression suites, and executable active-SPEC coverage pass without reconstructing retired authority gates

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908

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
