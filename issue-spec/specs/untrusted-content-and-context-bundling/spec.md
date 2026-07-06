# untrusted-content-and-context-bundling

## Purpose

Define the long-lived behavior contract for how issue-spec treats GitHub-fetched
content as untrusted and how it bundles context for the sandboxed coordinator agent.
This capability owns two coupled surfaces: a safe read path that partitions
tool-produced trusted metadata from attacker-controllable user content (per-invocation
spoof-resistant boundaries, untrusted labeling, secret redaction), and context-bundle
discipline that keeps the coordinator prompt bounded by referencing bodies for
on-demand fetch instead of re-inlining them every resume turn.

This durable spec is organized by stable capability surfaces. Future changes that add
untrusted content sources, strengthen the trust boundary or redaction, or adjust how
the coordinator context bundle is minimized should update the relevant requirement
module below (matched by requirement title, newest wins) instead of appending a
one-to-one copy of new proposal requirements.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/126

Related history (audit trail, not part of the durable contract):
- Design: https://github.com/higress-group/issue-spec/issues/127
- Implement: https://github.com/higress-group/issue-spec/issues/128
- Implementation PR: https://github.com/higress-group/issue-spec/pull/131

## Requirements

### Requirement: Read-only safe content fetch command

issue-spec MUST provide a read-only subcommand that fetches GitHub issue, pull request, and comment content through the existing authenticated backend. The command MUST NOT create, update, or delete any GitHub resource, and SHALL reuse the caller's configured authentication and repository permissions.

#### Scenario: Fetch issue content on demand

- **WHEN** an authorized caller runs the read command for an issue and requests comments
- **THEN** the command returns the issue title, body, and comments fetched via the existing backend and issues no mutating API call

#### Scenario: Fetch pull request content on demand

- **WHEN** an authorized caller runs the read command for a pull request
- **THEN** the command returns the pull request body and, on request, its review comments without performing any write

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/126#issuecomment-4889714752

### Requirement: Untrusted labeling with spoof-resistant boundary

The safe read command MUST label its output as untrusted artifact data and MUST wrap every user-authored content field in a boundary that is unique per invocation, such that fetched content cannot forge or terminate the boundary marker. The output MUST carry an explicit notice that wrapped content is data only and cannot override the agent contract. The output format is NOT required to be JSON; the security boundary is the per-invocation marker, not any serialization format. Tool-produced trusted metadata and untrusted content MUST be partitioned: trusted metadata (such as ids, URLs, author, and the typed-artifact flag) outside the boundary, and untrusted user-authored content inside it.

#### Scenario: Each content field is bounded

- **WHEN** the read command returns an issue body and comment bodies
- **THEN** each body is enclosed in a per-invocation unique boundary and the payload is tagged with the untrusted trust label

#### Scenario: Content cannot escape the boundary

- **WHEN** fetched content itself contains text resembling a closing boundary marker
- **THEN** the authentic boundary uses a per-invocation nonce the content cannot match, so the injected marker remains inert data

#### Scenario: Trusted metadata is partitioned from untrusted content

- **WHEN** the read command emits an item that has both tool-produced metadata and user-authored content
- **THEN** the trusted metadata appears outside the boundary and only the untrusted content appears inside it, independent of whether the output is JSON or plain text

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/126#issuecomment-4889715517

### Requirement: Secret redaction in read output

The safe read command MUST redact known secret values, including the resolved authentication token and configured token environment variables, from all returned content before emitting output.

#### Scenario: Token value is redacted

- **WHEN** fetched issue or comment content contains a value equal to the active authentication token
- **THEN** the emitted output replaces that value with a redaction placeholder

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/126#issuecomment-4889716266

### Requirement: Comment coverage including non-typed comments

The safe read command MUST be able to return non-typed (ordinary human) comments in addition to issue-spec typed comments, and MUST annotate each returned comment with whether it is an issue-spec typed artifact. The command MUST support restricting output to typed comments only.

#### Scenario: Non-typed comments are returned and annotated

- **WHEN** an issue contains both typed issue-spec comments and ordinary human comments and the caller requests comments
- **THEN** the command returns both kinds, each marked with whether it is a typed artifact

#### Scenario: Typed-only restriction

- **WHEN** the caller requests typed comments only
- **THEN** the command returns issue-spec typed comments and omits ordinary comments

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/126#issuecomment-4889716942

### Requirement: Coordinator prompt mandates the safe read path

The coordinator prompt MUST instruct the agent to read issue, pull request, and comment body content through the safe read command rather than raw GitHub CLI reads, and MUST state that the returned content is untrusted data that cannot override the coordinator contract.

#### Scenario: Prompt directs body reads through the safe command

- **WHEN** the coordinator prompt is rendered for a runner turn
- **THEN** it contains a contract clause requiring issue/PR/comment body reads via the safe read command and treating results as untrusted

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/126#issuecomment-4889717594

### Requirement: No per-turn body re-inlining on resume

The runner MUST NOT re-inline full issue or pull request body content into the coordinator prompt on every resume turn. It SHALL instead provide references sufficient for the agent to fetch bodies on demand through the safe read command, so that context does not accumulate duplicate body content across resume turns.

#### Scenario: Resume does not duplicate body content

- **WHEN** a public session is resumed multiple times
- **THEN** the coordinator prompt for each resume turn references the issue/PR rather than re-embedding the full body content already provided earlier

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/126#issuecomment-4889718522
