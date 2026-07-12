# issue-and-change-web-experience

## Purpose

Define the embedded issue SPA, secure Markdown, workflow change boards and provider-neutral code-change relationships presented to users.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160

## Requirements

### Requirement: REST-backed issue SPA with secure Markdown and usable self-host administration

The server MUST ship an embedded React SPA that uses the authenticated APIs for issue management and the minimum bootstrap, identity, PAT, organization and repository administration needed to operate the self-hosted product, while preserving raw workflow bodies and preventing active Markdown or browser-session attacks.

#### Scenario: users browse and mutate issues through REST

- **WHEN** an authorized user opens a repository issue list or detail view
- **THEN** the SPA MUST support pagination, issue creation/edit/state changes, ordered comments, comment creation, label assignment and reaction operations with loading, empty and error states

#### Scenario: Markdown is faithful but not executable

- **WHEN** an issue or comment contains GitHub-Flavored Markdown, fenced code, raw HTML, unsafe URL schemes or event-handler attributes
- **THEN** the SPA MUST render supported GFM and syntax highlighting while sanitizing active content under a restrictive content security policy

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
