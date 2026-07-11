package changes

import "testing"

func TestAcceptedClosureLinkIsSafeAndProviderNeutral(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "github", raw: `["https://github.com/acme/widgets/pull/42"]`, want: true},
		{name: "gitlab", raw: `["https://gitlab.example/acme/widgets/-/merge_requests/42"]`, want: true},
		{name: "gerrit", raw: `["https://review.example/c/widgets/+/42"]`, want: true},
		{name: "placeholder", raw: `["N/A"]`},
		{name: "plaintext", raw: `["http://code.example/change/42"]`},
		{name: "credentials", raw: `["https://token@code.example/change/42"]`},
		{name: "query", raw: `["https://code.example/change/42?token=secret"]`},
		{name: "fragment", raw: `["https://code.example/change/42#secret"]`},
		{name: "origin only", raw: `["https://code.example"]`},
		{name: "malformed", raw: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasAcceptedClosureLink([]byte(test.raw)); got != test.want {
				t.Fatalf("hasAcceptedClosureLink(%s) = %v, want %v", test.raw, got, test.want)
			}
		})
	}
}
