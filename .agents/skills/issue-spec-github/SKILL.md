---
name: issue-spec-github
description: Use GitHub CLI for GitHub issues, pull requests, CI runs, and API queries that issue-spec does not wrap.
license: MIT
compatibility: Requires GitHub CLI (gh).
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "issue-spec"
---

# GitHub CLI

Use the `gh` CLI for GitHub-specific repository, issue, pull request, CI, and API operations that are outside issue-spec's workflow and discussion surfaces.

## When To Use

- Checking PR status, reviews, mergeability, or CI checks.
- Creating, viewing, updating, or closing generic GitHub issues when issue-spec does not provide a dedicated command.
- Listing or inspecting pull requests, workflow runs, releases, labels, or repository metadata.
- Calling read-only or adjacent GitHub API endpoints with `gh api` when issue-spec does not provide a dedicated command.

## When Not To Use

- Ordinary issue discussion writes, including a requested reply, answer, clarification, recommendation, findings report, or handoff. Write the body to a file and use `issue-spec comment create --repo <repo> --issue <n> --body-file <file> --json` so the selected issue backend owns the write.
- Do not use GitHub CLI issue-comment subcommands or direct REST/GraphQL issue-comment writes for ordinary discussion.
- Local git operations such as commit, branch, fetch, merge, or push. Use `git` directly.
- Non-GitHub repositories. Use the matching provider CLI instead.
- Complex code review across local diffs. Read the repository files directly and use issue-spec review commands for traceable findings.

## Setup

```bash
gh auth login
gh auth status
```

## Common Commands

```bash
gh issue list --repo owner/repo --state open
gh issue view 42 --repo owner/repo --json number,title,state,url,body

issue-spec comment create --repo owner/repo --issue 42 --body-file reply.md --json

gh pr list --repo owner/repo
gh pr view 17 --repo owner/repo --json number,title,state,headRefName,baseRefName,url
gh pr checks 17 --repo owner/repo

gh run list --repo owner/repo --limit 10
gh run view <run-id> --repo owner/repo --log-failed

gh api repos/owner/repo/labels --jq '.[].name'
```

## Notes

- Always pass `--repo owner/repo` when the current directory is not definitely inside the target repository.
- Use GitHub URLs directly when convenient, for example `gh pr view https://github.com/owner/repo/pull/17`.
- Prefer structured output with `--json` and `--jq` when another command or agent step consumes the result.
- Every ordinary issue discussion write goes through `issue-spec comment create`. This provider-neutral boundary also applies when GitHub CLI is authenticated.
- issue-spec owns the proposal, design, implement, typed comment, review, verify, and archive workflow state. Use `gh` for adjacent GitHub operations that are outside issue-spec's command surface.
