# issue-discovery-and-search

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/448

## Requirements

### Requirement: Issue search is limited to Proposal titles and bodies

The issue-spec CLI and dedicated self-hosted Search web/native routes MUST return only canonical Proposal Issues and MUST match query text only against each Proposal Issue title and body. The self-hosted PostgreSQL query MUST restrict candidates to active Proposal artifacts before text matching and MUST NOT search or hydrate comments, ordinary Issues, Design/Implement Issue bodies, issue numbers, or standalone change-key metadata. GitHub CLI search MUST require the canonical issue-spec/proposal label with in:title,body and MUST defensively discard non-Proposal provider results. Authorization, state filtering, deterministic ordering, pagination, bounded plain-text excerpts, provider trust boundaries, and the five-second server-owned query deadline MUST remain intact. Compatibility inputs source=all|issue and stage unset|proposal MAY be accepted and normalized to this fixed scope; contradictory comment, change, design, or implement filters MUST be rejected.

#### Scenario: Proposal title or body is discoverable

- **WHEN** an authorized canonical Proposal Issue contains the query in its title or body
- **THEN** CLI and dedicated self-hosted Search web/native routes return the Proposal with a bounded issue-source excerpt

#### Scenario: Non-Proposal discussion content is excluded from discovery

- **WHEN** the query appears only in a comment, ordinary Issue, Design Issue, Implement Issue, issue number, or standalone artifact metadata
- **THEN** the dedicated discovery search returns no result for that content and performs no comment matching or hydration

#### Scenario: PostgreSQL narrows before matching

- **WHEN** a repository contains many Issues and comments but relatively few active Proposals
- **THEN** the dedicated discovery query derives the active Proposal candidate rows before evaluating title/body matching and ranking

#### Scenario: GitHub uses the same Proposal-only contract

- **WHEN** the CLI searches through a GitHub profile
- **THEN** the provider query requires issue-spec/proposal and in:title,body, and response normalization rejects records without the Proposal label

#### Scenario: Compatible calls remain valid

- **WHEN** an existing CLI or dedicated native discovery caller uses default filters, source=all, source=issue, or stage=proposal
- **THEN** the request is normalized to Proposal title/body search, while comments, change, design, and implement filters fail with a clear validation error

#### Scenario: Dedicated Search page exposes only applicable controls

- **WHEN** a user opens the dedicated self-hosted Search page
- **THEN** the page describes Proposal title/body discovery and retains scope, query, state, reset, and pagination without source or stage selectors

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/448#issuecomment-5264046893

### Requirement: Repository Issues page searches complete issue discussions

The self-hosted repository Issues page MUST provide an authorized repository-local search that matches Issue titles, Issue bodies, exact Issue numbers, and comment bodies through a native endpoint distinct from Proposal discovery. The operation MUST preserve state and selected-label filters, deterministic issue-centric ordering, pagination, canonical local navigation, bounded inert excerpts, and existing repository read authorization. It MUST use a 60-second database query deadline, honor earlier client cancellation, return no partial results on timeout, and expose a stable timeout problem. PostgreSQL search preparation MUST reconcile comment-body pg_bigm and pg_jieba indexes for this operation while the CLI and dedicated Search page remain Proposal-only with their existing five-second deadline.

#### Scenario: Issue title or body is searchable from the repository page

- **WHEN** an authorized user enters a term present in any Issue title or body on /<owner>/<repo>/issues
- **THEN** the repository full-discussion endpoint returns the matching Issue with a bounded issue-source excerpt and canonical local navigation

#### Scenario: Comment-only text finds its Issue

- **WHEN** the query appears only in an ordinary or typed comment body
- **THEN** the repository full-discussion endpoint returns the owning Issue with a bounded comment-source excerpt

#### Scenario: Repository filters and authorization remain effective

- **WHEN** a search request includes state and selected-label filters or targets a repository the caller cannot read
- **THEN** filters apply before pagination and the existing non-enumerating repository authorization boundary prevents disclosure

#### Scenario: Full search times out without partial results

- **WHEN** repository full-discussion database work has not completed within 60 seconds
- **THEN** the query is canceled and the API returns a stable timeout problem without a partial result page

#### Scenario: Search surfaces remain separate

- **WHEN** the same comment-only term is queried through the CLI or dedicated Search page
- **THEN** Proposal discovery does not return it and retains its five-second deadline

#### Scenario: Fresh PostgreSQL search setup supports comments

- **WHEN** a server enables PostgreSQL search without pre-existing application search indexes
- **THEN** preparation creates valid Issue text, comment text, and active-Proposal association indexes

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/448#issuecomment-5264841170
