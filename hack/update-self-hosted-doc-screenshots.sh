#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
web="$root/web"
assets="$root/docs/self-hosting/assets"
platform=$(node -p 'process.platform')

cd "$web"
npm ci
npm run build
npx playwright test --config playwright.config.ts --project=desktop-1440 tests/e2e/shell.spec.ts --update-snapshots
npx playwright test --config playwright.config.ts --project=desktop-1440 tests/e2e/requirements-onboarding.spec.ts --update-snapshots
npx playwright test --config src/features/issues/playwright.issues.config.ts --project=issues-desktop-1440 --update-snapshots
npx playwright test --config src/features/boards/playwright.boards.config.ts --project=boards-desktop-1440 --update-snapshots
npx playwright test --config src/repos/playwright.integrations.config.ts --project=integrations-desktop --update-snapshots

ISSUE_SPEC_E2E_LANGUAGE=zh-CN npx playwright test --config playwright.config.ts --project=desktop-1440 tests/e2e/shell.spec.ts --update-snapshots
ISSUE_SPEC_E2E_LANGUAGE=zh-CN npx playwright test --config playwright.config.ts --project=desktop-1440 tests/e2e/requirements-onboarding.spec.ts --update-snapshots
ISSUE_SPEC_E2E_LANGUAGE=zh-CN npx playwright test --config src/features/issues/playwright.issues.config.ts --project=issues-desktop-1440 --update-snapshots
ISSUE_SPEC_E2E_LANGUAGE=zh-CN npx playwright test --config src/features/boards/playwright.boards.config.ts --project=boards-desktop-1440 --update-snapshots
ISSUE_SPEC_E2E_LANGUAGE=zh-CN npx playwright test --config src/repos/playwright.integrations.config.ts --project=integrations-desktop --update-snapshots

mkdir -p "$assets"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/issue-list-issues-desktop-1440-$platform.png" "$assets/self-hosted-dashboard.png"
cp "tests/e2e/shell.spec.ts-snapshots/runner-service-account-desktop-1440-$platform.png" "$assets/self-hosted-runner-service-account.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/issue-detail-issues-desktop-1440-$platform.png" "$assets/self-hosted-issue-detail.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/review-projection-issues-desktop-1440-$platform.png" "$assets/self-hosted-review-projection.png"
cp "src/features/boards/boards.visual.e2e.ts-snapshots/change-board-boards-desktop-1440-$platform.png" "$assets/self-hosted-change-board.png"
cp "src/features/boards/boards.visual.e2e.ts-snapshots/change-detail-boards-desktop-1440-$platform.png" "$assets/self-hosted-change-detail.png"
cp "src/repos/integrations.e2e.ts-snapshots/webhook-integrations-integrations-desktop-$platform.png" "$assets/self-hosted-webhook-integrations.png"
cp "src/repos/integrations.e2e.ts-snapshots/runner-intake-config-integrations-desktop-$platform.png" "$assets/self-hosted-runner-intake.png"
cp "src/repos/integrations.e2e.ts-snapshots/runner-intake-credentials-integrations-desktop-$platform.png" "$assets/self-hosted-runner-credentials.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/runner-command-issues-desktop-1440-$platform.png" "$assets/self-hosted-runner-command.png"
cp "tests/e2e/requirements-onboarding.spec.ts-snapshots/requirements-pat-secret-desktop-1440-$platform.png" "$assets/requirements-pat-secret.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/requirements-simple-issue-issues-desktop-1440-$platform.png" "$assets/requirements-simple-issue.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/requirements-standard-proposal-issues-desktop-1440-$platform.png" "$assets/requirements-standard-proposal.png"

cp "src/features/issues/issues.visual.e2e.ts-snapshots/issue-list-zh-CN-issues-desktop-1440-$platform.png" "$assets/self-hosted-dashboard.zh-CN.png"
cp "tests/e2e/shell.spec.ts-snapshots/runner-service-account-zh-CN-desktop-1440-$platform.png" "$assets/self-hosted-runner-service-account.zh-CN.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/issue-detail-zh-CN-issues-desktop-1440-$platform.png" "$assets/self-hosted-issue-detail.zh-CN.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/review-projection-zh-CN-issues-desktop-1440-$platform.png" "$assets/self-hosted-review-projection.zh-CN.png"
cp "src/features/boards/boards.visual.e2e.ts-snapshots/change-board-zh-CN-boards-desktop-1440-$platform.png" "$assets/self-hosted-change-board.zh-CN.png"
cp "src/features/boards/boards.visual.e2e.ts-snapshots/change-detail-zh-CN-boards-desktop-1440-$platform.png" "$assets/self-hosted-change-detail.zh-CN.png"
cp "src/repos/integrations.e2e.ts-snapshots/webhook-integrations-zh-CN-integrations-desktop-$platform.png" "$assets/self-hosted-webhook-integrations.zh-CN.png"
cp "src/repos/integrations.e2e.ts-snapshots/runner-intake-config-zh-CN-integrations-desktop-$platform.png" "$assets/self-hosted-runner-intake.zh-CN.png"
cp "src/repos/integrations.e2e.ts-snapshots/runner-intake-credentials-zh-CN-integrations-desktop-$platform.png" "$assets/self-hosted-runner-credentials.zh-CN.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/runner-command-zh-CN-issues-desktop-1440-$platform.png" "$assets/self-hosted-runner-command.zh-CN.png"
cp "tests/e2e/requirements-onboarding.spec.ts-snapshots/requirements-pat-secret-zh-CN-desktop-1440-$platform.png" "$assets/requirements-pat-secret.zh-CN.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/requirements-simple-issue-zh-CN-issues-desktop-1440-$platform.png" "$assets/requirements-simple-issue.zh-CN.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/requirements-standard-proposal-zh-CN-issues-desktop-1440-$platform.png" "$assets/requirements-standard-proposal.zh-CN.png"

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$assets" && sha256sum requirements-*.png > requirements-screenshots.sha256)
else
  (cd "$assets" && shasum -a 256 requirements-*.png > requirements-screenshots.sha256)
fi

echo "Updated self-hosted documentation screenshots in $assets"
