# profile-onboarding-and-workflow-generation

## Purpose

Define profile-driven server onboarding, stable instance authority, repository registration, source binding and provider-capability-aware workflow generation.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160

## Requirements

### Requirement: Profile-driven server onboarding and provider-aware workflow initialization

The issue-spec CLI MUST allow an origin-bound profile to reference operator-owned provider registries and onboarding policy, and `issue-spec init` using that profile MUST idempotently resolve or register the target issue repository, establish a credential-free source binding when policy permits, discover the selected registered provider's neutral description and capabilities, and generate matching provider-neutral workflow artifacts without storing provider executables, credentials, or vendor-specific authority in the project checkout.

#### Scenario: Profile binds the server realm and operator registry

- **WHEN** a user selects a named self-hosted profile for init, CLI, or runner operations
- **THEN** the profile determines the canonical API and web origins, immutable server instance identity, CA trust, isolated credential realm, operator registry reference, and onboarding policy while credentials remain outside the persisted profile and the repository cannot override any of those authorities

#### Scenario: One profile may serve repositories using different providers

- **WHEN** repositories on the same issue-spec server use Aone, GitHub, GitLab, or another registered code provider
- **THEN** the profile references a registry containing multiple provider keys and each repository workflow or source binding selects its own key rather than the profile forcing one global default provider

#### Scenario: Init resolves an existing server repository idempotently

- **WHEN** the selected profile can reach the server and the requested organization/repository already exists and is visible to the caller
- **THEN** init reuses the unique existing repository, verifies the caller's effective access and compatible identity, performs no destructive update, and generates local configuration that records the selected profile and issue repository

#### Scenario: Init registers a missing repository under profile policy

- **WHEN** the target repository is absent, profile or explicit CLI policy permits create-if-missing registration, and the credential has organization-scoped repository-create authority
- **THEN** init creates exactly one audited repository with safe defaults such as private visibility and member contribution, handles concurrent creation idempotently, and does not require unrelated organization-administration authority when a dedicated repository-create grant is sufficient

#### Scenario: Unauthorized or ambiguous registration fails before local success

- **WHEN** the organization is missing or ambiguous, the credential lacks repository-create authority, the profile origin or server identity is invalid, or an existing repository conflicts with the requested identity
- **THEN** init fails closed with an actionable result, does not report successful initialization, does not create labels or bindings in another repository, and does not silently fall back to GitHub or a different profile

#### Scenario: Source provider and repository are discovered safely

- **WHEN** source-binding automation is enabled and the local checkout has canonical git remotes
- **THEN** init matches the remote authority and repository identity against operator-owned provider descriptions, selects a provider automatically only when the match is unique, accepts an explicit provider key as a disambiguation, and never accepts a repository-supplied executable path, credential, or unregistered provider

#### Scenario: Source binding creation is credential-free and conflict-safe

- **WHEN** init has selected one registered provider and external source repository
- **THEN** it creates or reuses an active source binding containing only the provider key, stable external repository identity, validated credential-free clone and web URLs, and default branch; an incompatible active binding produces a conflict instead of being replaced automatically

#### Scenario: Provider description drives generic workflow generation

- **WHEN** init resolves a registered provider for the repository
- **THEN** it reads only operator-trusted provider-neutral metadata and capabilities such as display name, supported remote authorities, code-change label, `change.create`, `change.comment`, `evidence.snapshot`, and recommended evidence names, then generates one generic workflow whose available steps and preconditions match those capabilities without embedding vendor CLI commands

#### Scenario: Generated workflow reflects missing and available capabilities

- **WHEN** a provider advertises only a subset of the neutral contract
- **THEN** generated skills require a pre-existing external change when `change.create` is absent, enable external finding and reply flows only when `change.comment` is present, configure pre-gate synchronization only when `evidence.snapshot` is present, and fail closed rather than describing unsupported operations as available

#### Scenario: Generated evidence synchronization does not block the first runner dispatch

- **WHEN** init generates workflow policy for a provider that supports `evidence.snapshot`
- **THEN** it configures synchronization before `verify` by default so an initial Runner `/new` dispatch can create its external change before evidence is required, while `runner` remains a valid explicit synchronization timing for projects whose every dispatch already has an active external change

#### Scenario: Project tracker integration remains independently selectable

- **WHEN** a source provider also has a project or work-item system such as Aone
- **THEN** code-provider discovery does not implicitly grant or configure project-tracker authority, and init generates tracker workflow only when a separately registered or explicitly selected project provider is available

#### Scenario: Profile registry selection has deterministic precedence

- **WHEN** both process-level operator configuration and a profile registry reference are present
- **THEN** an explicit operator environment override takes precedence over the profile reference, a missing or malformed selected registry fails provider operations closed, and repository workflow configuration never participates in registry path resolution

#### Scenario: Partial onboarding is auditable and safely resumable

- **WHEN** repository creation succeeds but source binding, provider discovery, label creation, or local workflow generation later fails
- **THEN** init reports the exact completed and pending stages, preserves server audit history, performs no destructive rollback of shared remote state, and a repeated invocation resumes idempotently without creating duplicate repositories, bindings, or labels

#### Scenario: Interactive and automated initialization make remote mutation explicit

- **WHEN** profile policy would create a remote repository or source binding
- **THEN** interactive init presents the planned server, organization, repository, provider, and visibility before mutation unless the profile or CLI explicitly enables unattended registration, and non-interactive use requires an explicit create-if-missing policy rather than inferring authority from project files

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4949612765
