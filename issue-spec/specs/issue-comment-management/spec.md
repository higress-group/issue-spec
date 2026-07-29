# issue-comment-management

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/356

## Requirements

### Requirement: profile-neutral issue comment management

issue-spec MUST let an authorized user edit or delete one issue comment through the selected issue backend, and the self-hosted server and Issue UI MUST preserve authorization, transactional consistency, bounded outputs, and existing comment behavior.

#### Scenario: CLI edits a comment through the selected backend

- **WHEN** an authenticated caller supplies a repository, positive comment ID, and non-empty body file to comment edit
- **THEN** the CLI sends PATCH through the selected GitHub or self-hosted issue backend and returns only bounded mutation metadata

#### Scenario: CLI deletes a comment through the selected backend

- **WHEN** an authenticated caller supplies a repository and positive comment ID to comment delete
- **THEN** the CLI sends DELETE through the selected GitHub or self-hosted issue backend and returns a bounded deleted result without exposing the former body or token

#### Scenario: self-host deletion preserves repository consistency

- **WHEN** a comment author with contribution access or a triager deletes an existing self-hosted comment
- **THEN** the server atomically removes the comment and dependent reactions, mentions, and typed projection state, invalidates comment and artifact collections, and returns HTTP 204

#### Scenario: unauthorized deletion is rejected

- **WHEN** a caller who is neither the author with contribution access nor a triager attempts to delete a self-hosted comment
- **THEN** the server rejects the mutation without deleting the comment or changing collection versions

#### Scenario: Issue UI confirms single-comment deletion

- **WHEN** a permitted user chooses Delete on one Issue timeline comment and confirms the action
- **THEN** the UI deletes only that comment, reports any request failure accessibly, and refreshes the Issue and comment queries

#### Scenario: existing comment behavior remains compatible

- **WHEN** comment creation, editing, reactions, previews, typed projections, or created and edited webhooks are used
- **THEN** their existing behavior remains unchanged except that a successfully deleted comment is no longer readable or listed

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/356#issuecomment-5114207932
