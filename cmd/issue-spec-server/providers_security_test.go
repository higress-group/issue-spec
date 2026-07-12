package main

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
)

func TestConfigureAdaptersRejectsUntrustedProductionGitHubUserEndpointBeforeStartup(t *testing.T) {
	for _, test := range []struct {
		name, issuer, userURL, want string
	}{
		{name: "http", issuer: "https://ghe.example", userURL: "http://ghe.example/api/v3/user", want: "HTTPS"},
		{name: "cross origin", issuer: "https://ghe.example", userURL: "https://evil.example/user", want: "origin"},
		{name: "query", issuer: "https://ghe.example", userURL: "https://ghe.example/user?token=x", want: "canonical"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(`{"providers":[{"id":"` + uuid.NewString() + `","name":"github","kind":"github-oauth","issuer":"` + test.issuer + `","client_id":"id","client_secret":"secret","user_url":"` + test.userURL + `"}]}`)
			if _, err := configureAdapters(context.Background(), nil, nil, publicurl.Origins{}, raw, true); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("configureAdapters error = %v, want %q", err, test.want)
			}
		})
	}
}
