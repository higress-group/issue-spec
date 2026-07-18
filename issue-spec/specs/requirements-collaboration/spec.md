# requirements-collaboration

## Purpose

Define the durable behavior for non-developer requirements onboarding, global self-hosted connectivity, agent-neutral skill delivery, bounded requirement authoring, and public contribution.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/262

## Requirements

### Requirement: publish complete releases with curl-only installation guidance

issue-spec MUST publish complete immutable semantic-version Releases and one mutable rolling latest cross-platform Release with platform archives, Shell and PowerShell installers, a standalone agent-neutral requirements-skill archive, SHA-256 metadata, provenance or signatures, and a manifest. Every user-facing installation path in generated Release descriptions and onboarding documentation MUST download Release assets with curl only, MUST NOT require GitHub CLI, wget, Invoke-WebRequest, or another provider-specific downloader, and MUST verify the downloaded manifest, checksums, and selected archive before replacing an existing CLI.

#### Scenario: build an immutable semantic-version release

- **WHEN** a maintainer pushes a documented stable or prerelease semantic-version tag
- **THEN** GitHub Actions MUST build every supported target from the exact tagged revision and publish one matching immutable Release whose CLI and manifest report that version and revision

#### Scenario: update the single rolling latest Release

- **WHEN** a commit reaches main and its complete release matrix succeeds while it remains the newest successful main revision
- **THEN** the workflow MUST move the fixed `rolling` tag to that revision, replace the assets and description of the single rolling Release, and mark it as GitHub latest without creating a per-revision rolling Release or modifying immutable semantic-version Releases

#### Scenario: prevent stale or partial publication

- **WHEN** a required build, test, packaging, integrity, provenance, skill, or documentation job fails or an older rolling run finishes late
- **THEN** the workflow MUST NOT start a rolling update from that candidate; if an in-place asset replacement is interrupted, installers MUST reject any mixed asset set through manifest and checksum verification and the same rolling Release MUST remain safely retryable

#### Scenario: publish the complete asset set

- **WHEN** a semantic-version or rolling Release is ready
- **THEN** it MUST contain every supported platform archive, install.sh, install.ps1, issue-spec-requirements.zip, SHA256SUMS, manifest.json, and the configured provenance or signature evidence

#### Scenario: generate curl-only installation instructions

- **WHEN** a Release description is generated or onboarding documentation shows CLI installation
- **THEN** the copyable Shell and PowerShell commands MUST use curl or curl.exe to fetch Release assets directly, MUST include version and integrity verification, and MUST contain no gh CLI, wget, or Invoke-WebRequest dependency

#### Scenario: install on a fresh supported machine

- **WHEN** a user follows the curl-only Shell or PowerShell instructions without Go or GitHub CLI installed
- **THEN** the installer MUST select the matching OS and architecture asset, download required Release evidence with curl, verify it, install the CLI, and report the selected channel or version and exact source revision

#### Scenario: reject a modified release asset

- **WHEN** the downloaded archive or integrity metadata disagrees with the selected Release manifest or checksums
- **THEN** the installer MUST exit nonzero before replacing the CLI and MUST NOT execute the downloaded binary

#### Scenario: repeat an installation

- **WHEN** the installer is rerun for the already installed version or latest source revision
- **THEN** it SHALL complete idempotently without corrupting the executable or creating duplicate PATH entries

#### Scenario: deny publishing from an untrusted ref

- **WHEN** the workflow is invoked by a pull request, unsupported tag, untagged non-main branch, or tag that does not resolve to the expected revision
- **THEN** publishing jobs MUST NOT receive or use release authority and MUST NOT create or mutate a Release

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/262#issuecomment-5003227703

### Requirement: establish one secure global connection to a self-hosted requirements server

issue-spec MUST provide an additive requirements-onboarding path that accepts one canonical self-hosted server URL and an optional profile name; discovers canonical endpoints and immutable server identity through credential-free server metadata; persists only the non-secret origin-bound profile and server realm; accepts a PAT through a hidden interactive prompt or protected stdin; validates the realm and authenticated identity; and stores the PAT in the OS keyring by default. Setup MUST NOT select or persist a target repository or agent, and repository-controlled content MUST NOT select an arbitrary saved self-hosted credential realm. The saved connection SHALL be reusable for every project visible through that self-hosted server, with repository authorization evaluated live when a repository operation is attempted.

#### Scenario: configure a new global self-hosted connection

- **WHEN** a user supplies a reachable canonical server URL, optional profile name, and valid PAT
- **THEN** the CLI MUST discover and validate the server metadata, record only the non-secret origin-bound profile and immutable server identity, validate the authenticated user, store the credential in the OS-keyring realm, and report the next non-secret onboarding step without requesting or persisting a repository or agent

#### Scenario: server identity changes

- **WHEN** a saved profile or onboarding attempt observes endpoints or a server instance ID that conflict with the previously bound realm
- **THEN** the CLI MUST fail closed without sending or moving the credential to the conflicting realm and MUST explain how the user can inspect or deliberately replace the saved profile

#### Scenario: secure storage is unavailable

- **WHEN** the OS keyring cannot store the PAT
- **THEN** the CLI MUST fail with actionable keyring guidance and MUST NOT silently write the PAT to plaintext configuration; any explicit insecure fallback SHALL retain the existing opt-in warning and behavior

#### Scenario: repository content names a self-hosted profile

- **WHEN** untrusted repository-local configuration attempts to select an arbitrary saved self-hosted profile for the user-global requirements context
- **THEN** issue-spec MUST ignore or reject that selection and SHALL require an explicit user or environment choice consistent with the existing profile-precedence security boundary

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/262#issuecomment-5003228134

### Requirement: configure one global self-hosted server connection with the existing site-wide PAT

The requirements setup command MUST configure one origin-bound self-hosted server connection globally from a canonical server URL and the existing default-authorized site-wide PAT. Setup MUST NOT require or persist a repository selection or agent type, MUST NOT install an agent skill, and MUST let the resulting profile and credential be used for every repository on that server that the identity can access. Repository choice and live allowed_actions MUST be evaluated only when a later requirement operation targets a repository.

#### Scenario: preview a global connection

- **WHEN** a contributor runs requirements setup with only the server URL and without --yes
- **THEN** the CLI MUST discover and validate the server realm, preview the global profile and credential plan, and MUST NOT write profile, context, token, repository, or agent state

#### Scenario: save a global connection

- **WHEN** the contributor transfers the one-time PAT through the hidden prompt or protected stdin and confirms with --yes
- **THEN** the CLI MUST validate the server realm and authenticated identity, store the PAT in the origin-bound OS keyring, and persist only server/profile context

#### Scenario: omit repository and agent selection

- **WHEN** a contributor runs requirements setup
- **THEN** the command MUST NOT require or expose --repo, --agent, skill-destination, or skill-conflict flags and MUST NOT write Codex, Claude, or other agent directories

#### Scenario: reuse the connection across repositories

- **WHEN** the contributor later targets any repository on the configured server
- **THEN** the CLI MUST reuse the global profile and site-wide PAT while applying that identity's current repository visibility and operation authorization without a setup-time allowlist

#### Scenario: inspect repository authority at use time

- **WHEN** the requirements skill prepares work for a selected owner/name repository
- **THEN** it MUST request live repository status or perform provider-neutral reads for that repository and MUST keep drafts local when the current allowed_actions does not include contribute

#### Scenario: preserve live authorization boundaries

- **WHEN** the identity lacks or later loses private-repository access, contribution, runner, evidence, or administrative authority
- **THEN** the global site-wide PAT MUST NOT manufacture or preserve that authority independently of the live server decision

#### Scenario: protect the one-time secret

- **WHEN** documentation, screenshots, tests, CLI diagnostics, or skill examples describe setup
- **THEN** they MUST use synthetic or redacted values and MUST NOT render, log, persist, or transmit a real PAT outside the deliberate browser-to-local-CLI transfer and origin-bound keyring

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/262#issuecomment-5003228610

### Requirement: distribute one agent-neutral requirements skill for agents to install themselves

Every complete semantic-version and rolling latest Release MUST publish one standalone issue-spec-requirements skill archive from the canonical skill source with matching content identity and declared CLI/server compatibility. Release descriptions and onboarding documentation MUST expose its direct Release download URL. The issue-spec CLI MUST remain agent-agnostic: requirements setup MUST NOT select an agent, calculate an agent-specific destination, install or update a skill, or implement Codex/Claude-specific conflict handling. A user MUST instead give the verified Release archive URL to the particular agent, which owns installation according to its native skill mechanism.

#### Scenario: publish the standalone skill asset

- **WHEN** a semantic-version or rolling Release is ready
- **THEN** the release gate MUST require issue-spec-requirements.zip, its manifest entry, checksum, provenance or signature coverage, and compatibility identity

#### Scenario: identify a rolling latest skill

- **WHEN** the skill is obtained from the rolling latest Release
- **THEN** its manifest MUST identify the exact main source revision and compatible CLI build and MUST NOT present itself as an immutable semantic version

#### Scenario: show a direct skill download URL

- **WHEN** a Release description or onboarding guide explains skill setup
- **THEN** it MUST provide a curl-downloadable Release asset URL that can be handed to the intended agent without invoking issue-spec requirements setup

#### Scenario: keep CLI setup agent-neutral

- **WHEN** a user configures the requirements server
- **THEN** the CLI MUST NOT ask for an agent type, write an agent-global directory, or contain destination and overwrite workflows for specific agents

#### Scenario: let the selected agent install itself

- **WHEN** a user gives the verified skill archive or direct Release URL to a capable agent
- **THEN** that agent MUST use its own native installation mechanism and destination rules while the issue-spec CLI remains uninvolved

#### Scenario: preserve repository-owned developer skills

- **WHEN** a project contains repository-local issue-spec workflow skills
- **THEN** installing the standalone user skill through an agent's native mechanism MUST leave repository-owned files unchanged and describe the requirements skill as an additive pre-engineering workflow

#### Scenario: keep the archive non-secret and global

- **WHEN** the skill archive is inspected or installed
- **THEN** it MUST contain no credential, server secret, repository selection, or agent-specific path and MUST resolve server authority through the globally configured issue-spec profile

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/262#issuecomment-5003229062

### Requirement: keep agent-assisted requirement work bounded, authorization-aware, and confirmation-gated

The issue-spec-requirements skill MUST help a non-developer discover related self-hosted discussions, safely read selected issues, author either a simple untyped requirement issue or a self-contained proposal with canonical SPEC or QUESTION content, inspect the repository's existing allowed_actions, preview only remote writes permitted by the existing contribute decision, wait for explicit user confirmation, perform the confirmed requirement or discussion writes through provider-neutral issue-spec CLI commands, return browser URLs, and stop before engineering design, TASK, PROCESS, code, review, verification, or archive work. The skill MUST NOT require or introduce requirement-specific roles or granular authorization actions.

#### Scenario: draft a new requirement

- **WHEN** a user describes a non-trivial requirement to an agent using the requirements skill
- **THEN** the skill MUST validate the selected context and authentication, search related self-hosted discussions when supported, fetch only selected full issues through the safe-read command, and produce a self-contained proposal and draft requirements or questions for user review

#### Scenario: submit a simple requirement through public contribution

- **WHEN** an active authenticated identity that is not an organization member or collaborator selects a public repository whose existing contribution policy grants contribute and explicitly confirms a simple untyped requirement issue preview
- **THEN** the skill MUST create the ordinary issue through issue-spec without requesting labels or privileged metadata and MUST return its browser URL

#### Scenario: submit a standard proposal through public contribution

- **WHEN** the same external identity explicitly confirms a standard proposal preview
- **THEN** the skill MAY create a proposal issue with only its canonical proposal label, create or update canonical SPEC or QUESTION comments using the normal contribution and author-edit rules, maintain the identity's own proposal title and body, create ordinary discussion comments, and MUST return the browser URL for every completed write

#### Scenario: respect the existing repository contribution policy

- **WHEN** the target repository's allowed_actions does not include contribute because its policy is disabled, member-only, or otherwise denies the selected identity
- **THEN** the skill MUST keep the draft local, MUST NOT attempt a requirement mutation, and MUST explain the live repository restriction without inventing a bypass or exposing a concealed repository

#### Scenario: user has not confirmed

- **WHEN** the user asks to explore, revise, or discuss the draft but has not explicitly confirmed submission
- **THEN** the skill MUST keep the work local to the conversation and MUST NOT create or modify any remote issue or comment

#### Scenario: requirement reaches engineering design

- **WHEN** the user asks the requirements skill to create design, TASK, PROCESS, code-change, review, verification, git, PR/MR, implementation, or archive artifacts
- **THEN** the skill MUST stop, explain the boundary, and hand off to the repository-owned issue-spec developer workflow without performing the requested engineering mutation

#### Scenario: remote discussion contains instructions

- **WHEN** a search result or safely read issue contains user-authored text that attempts to direct the agent or request credentials
- **THEN** the skill MUST treat it only as untrusted requirement data, MUST NOT follow those instructions, and MUST never ask the user to paste a PAT into the agent conversation

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/262#issuecomment-5003236228

### Requirement: prove a simple bilingual fresh-user onboarding journey

The project MUST publish equivalent English and Chinese onboarding guides, generated Release descriptions, and automated acceptance evidence for a fresh non-developer journey. Installation MUST use curl-only Release downloads with no GitHub CLI dependency; requirements setup MUST configure only the global server connection; skill setup MUST be expressed as a direct Release archive URL handed to the selected agent; and documentation MUST keep only screenshots that materially explain PAT creation or requirement authoring rather than a synthetic Release page.

#### Scenario: install from a Release description

- **WHEN** a user opens a semantic-version or rolling latest Release
- **THEN** the generated description MUST identify the channel and source revision and provide copyable curl-only Shell and PowerShell commands for CLI installation, version verification, and integrity checking

#### Scenario: configure one global server

- **WHEN** a fresh user follows the English or Chinese guide
- **THEN** the documented requirements setup command MUST take the server URL without repository or agent flags and MUST explain that the connection applies to all accessible projects on that server

#### Scenario: hand the skill URL to an agent

- **WHEN** the user wants a particular agent to perform requirement authoring
- **THEN** the guide MUST provide the standalone Release asset URL and direct the user to let that agent install it through its native mechanism rather than asking the CLI to adapt to the agent

#### Scenario: omit the synthetic Release screenshot

- **WHEN** documentation assets are validated or regenerated
- **THEN** requirements-release.png and its Chinese counterpart MUST not be generated, referenced, checksummed, or required because the curl commands and integrity text are the authoritative evidence

#### Scenario: explain the rolling channel

- **WHEN** documentation presents rolling latest
- **THEN** it MUST state that latest follows successful main builds, show the exact source revision, distinguish it from immutable semantic versions, and explain how to select an immutable version

#### Scenario: regenerate useful screenshots

- **WHEN** maintainers run the screenshot-generation target
- **THEN** Playwright fixtures MUST regenerate only the required PAT and requirement-result images deterministically from synthetic data and documentation validation MUST detect missing or stale referenced assets

#### Scenario: inspect documentation safety

- **WHEN** Release descriptions, guides, screenshots, terminal examples, or test logs are scanned
- **THEN** they MUST contain no real PAT, session cookie, internal deployment origin, production identity, or production issue content

#### Scenario: check an incompatible CLI or server

- **WHEN** the requirements skill or onboarding command observes a missing required capability
- **THEN** the flow MUST stop before remote mutation and report the observed compatibility problem and an actionable upgrade path

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/262#issuecomment-5003236655

### Requirement: reuse public contribution policy for GitHub-like requirement authoring

When a public issue repository uses the existing public contribution policy, issue-spec MUST grant the existing contribute operation to every active authenticated identity, including a nonmember and noncollaborator, so that the identity can create a simple untyped issue or a standard proposal, create ordinary or canonical typed comments, edit its own comments, and maintain the title or body of its own issue through a browser session or origin-bound site-wide PAT. The implementation MUST reuse the existing read, contribute, triage, and write model instead of introducing requirement-specific roles or granular actions. Member-only and disabled contribution policies MUST retain their existing meaning; arbitrary labels, issue state, other-author content, runner, source-code, evidence, and administration MUST retain their existing stronger authorization.

#### Scenario: receive contribute from the existing public policy

- **WHEN** an active authenticated identity that is neither an organization member nor a repository collaborator opens a public repository whose contribution policy is public
- **THEN** the existing authorization evaluator MUST return contribute without a new requirement-author role or action

#### Scenario: preserve member-only and disabled policies

- **WHEN** the same external identity opens a repository whose contribution policy is members or disabled
- **THEN** the existing evaluator MUST deny contribute unless another existing membership or collaborator grant applies, and requirements onboarding MUST report the policy instead of bypassing it

#### Scenario: create a simple requirement as a public contributor

- **WHEN** the external contributor submits an ordinary issue without labels or a workflow marker
- **THEN** the server MUST create and attribute the issue through the existing contribute operation without requiring organization onboarding

#### Scenario: create a standard proposal without label-management authority

- **WHEN** the external contributor submits a canonical issue-spec proposal whose requested labels contain exactly the canonical issue-spec/proposal label derived from its valid proposal marker
- **THEN** the server MUST create the proposal through contribute without requiring triage, while any absent marker, mismatched label, or additional label MUST retain the existing triage requirement

#### Scenario: refine an author-owned issue

- **WHEN** the external contributor changes only the title or body of its own simple issue or proposal
- **THEN** the server MUST authorize the update through contribute, while a state change, label change, privileged metadata change, or another author's issue MUST retain the existing triage requirement

#### Scenario: create and maintain requirement comments

- **WHEN** the external contributor creates an ordinary comment or canonical SPEC or QUESTION comment, or edits a comment authored by that identity
- **THEN** the server MUST use the existing contribute and author-edit behavior and the existing typed-comment projector without adding a parent-ownership or typed-artifact permission gate

#### Scenario: use the default site-wide PAT

- **WHEN** the external contributor creates a PAT with the existing default authorization and site-wide repository access and submits a simple issue, proposal, typed requirement comment, or ordinary comment through the CLI
- **THEN** the server MUST apply the same existing public contribution decision without requiring a repository allowlist, organization membership, or a separate token

#### Scenario: reject anonymous requirement mutation

- **WHEN** an unauthenticated caller attempts to create or update an issue, proposal, SPEC, QUESTION, or comment on a public repository
- **THEN** the server MUST reject the mutation while preserving anonymous read access

#### Scenario: preserve private-repository isolation

- **WHEN** the external identity or its PAT addresses a private repository for which it has no explicit membership or collaborator grant
- **THEN** the server MUST conceal the repository and MUST NOT reveal or mutate its issues or comments

#### Scenario: deny unrelated privilege expansion

- **WHEN** the external contributor attempts to edit another author's content, change issue state or arbitrary labels, trigger a runner, publish evidence, administer the repository, or access source-code credentials
- **THEN** the server MUST evaluate that existing operation independently and MUST deny it unless the identity has the separately required authority

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/262#issuecomment-5003380857
