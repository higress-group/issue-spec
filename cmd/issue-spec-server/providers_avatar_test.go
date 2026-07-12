package main

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestConfiguredAvatarOriginsDefaultsGitHubAndRequiresHTTPS(t *testing.T) {
	id := uuid.New()
	raw := []byte(`{"providers":[{"id":"` + id.String() + `","name":"github","kind":"github-oauth","issuer":"https://github.com","client_id":"id","client_secret":"secret"}]}`)
	origins, err := configuredAvatarOrigins(raw)
	if err != nil || len(origins[id]) != 1 || origins[id][0] != "https://avatars.githubusercontent.com" {
		t.Fatalf("origins=%v err=%v", origins, err)
	}
	unsafe := []byte(`{"providers":[{"id":"` + id.String() + `","name":"oidc","kind":"oidc","issuer":"https://id.example","client_id":"id","client_secret":"secret","avatar_origins":["http://127.0.0.1"]}]}`)
	if _, err := configuredAvatarOrigins(unsafe); err == nil || !strings.Contains(err.Error(), "avatar") {
		t.Fatalf("unsafe origin error=%v", err)
	}
}
