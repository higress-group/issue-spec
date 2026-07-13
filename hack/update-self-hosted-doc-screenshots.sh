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
npx playwright test --config src/features/issues/playwright.issues.config.ts --project=issues-desktop-1440 --update-snapshots
npx playwright test --config src/features/boards/playwright.boards.config.ts --project=boards-desktop-1440 --update-snapshots
npx playwright test --config src/repos/playwright.integrations.config.ts --project=integrations-desktop --update-snapshots

ISSUE_SPEC_E2E_LANGUAGE=zh-CN npx playwright test --config playwright.config.ts --project=desktop-1440 tests/e2e/shell.spec.ts --update-snapshots
ISSUE_SPEC_E2E_LANGUAGE=zh-CN npx playwright test --config src/features/issues/playwright.issues.config.ts --project=issues-desktop-1440 --update-snapshots
ISSUE_SPEC_E2E_LANGUAGE=zh-CN npx playwright test --config src/features/boards/playwright.boards.config.ts --project=boards-desktop-1440 --update-snapshots
ISSUE_SPEC_E2E_LANGUAGE=zh-CN npx playwright test --config src/repos/playwright.integrations.config.ts --project=integrations-desktop --update-snapshots

mkdir -p "$assets"
cp "tests/e2e/shell.spec.ts-snapshots/dashboard-desktop-1440-$platform.png" "$assets/self-hosted-dashboard.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/issue-detail-issues-desktop-1440-$platform.png" "$assets/self-hosted-issue-detail.png"
cp "src/features/boards/boards.visual.e2e.ts-snapshots/change-board-boards-desktop-1440-$platform.png" "$assets/self-hosted-change-board.png"
cp "src/features/boards/boards.visual.e2e.ts-snapshots/change-detail-boards-desktop-1440-$platform.png" "$assets/self-hosted-change-detail.png"
cp "src/repos/integrations.e2e.ts-snapshots/webhook-integrations-integrations-desktop-$platform.png" "$assets/self-hosted-webhook-integrations.png"

cp "tests/e2e/shell.spec.ts-snapshots/dashboard-zh-CN-desktop-1440-$platform.png" "$assets/self-hosted-dashboard.zh-CN.png"
cp "src/features/issues/issues.visual.e2e.ts-snapshots/issue-detail-zh-CN-issues-desktop-1440-$platform.png" "$assets/self-hosted-issue-detail.zh-CN.png"
cp "src/features/boards/boards.visual.e2e.ts-snapshots/change-board-zh-CN-boards-desktop-1440-$platform.png" "$assets/self-hosted-change-board.zh-CN.png"
cp "src/features/boards/boards.visual.e2e.ts-snapshots/change-detail-zh-CN-boards-desktop-1440-$platform.png" "$assets/self-hosted-change-detail.zh-CN.png"
cp "src/repos/integrations.e2e.ts-snapshots/webhook-integrations-zh-CN-integrations-desktop-$platform.png" "$assets/self-hosted-webhook-integrations.zh-CN.png"

echo "Updated self-hosted documentation screenshots in $assets"
