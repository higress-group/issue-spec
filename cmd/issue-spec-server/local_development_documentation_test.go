package main

import (
	"strings"
	"testing"
)

func TestLocalDevelopmentDocumentationMatchesFixture(t *testing.T) {
	for _, path := range []string{
		"docs/self-hosting/local-development.md",
		"docs/self-hosting/local-development.zh-CN.md",
	} {
		document := readRepoFile(t, path)
		for _, required := range []string{
			"docker compose -f deployments/dev/compose.yaml --profile server up -d --build --wait",
			"docker compose -f deployments/dev/compose.yaml up -d --wait postgres",
			"make build-server",
			"BOOTSTRAP_SECRET_FILE",
			"TOKEN_PEPPER_FILE",
			"ENCRYPTION_KEY_FILE",
			"MIGRATIONS_MODE=auto",
			"SEARCH_MODE=disabled",
			"ISSUE_SPEC_PUBLIC_URL",
			"/readyz",
			"/bootstrap",
		} {
			if !strings.Contains(document, required) {
				t.Errorf("%s omits %q", path, required)
			}
		}
	}

	compose := readRepoFile(t, "deployments/dev/compose.yaml")
	publicURLDefault := "${ISSUE_SPEC_PUBLIC_URL:-http://127.0.0.1:8080}"
	if strings.Count(compose, publicURLDefault) != 2 {
		t.Errorf("development Compose fixture must use %q for both public origins", publicURLDefault)
	}

	for _, path := range []string{
		"README.md",
		"README.zh-CN.md",
		"docs/self-hosting/README.md",
		"docs/self-hosting/README.zh-CN.md",
		"deployments/dev/README.md",
	} {
		if !strings.Contains(readRepoFile(t, path), "local-development") {
			t.Errorf("%s does not link to the local development guide", path)
		}
	}
}
