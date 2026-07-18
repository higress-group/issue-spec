GO ?= go
NPM ?= npm
DIST_DIR ?= dist
IMAGE ?= issue-spec-server:dev

.PHONY: generate-web verify-generated verify-docs verify-requirements-acceptance docs-self-hosted-screenshots build-server test-server release-server release-cli verify-release docker-server backup-smoke

generate-web:
	cd web && $(NPM) ci && $(NPM) run build
	$(GO) run ./internal/server/staticui/cmd/generate ./web/dist ./internal/server/staticui
	$(GO) fmt ./internal/server/staticui/...

verify-generated: generate-web
	git diff --exit-code -- internal/server/staticui

verify-docs:
	$(GO) test ./cmd/issue-spec-server -run '^TestExternalAuthDocumentation'
	./hack/requirements-acceptance/verify.sh

verify-requirements-acceptance:
	$(GO) test ./internal/commands -run '^TestRequirementsAcceptance'
	$(GO) test ./internal/server/api/github/issues -run '^TestPublicContributorIssueCompatibility$$'
	./hack/requirements-acceptance/verify.sh

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
