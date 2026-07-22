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

The runner MUST mirror the host ~/.qoder/settings.json and the regular files directly inside ~/.qoder/.auth into the sandbox temporary HOME. The .auth mirror MUST be one level only, MUST preserve restrictive source permission bits (defaulting to 0600), MUST remove stale mirrored files, and MUST NOT follow destination symlinks or mirror cache, log, session-state, symlink, directory, or device entries.

#### Scenario: settings.json is mirrored into sandbox TempHome

- **WHEN** a qoder agent job is dispatched and the host has ~/.qoder/settings.json
- **THEN** the runner MUST copy settings.json to $TempHome/.qoder/settings.json before agent startup and qodercli MUST read the mirrored config

#### Scenario: .auth regular files are mirrored safely and stale files are pruned

- **WHEN** the host has regular credential files directly inside ~/.qoder/.auth or a previously mirrored credential is no longer present
- **THEN** the runner MUST copy only those one-level regular files with restrictive modes, MUST remove stale mirrored files, and MUST NOT write through a destination symlink or outside $TempHome/.qoder/.auth

#### Scenario: missing host qoder config is not an error

- **WHEN** a qoder agent job is dispatched and the host has no ~/.qoder directory
- **THEN** the mirror step MUST succeed silently and the sandbox MUST start without a .qoder directory

#### Scenario: sandbox HOME resolves qoder config

- **WHEN** qodercli starts inside the sandbox
- **THEN** it MUST resolve its configuration from $HOME/.qoder/settings.json where HOME is the sandbox TempHome, without requiring a QODER_HOME environment variable

#### Scenario: non-regular source entries are skipped

- **WHEN** the host qoder config contains symlinks, subdirectories, devices, cache, logs, or session state
- **THEN** the mirror MUST skip those entries, MUST NOT recurse, and MUST NOT expose them inside the sandbox

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/323#issuecomment-5035087599

### Requirement: Runner derives qoder-specific acpx configuration for job dispatch

The runner MUST derive qoder-specific acpx configuration in AcpxConfigForKind, using approve-reads by default and approve-all when QoderAgentFullAccess is enabled. Qoder sessions MUST NOT inherit Claude-only fields. The configured model MUST be forwarded only when qoder is the runner's configured default kind; a secondary /new qoder job MUST leave Model empty and use mirrored qoder settings.

#### Scenario: default qoder acpx config uses approve-reads

- **WHEN** AcpxConfigForKind is called with kind qoder and default config
- **THEN** the returned acpx.Config MUST have Agent=qoder, MaxPermissions=approve-reads, and empty Mode

#### Scenario: qoder full-access mode sets approve-all

- **WHEN** AcpxConfigForKind is called with kind qoder and QoderAgentFullAccess enabled
- **THEN** the returned acpx.Config MUST have MaxPermissions=approve-all

#### Scenario: qoder config does not inherit Claude-specific fields

- **WHEN** AcpxConfigForKind is called with kind qoder and ClaudeIncludeUserSettings, ClaudeAllowedTools, or ClaudeEffort are configured
- **THEN** the returned acpx.Config MUST NOT set any Claude-specific field

#### Scenario: model pass-through applies when qoder is the configured default

- **WHEN** the runner default kind is qoder and the operator sets --model ultimate before dispatching a qoder job
- **THEN** the acpx.Config MUST carry Model=ultimate for ACP config-option negotiation

#### Scenario: secondary qoder selection uses mirrored model settings

- **WHEN** the runner default kind is codex or claude and a comment dispatches /new qoder
- **THEN** the qoder acpx.Config MUST leave Model empty so qodercli uses the mirrored settings.json model

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/323#issuecomment-5035089982

### Requirement: Runner preflight validates qoder agent prerequisites

The runner preflight MUST validate acpx, qodercli, and qoder authentication from host PATH and host auth sources before dispatch. A successful qoder prerequisite MUST remain usable inside the sandbox: the selected qodercli runtime MUST be mounted and reachable on sandbox PATH, and QODER_PERSONAL_ACCESS_TOKEN or mirrored ~/.qoder/.auth credentials MUST reach qodercli. Preflight MUST NOT accept sandbox-only state, and failures for a non-default agent MUST be demoted to non-blocking warnings.

#### Scenario: preflight passes with qodercli and acpx available

- **WHEN** issue-spec runner preflight --agent qoder runs and both acpx and qodercli are on host PATH with valid host auth
- **THEN** preflight MUST report success for all qoder prerequisites

#### Scenario: preflight fails when qodercli is missing

- **WHEN** issue-spec runner preflight --agent qoder runs and qodercli is not on host PATH
- **THEN** preflight MUST fail with a diagnostic indicating qodercli is required for the qoder agent

#### Scenario: preflight fails when qodercli auth is missing

- **WHEN** issue-spec runner preflight --agent qoder runs and neither regular files in host ~/.qoder/.auth nor host QODER_PERSONAL_ACCESS_TOKEN are available
- **THEN** preflight MUST fail with a diagnostic indicating qodercli authentication is required

#### Scenario: accepted qoder executable and token reach the sandbox

- **WHEN** qodercli is installed outside fixed system bind roots or QODER_PERSONAL_ACCESS_TOKEN is the selected auth source
- **THEN** sandbox preparation MUST mount the required qodercli runtime, add its bin directory to sandbox PATH, and preserve the token for the qoder process

#### Scenario: secondary agent failures do not block the default agent

- **WHEN** qoder is non-default or codex/claude are non-default and one of that secondary agent's prerequisite checks fails
- **THEN** preflight MUST report the secondary failure as a non-blocking warning while keeping the configured default agent startup decision unchanged

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/323#issuecomment-5035092849
