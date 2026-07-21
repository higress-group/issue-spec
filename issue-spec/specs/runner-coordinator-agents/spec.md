# runner-coordinator-agents

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/323

## Requirements

### Requirement: Runner accepts qoder as a valid coordinator agent kind

The runner MUST accept `qoder` as a valid coordinator agent kind in config validation, acpx adapter validation, and comment command parsing, following the same pattern as `codex` and `claude`. The runner MUST reject unknown agent kinds with a diagnostic listing all valid values including qoder.

#### Scenario: config validation accepts qoder agent

- **WHEN** the runner is started with --agent qoder or config agent.kind: qoder
- **THEN** config validation MUST accept the agent kind and proceed to startup without error

#### Scenario: acpx adapter validates qoder agent

- **WHEN** the acpx adapter is constructed with agent qoder
- **THEN** validateConfig MUST accept the agent and the adapter MUST initialize without error

#### Scenario: comment command selects qoder agent

- **WHEN** an issue comment contains /new qoder fix the bug
- **THEN** splitAgentSelector MUST return agent qoder and prompt fix the bug, and the dispatcher MUST create a qoder coordinator session

#### Scenario: invalid agent kind is rejected with qoder in valid values

- **WHEN** the runner is started with --agent gemini
- **THEN** config validation MUST return an error listing valid values as codex, claude, qoder

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/323#issuecomment-5035081507

### Requirement: Runner mirrors host qoder configuration into the sandbox

The runner MUST mirror the host ~/.qoder/settings.json into the sandbox temporary HOME at $TempHome/.qoder/settings.json so that qodercli starts with the operator configured model, reasoning effort, and context window. The runner MUST follow the same mirror pattern as Claude config mirroring, using copyLimitedFiles for regular files only. The runner MUST NOT mirror cache, log, or session state files.

#### Scenario: settings.json is mirrored into sandbox TempHome

- **WHEN** a qoder agent job is dispatched and the host has ~/.qoder/settings.json
- **THEN** the runner MUST copy settings.json to $TempHome/.qoder/settings.json before agent startup, and qodercli MUST read the mirrored config

#### Scenario: missing host qoder config is not an error

- **WHEN** a qoder agent job is dispatched and the host has no ~/.qoder directory
- **THEN** the mirror step MUST succeed silently and the sandbox MUST start without a .qoder directory

#### Scenario: sandbox HOME resolves qoder config

- **WHEN** qodercli starts inside the sandbox
- **THEN** it MUST resolve its configuration from $HOME/.qoder/settings.json where HOME is the sandbox TempHome, without requiring a QODER_HOME environment variable

#### Scenario: non-regular files are skipped during mirror

- **WHEN** the host ~/.qoder directory contains symlinks or subdirectories
- **THEN** the mirror MUST skip non-regular files and MUST NOT fail or copy them into the sandbox

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/323#issuecomment-5035087599

### Requirement: Runner derives qoder-specific acpx configuration for job dispatch

The runner MUST derive qoder-specific acpx configuration in AcpxConfigForKind, setting the agent kind to qoder and applying the default permission model (approve-reads). When QoderAgentFullAccess is enabled, the runner MUST set permissions to approve-all. The runner MUST NOT apply Claude-specific config fields (ClaudeIncludeUserSettings, ClaudeAllowedTools, ClaudeEffort) to qoder sessions.

#### Scenario: default qoder acpx config uses approve-reads

- **WHEN** AcpxConfigForKind is called with kind qoder and default config
- **THEN** the returned acpx.Config MUST have Agent=qoder, MaxPermissions=approve-reads, and empty Mode

#### Scenario: qoder full-access mode sets approve-all

- **WHEN** AcpxConfigForKind is called with kind qoder and QoderAgentFullAccess enabled
- **THEN** the returned acpx.Config MUST have MaxPermissions=approve-all

#### Scenario: qoder config does not inherit Claude-specific fields

- **WHEN** AcpxConfigForKind is called with kind qoder and ClaudeIncludeUserSettings or ClaudeAllowedTools set in config
- **THEN** the returned acpx.Config MUST NOT set ClaudeIncludeUserSettings, ClaudeAllowedTools, or ClaudeEffort

#### Scenario: model pass-through works for qoder

- **WHEN** the operator sets --model ultimate and dispatches a qoder job
- **THEN** the acpx.Config MUST carry Model=ultimate, and acpx MUST forward it to qodercli via ACP config-option negotiation

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/323#issuecomment-5035089982

### Requirement: Runner preflight validates qoder agent prerequisites

The runner preflight MUST validate qoder agent prerequisites before job dispatch: acpx binary availability, qodercli availability on PATH, and qodercli authentication status. Preflight MUST fail with actionable diagnostics when any prerequisite is missing, and MUST NOT fall back to host environment variables or credentials.

#### Scenario: preflight passes with qodercli and acpx available

- **WHEN** issue-spec runner preflight --agent qoder runs and both acpx and qodercli are on PATH with valid auth
- **THEN** preflight MUST report success for all qoder prerequisites

#### Scenario: preflight fails when qodercli is missing

- **WHEN** issue-spec runner preflight --agent qoder runs and qodercli is not on PATH
- **THEN** preflight MUST fail with a diagnostic indicating qodercli is required for the qoder agent

#### Scenario: preflight fails when qodercli auth is missing

- **WHEN** issue-spec runner preflight --agent qoder runs and qodercli has no valid authentication (no ~/.qoder/.auth and no QODER_PERSONAL_ACCESS_TOKEN)
- **THEN** preflight MUST fail with a diagnostic indicating qodercli authentication is required

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/323#issuecomment-5035092849
