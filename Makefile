GO ?= go
NPM ?= npm
DIST_DIR ?= dist
IMAGE ?= issue-spec-server:dev

.PHONY: generate-web verify-generated build-server test-server release-server docker-server backup-smoke

generate-web:
	cd web && $(NPM) ci && $(NPM) run build
	$(GO) run ./internal/server/staticui/cmd/generate ./web/dist ./internal/server/staticui
	$(GO) fmt ./internal/server/staticui/...

verify-generated: generate-web
	git diff --exit-code -- internal/server/staticui

build-server:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(DIST_DIR)/issue-spec-server ./cmd/issue-spec-server

test-server:
	$(GO) test ./cmd/issue-spec-server ./internal/server/...

release-server: verify-generated test-server
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w -buildid=' -o $(DIST_DIR)/issue-spec-server ./cmd/issue-spec-server

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
