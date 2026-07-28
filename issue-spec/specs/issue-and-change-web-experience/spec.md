# issue-and-change-web-experience

## Purpose

Define the embedded issue SPA, secure Markdown, issue-first navigation and product presentation, workflow change boards, and provider-neutral code-change relationships presented to users.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160
- https://github.com/higress-group/issue-spec/issues/268

## Requirements

### Requirement: REST-backed issue SPA with secure Markdown and usable self-host administration

The server MUST ship an embedded React SPA that uses the authenticated APIs for issue management and the minimum bootstrap, identity, PAT, organization and repository administration needed to operate the self-hosted product, while preserving raw workflow bodies and preventing active Markdown or browser-session attacks.

#### Scenario: users browse and mutate issues through REST

- **WHEN** an authorized user opens a repository issue list or detail view
- **THEN** the SPA MUST support pagination, issue creation/edit/state changes, ordered comments, comment creation, label assignment and reaction operations with loading, empty and error states

#### Scenario: Markdown is faithful but not executable

- **WHEN** an issue or comment contains GitHub-Flavored Markdown, fenced code, raw HTML, unsafe URL schemes or event-handler attributes
- **THEN** the SPA MUST render supported GFM and syntax highlighting while sanitizing active content under a restrictive content security policy

#### Scenario: Mermaid fences become safe diagrams

- **WHEN** an issue or comment contains a valid `mermaid` fenced code block
- **THEN** the SPA MUST render a responsive, non-interactive diagram without relaxing Markdown sanitization or content security policy, and MUST fall back to the original fenced source when diagram parsing fails

#### Scenario: typed markers are hidden visually and preserved raw

- **WHEN** a body includes an issue-spec issue or typed-comment HTML marker
- **THEN** the rendered view MUST hide the marker while the REST body and raw editing path preserve its bytes unchanged

#### Scenario: administrators can reach an operable initial state

- **WHEN** the first administrator signs in after bootstrap
- **THEN** the SPA MUST expose PAT lifecycle and organization, repository, membership, visibility, source-binding and webhook navigation permitted by the native APIs

#### Scenario: the product remains issue-only

- **WHEN** a user navigates the SPA
- **THEN** the UI MUST NOT expose source browsing, branch, diff or pull-request hosting surfaces and MUST represent external code objects only as neutral links and evidence

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926528283

### Requirement: One workflow board card per issue-spec change

The server MUST project proposal, design and implement artifact issues sharing a repository and change key into one permission-filtered change card, MUST derive current stage and lifecycle deterministically, and MUST report marker, label and link anomalies instead of displaying the same change as three independent stage cards.

#### Scenario: three artifact issues become one card

- **WHEN** proposal, design and implement issues exist for the same repository and change marker
- **THEN** the board MUST return exactly one card with three artifact slots and current_stage implement

#### Scenario: blocked and completed are lifecycle states rather than duplicate columns

- **WHEN** a change has an unresolved blocking QUESTION or later has successful final VERIFY plus accepted archive/closure evidence
- **THEN** the card MUST report blocked or completed lifecycle independently from its highest artifact stage

#### Scenario: invalid artifact structure is visible

- **WHEN** a change has duplicate artifact types, a marker/label mismatch, missing links or an implement artifact without its expected predecessor
- **THEN** the server MUST keep a single card where possible, attach stable anomaly codes and MUST NOT silently duplicate or drop the change

#### Scenario: organization boards do not leak hidden repositories

- **WHEN** a user requests an organization-level board
- **THEN** the server MUST filter repositories and restricted external data by caller permission before aggregating cards, counts or progress

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926528729

### Requirement: Provider-neutral external code-change relationships in the web UI

The self-hosted server and SPA MUST project every caller-visible active `code_change` external reference as a first-class relationship on the owning issue and its derived change view, using the persisted provider key, external repository identity, external object identity, canonical URL, lifecycle state, and only metadata authorized for that caller; the UI MUST derive provider-specific presentation without making issue-spec core depend on vendor-specific object types or parsing free-form issue bodies.

#### Scenario: Issue detail exposes the persisted code-change relationship

- **WHEN** an authorized caller opens an issue that has a repository-visible active `code_change` reference
- **THEN** the issue detail shows the provider, external repository, external object identifier, lifecycle state, and a safe clickable canonical link sourced from the reference API rather than from issue-body text

#### Scenario: Change projection exposes delivery association

- **WHEN** a projected change contains an implement artifact whose issue has a caller-visible active `code_change` reference
- **THEN** the change detail includes that relationship and the change card provides a compact delivery indicator without removing the proposal, design, task, process, or diagnostic information

#### Scenario: Provider-specific labels remain presentation-only

- **WHEN** the UI renders code-change references from GitHub, Aone, or an unrecognized registered provider key
- **THEN** it labels GitHub objects as pull requests, Aone objects as merge requests, and unknown providers as code changes while preserving one provider-neutral API shape and canonical-link behavior

#### Scenario: Reference visibility and metadata permissions are preserved

- **WHEN** a caller can read the issue or public repository but lacks permission to read a maintainer-only reference or restricted reference metadata
- **THEN** the relationship projection omits or redacts exactly the same data as the reference API and does not disclose hidden identifiers, revisions, provider payloads, credentials, or authorization material

#### Scenario: Source binding mismatch is not presented as healthy delivery

- **WHEN** an active code-change reference identifies a different provider key or external repository from the repository's active source binding
- **THEN** the change UI reports a structural delivery diagnostic and does not present the mismatched reference as a healthy authoritative PR or MR association

#### Scenario: Canonical external links remain safe

- **WHEN** the relationship is rendered as an external link
- **THEN** the UI uses only the server-validated canonical URL, opens it with safe external-link protections, and never constructs a provider URL from untrusted issue text or embeds provider credentials

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4949546738

### Requirement: Issue-first repository navigation and product presentation

The authenticated application navigation and public project documentation MUST present Issues as the primary product workflow, MUST expose one Repositories entry backed by organization selection instead of separate Overview and implicit first-organization destinations, MUST preserve canonical organization and repository routes with path-derived context, and MUST keep the Changes workspace headline concise.

#### Scenario: Navigation starts with issues

- **WHEN** an authenticated user opens the desktop sidebar or mobile navigation
- **THEN** Issues is the first workspace destination and Repositories appears once

#### Scenario: Repositories starts with organization selection

- **WHEN** the user chooses Repositories
- **THEN** the landing page lists visible organizations and selecting one opens its existing repository list

#### Scenario: Existing repository links remain compatible

- **WHEN** the user follows an existing canonical organization or repository URL
- **THEN** the route continues to resolve and the shell identifies the organization from the current path

#### Scenario: Reader sees the primary product workflow

- **WHEN** a reader opens the English or Chinese README
- **THEN** the first product screenshot shows the Issue list, including the repository subscription entry when available, rather than the overview dashboard

#### Scenario: Changes headline stays concise

- **WHEN** a Chinese user opens the Changes workspace
- **THEN** its headline is “慎终如始，则无败事”

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/268#issuecomment-5009979689
- https://github.com/higress-group/issue-spec/issues/268#issuecomment-5010424793

### Requirement: Repository readers can inspect credential-free source bindings

The authenticated SPA MUST allow every caller with repository read permission to inspect that repository's active credential-free source binding while reserving source-binding mutations and all sensitive Webhook configuration for callers with integration-management permission.

#### Scenario: repository reader locates the active external source repository

- **WHEN** a caller can read a repository, lacks `integrations.manage`, and the repository has an active source binding
- **THEN** the source-connection page MUST fetch and display the provider key, external repository identity, server-validated web link, credential-free clone URL, default branch, binding version, and update time

#### Scenario: read-only source view exposes no mutation interaction

- **WHEN** a caller can read a repository but lacks `integrations.manage`
- **THEN** the source-connection page MUST NOT render controls that publish, connect, replace, deactivate, or confirm deactivation of a source binding and MUST explain that changes require an integration manager

#### Scenario: unbound repository remains explicit to a reader

- **WHEN** a repository reader without `integrations.manage` opens the source-connection page and no active binding exists
- **THEN** the SPA MUST display an unbound state without offering source-binding mutation controls

#### Scenario: integration manager retains source-binding management

- **WHEN** a caller has `integrations.manage` for the repository
- **THEN** the source-connection page MUST retain the existing publish and deactivate interactions in addition to the binding summary

#### Scenario: Webhook configuration remains management-only

- **WHEN** a caller without `integrations.manage` opens the repository Webhook route
- **THEN** the SPA MUST retain the permission-denied surface and MUST NOT fetch or reveal Webhook destinations, filters, secret state, suppressions, delivery history, or replay controls

#### Scenario: repository visibility remains authoritative

- **WHEN** a caller cannot read the repository or the repository is outside the caller's tenant and token scope
- **THEN** the existing repository and source-binding APIs MUST conceal the resource and the read-only UI MUST NOT create an alternate discovery path

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/351#issuecomment-5102035206
