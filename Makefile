GO ?= go
NPM ?= npm
DIST_DIR ?= dist
IMAGE ?= issue-spec-server:dev

.PHONY: generate-web verify-generated verify-docs verify-requirements-acceptance verify-enterprise-provider verify-workflow-cutover candidate-cli-dogfood docs-self-hosted-screenshots build-server test-server release-server release-cli verify-release docker-server backup-smoke test-fast test-git-contract test-full test-baseline

# test-fast runs the deterministic, Git-free command orchestration tier and
# asserts its wall time against testdata/test-baseline.json.
test-fast:
	./scripts/test-tier.sh fast

# test-git-contract runs the bounded real-Git contract tier (real
# worktree/recovery/integration coverage plus the command-to-real-workspace
# smoke path).
test-git-contract:
	./scripts/test-tier.sh git-contract

# test-full runs the whole-module test suite and the public enterprise-provider
# wire/action conformance fixtures used by CI.
test-full: verify-enterprise-provider
	./scripts/test-tier.sh full

# test-baseline records a fresh same-host cold-cache baseline.
test-baseline:
	./scripts/test-tier.sh baseline

generate-web:
	cd web && $(NPM) ci && $(NPM) run build
	$(GO) run ./internal/server/staticui/cmd/generate ./web/dist ./internal/server/staticui
	$(GO) fmt ./internal/server/staticui/...

verify-generated: generate-web
	git diff --exit-code -- internal/server/staticui

verify-docs:
	$(GO) test ./cmd/issue-spec-server -run 'Documentation'
	./hack/requirements-acceptance/verify.sh

verify-requirements-acceptance:
	$(GO) test ./internal/commands -run '^TestRequirementsAcceptance'
	$(GO) test ./internal/server/api/github/issues -run '^TestPublicContributorIssueCompatibility$$'
	./hack/requirements-acceptance/verify.sh

verify-enterprise-provider:
	python3 -m unittest discover -s .agents/skills/configure-enterprise-provider/scripts -p '*_test.py'

# verify-workflow-cutover checks generated human-handoff guidance, the minimal
# provider operation contract, and the bilingual operator documentation.
verify-workflow-cutover: verify-enterprise-provider
	$(GO) test ./internal/workflow ./internal/templates ./internal/commands
	./hack/requirements-acceptance/verify.sh

# Candidate builds dogfood provider-aware generation and self-hosted handoff.
candidate-cli-dogfood:
	$(GO) test ./internal/commands -run '^(TestWriteWorkflowArtifactsWithProviderFollowsCapabilityMatrix|TestSelfHostedInitSupportsOperationOnlyProvidersThroughHumanHandoff)$$'

docs-self-hosted-screenshots:
	./hack/update-self-hosted-doc-screenshots.sh

build-server:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(DIST_DIR)/issue-spec-server ./cmd/issue-spec-server

test-server:
	$(GO) test ./cmd/issue-spec-server ./internal/server/...

release-server: verify-generated test-server
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w -buildid=' -o $(DIST_DIR)/issue-spec-server ./cmd/issue-spec-server

release-cli:
	test -n "$(RELEASE_REF)"
	test -n "$(RELEASE_REVISION)"
	test -n "$(SOURCE_DATE_EPOCH)"
	$(GO) run ./hack/release/cmd/package --root . --out $(DIST_DIR)/release --ref "$(RELEASE_REF)" --revision "$(RELEASE_REVISION)" --source-date-epoch "$(SOURCE_DATE_EPOCH)"

verify-release:
	$(GO) test ./internal/buildinfo ./internal/requirements ./internal/commands ./hack/release/...
	$(GO) run ./hack/release/cmd/package --verify $(DIST_DIR)/release

docker-server:
	docker build --target runtime -t $(IMAGE) .

backup-smoke:
	test -n "$(BACKUP_DIR)"
	test -n "$(KEY_DIR)"
	mkdir -p "$(BACKUP_DIR)"
	pg_dump --format=custom --file="$(BACKUP_DIR)/issue-spec.pgdump" "$(DATABASE_URL)"
	tar -C "$(KEY_DIR)" -cf "$(BACKUP_DIR)/issue-spec-keys.tar" .
	test -s "$(BACKUP_DIR)/issue-spec.pgdump"
	test -s "$(BACKUP_DIR)/issue-spec-keys.tar"
