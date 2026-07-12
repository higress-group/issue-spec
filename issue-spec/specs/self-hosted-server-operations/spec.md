# self-hosted-server-operations

## Purpose

Define production packaging, observability, recovery and executable end-to-end acceptance requirements for self-hosted capabilities.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160

## Requirements

### Requirement: Every self-hosted capability has an executable end-to-end acceptance case

The project MUST provide a reproducible conformance and security harness that boots the production server, database, identity fixtures, runner serve process, external source repository and neutral evidence stub, drives the production CLI and browser surfaces, and fails the final gate unless every active SPEC has a passing executable test.

#### Scenario: issue workflow and protocol run without external GitHub

- **WHEN** the harness starts an isolated server and points a production CLI profile at it
- **THEN** proposal/design/implement creation, typed upsert/link/status/verify-links, labels/reactions, marker fidelity, pagination, ETag/304, rate headers and compatible errors MUST pass without calling github.com

#### Scenario: identity, tenant and browser security are exercised

- **WHEN** the harness drives generic OIDC/GitHub OAuth test adapters, bootstrap, sessions, PATs, permissions and the SPA
- **THEN** CSRF/XSS, token lifecycle, cross-org reads/writes, admin flows and change-board filtering MUST match their SPECs

#### Scenario: real runner sandbox proves the decoupled runtime

- **WHEN** a delivered comment triggers runner serve against a temporary external git repository
- **THEN** the runner MUST resolve and clone the binding, use short-lived credentials, start the sandboxed agent, pass child auth status and perform comment/reaction/status writeback without exposing a long-lived token

#### Scenario: evidence and archive gates fail and pass for the right revision

- **WHEN** the harness supplies valid and invalid review/check/merge/archive evidence
- **THEN** valid trusted evidence for the exact revision MUST pass and stale, untrusted, mismatched, pending or failed evidence MUST block deterministically

#### Scenario: packaging and operations gates are reproducible

- **WHEN** CI runs the final matrix target
- **THEN** embedded frontend build, migrations, health/readiness, delivery retry/HA behavior and existing GitHub-mode regression suites MUST pass and the matrix MUST report no uncovered active SPEC

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4927466268

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
