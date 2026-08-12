# issue-discovery-and-search

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/448

## Requirements

### Requirement: Issue search is limited to Proposal titles and bodies

The issue-spec CLI and self-hosted web/native search MUST return only canonical Proposal Issues and MUST match query text only against each Proposal Issue title and body. The self-hosted PostgreSQL query MUST restrict candidates to active Proposal artifacts before text matching and MUST NOT search or hydrate comments, ordinary Issues, Design/Implement Issue bodies, issue numbers, or standalone change-key metadata. GitHub CLI search MUST require the canonical issue-spec/proposal label with in:title,body and MUST defensively discard non-Proposal provider results. Authorization, state filtering, deterministic ordering, pagination, bounded plain-text excerpts, provider trust boundaries, and the server-owned query deadline MUST remain intact. Compatibility inputs source=all|issue and stage unset|proposal MAY be accepted and normalized to this fixed scope; contradictory comment, change, design, or implement filters MUST be rejected.

#### Scenario: Proposal title or body is discoverable

- **WHEN** an authorized canonical Proposal Issue contains the query in its title or body
- **THEN** CLI and self-hosted web/native search return the Proposal with a bounded issue-source excerpt

#### Scenario: Non-Proposal discussion content is excluded

- **WHEN** the query appears only in a comment, ordinary Issue, Design Issue, Implement Issue, issue number, or standalone artifact metadata
- **THEN** the search returns no result for that content and performs no comment matching or hydration

#### Scenario: PostgreSQL narrows before matching

- **WHEN** a repository contains many Issues and comments but relatively few active Proposals
- **THEN** the database derives the active Proposal candidate rows before evaluating title/body matching and ranking

#### Scenario: GitHub uses the same Proposal-only contract

- **WHEN** the CLI searches through a GitHub profile
- **THEN** the provider query requires issue-spec/proposal and in:title,body, and response normalization rejects records without the Proposal label

#### Scenario: Compatible calls remain valid

- **WHEN** an existing CLI or native API caller uses default filters, source=all, source=issue, or stage=proposal
- **THEN** the request is normalized to Proposal title/body search, while comments, change, design, and implement filters fail with a clear validation error

#### Scenario: Web search exposes only applicable controls

- **WHEN** a user opens the self-hosted search page
- **THEN** the page describes Proposal title/body search and retains scope, query, state, reset, and pagination without source or stage selectors

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/448#issuecomment-5264046893
