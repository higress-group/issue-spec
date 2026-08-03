#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
english="$root/docs/self-hosting/requirements-onboarding.md"
chinese="$root/docs/self-hosting/requirements-onboarding.zh-CN.md"
assets="$root/docs/self-hosting/assets"
manifest="$assets/requirements-screenshots.sha256"

temp_root=$(mktemp -d "${TMPDIR:-/tmp}/issue-spec-requirements-acceptance.XXXXXX")
trap 'rm -rf "$temp_root"' EXIT HUP INT TERM

sed -n 's/.*requirements-step:\([^ ]*\).*/\1/p' "$english" > "$temp_root/english-steps"
sed -n 's/.*requirements-step:\([^ ]*\).*/\1/p' "$chinese" > "$temp_root/chinese-steps"
printf '%s\n' release pat context skill draft handoff > "$temp_root/expected-steps"
cmp "$temp_root/expected-steps" "$temp_root/english-steps"
cmp "$temp_root/expected-steps" "$temp_root/chinese-steps"

latest_download='https://github.com/higress-group/issue-spec/releases/latest/download'
for document in "$english" "$chinese"; do
  grep -Fq "$latest_download" "$document" || {
    echo "requirements onboarding must use the GitHub latest Release download endpoint: $document" >&2
    exit 1
  }
  if grep -Fq 'v1.8.0' "$document"; then
    echo "requirements onboarding must not pin the obsolete v1.8.0 example: $document" >&2
    exit 1
  fi
done

expected_images='requirements-pat-secret.png
requirements-pat-secret.zh-CN.png
requirements-simple-issue.png
requirements-simple-issue.zh-CN.png
requirements-standard-proposal.png
requirements-standard-proposal.zh-CN.png'

printf '%s\n' "$expected_images" | while IFS= read -r image; do
  test -s "$assets/$image" || { echo "missing requirements screenshot: $image" >&2; exit 1; }
  grep -Fq "assets/$image" "$english" "$chinese" || { echo "unreferenced requirements screenshot: $image" >&2; exit 1; }
done

compare_snapshot() {
  document_name=$1
  snapshot_path=$2
  test -s "$root/$snapshot_path" || { echo "missing source snapshot: $snapshot_path" >&2; exit 1; }
  cmp "$assets/$document_name" "$root/$snapshot_path" || {
    echo "stale requirements screenshot: $document_name differs from $snapshot_path" >&2
    exit 1
  }
}
compare_snapshot requirements-pat-secret.png web/tests/e2e/requirements-onboarding.spec.ts-snapshots/requirements-pat-secret-desktop-1440-linux.png
compare_snapshot requirements-pat-secret.zh-CN.png web/tests/e2e/requirements-onboarding.spec.ts-snapshots/requirements-pat-secret-zh-CN-desktop-1440-linux.png
compare_snapshot requirements-simple-issue.png web/src/features/issues/issues.visual.e2e.ts-snapshots/requirements-simple-issue-issues-desktop-1440-linux.png
compare_snapshot requirements-simple-issue.zh-CN.png web/src/features/issues/issues.visual.e2e.ts-snapshots/requirements-simple-issue-zh-CN-issues-desktop-1440-linux.png
compare_snapshot requirements-standard-proposal.png web/src/features/issues/issues.visual.e2e.ts-snapshots/requirements-standard-proposal-issues-desktop-1440-linux.png
compare_snapshot requirements-standard-proposal.zh-CN.png web/src/features/issues/issues.visual.e2e.ts-snapshots/requirements-standard-proposal-zh-CN-issues-desktop-1440-linux.png

test -s "$manifest" || { echo "missing requirements screenshot checksum manifest" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$assets" && sha256sum -c "$(basename "$manifest")")
else
  (cd "$assets" && shasum -a 256 -c "$(basename "$manifest")")
fi

printf '%s\n' "$expected_images" > "$temp_root/expected-images"
for path in "$assets"/requirements-*.png; do basename "$path"; done | sort > "$temp_root/actual-images"
if ! cmp -s "$temp_root/expected-images" "$temp_root/actual-images"; then
  echo "requirements screenshot set differs from the documented fixture set" >&2
  diff -u "$temp_root/expected-images" "$temp_root/actual-images" || true
  exit 1
fi

scan_files="$english $chinese
$root/internal/commands/requirements_acceptance_test.go
$root/web/tests/e2e/requirements-onboarding.spec.ts
$root/web/src/features/issues/issues.visual.e2e.ts"
if grep -En '(ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|Bearer[[:space:]]+[A-Za-z0-9._-]{20,}|eyJ[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}|alibaba-inc\.com)' $scan_files; then
  echo "credential-like or internal value found in requirements documentation fixtures" >&2
  exit 1
fi
if grep -En '(^|[^[:alnum:]_])(gh[[:space:]]+(release|attestation)|wget|Invoke-WebRequest)([^[:alnum:]_]|$)' "$english" "$chinese"; then
  echo "requirements onboarding installation must use curl-only Release downloads" >&2
  exit 1
fi

workflow="$root/.github/workflows/release.yml"
grep -Fq 'native-installer:' "$workflow"
grep -Fq 'runs-on: ${{ matrix.runner }}' "$workflow"
grep -Fq 'ubuntu-latest' "$workflow"
grep -Fq 'macos-15' "$workflow"
grep -Fq 'windows-2025' "$workflow"
grep -Fq 'needs: [package, native-installer]' "$workflow"

workflow_english="$root/docs/workflow.md"
workflow_chinese="$root/docs/workflow.zh-CN.md"
for document in "$workflow_english" "$workflow_chinese"; do
  for marker in \
    'change.create' \
    'change.comment' \
    'evidence.snapshot' \
    '### Implementation Rationale' \
    'deprecated_workflow'; do
    grep -Fq -- "$marker" "$document" || {
      echo "human-handoff workflow document missing $marker: $document" >&2
      exit 1
    }
  done
  if grep -Eq 'minimal-merge-authority|issue-spec merge-check|issue-spec code-change merge|issue-spec workflow preflight|provider-authority-token' "$document"; then
    echo "human-handoff workflow document retains automatic merge machinery: $document" >&2
    exit 1
  fi
done

release_manifest="$root/.agents/skills/issue-spec-workflow/release.json"
test -s "$release_manifest" || { echo "missing generated workflow release manifest" >&2; exit 1; }
grep -Fq '"schema": "issue-spec.generated-workflow/v1"' "$release_manifest"
grep -Fq '"content_digest": "sha256:' "$release_manifest"

for retired in \
  "$root/.agents/skills/issue-spec-review/SKILL.md" \
  "$root/.agents/skills/issue-spec-verify/SKILL.md" \
  "$root/.claude/commands/issue-spec/review.md" \
  "$root/.claude/commands/issue-spec/verify.md"; do
  test ! -e "$retired" || { echo "retired generated authority asset exists: $retired" >&2; exit 1; }
done

generated_assets="$root/.agents/skills/issue-spec-workflow/SKILL.md
$root/.agents/skills/issue-spec-apply/SKILL.md
$root/.agents/skills/issue-spec-propose/SKILL.md
$root/.claude/commands/issue-spec/apply.md
$root/.claude/commands/issue-spec/propose.md"
rationale_assets="$root/.agents/skills/issue-spec-workflow/SKILL.md
$root/.agents/skills/issue-spec-apply/SKILL.md
$root/.agents/skills/issue-spec-github/SKILL.md
$root/.claude/commands/issue-spec/apply.md"
for asset in $rationale_assets; do
  grep -Fq -- '### Implementation Rationale' "$asset" || {
    echo "active generated workflow omits the human review rationale: $asset" >&2
    exit 1
  }
done
for asset in \
  "$root/.agents/skills/issue-spec-workflow/SKILL.md" \
  "$root/.agents/skills/issue-spec-apply/SKILL.md" \
  "$root/.claude/commands/issue-spec/apply.md"; do
  for phrase in \
    'line-rationale drafts' \
    'stable symbol plus changed-line anchor' \
    'secret, raw payload, or credential' \
    'non-blocking inline discussion' \
    'path:symbol/line' \
    'Invalid, stale, or sensitive drafts'; do
    grep -Fq -- "$phrase" "$asset" || {
      echo "active generated workflow omits worker-owned inline rationale rule '$phrase': $asset" >&2
      exit 1
    }
  done
done
for phrase in 'commit_id' 'side=RIGHT' 'never rewrite them while claiming worker authorship'; do
  grep -Fq -- "$phrase" "$root/.agents/skills/issue-spec-github/SKILL.md" || {
    echo "generated GitHub guidance omits inline rationale publication rule '$phrase'" >&2
    exit 1
  }
done
if grep -En 'issue-spec (review sync|verify submit|code-change rationale)' $generated_assets; then
  echo "active generated workflow retains a retired authority command" >&2
  exit 1
fi
if grep -REn --include='*.md' 'issue-spec (review (submit|sync)|verify (submit|final)|code-change rationale|finalize)' "$root/docs"; then
  echo "operator documentation retains an active retired authority command" >&2
  exit 1
fi

echo "Requirements onboarding acceptance evidence is complete and self-consistent."
