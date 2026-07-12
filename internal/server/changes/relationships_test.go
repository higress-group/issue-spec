package changes

import "testing"

func TestSafeCanonicalHTTPSURL(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"https://code.example/acme/widgets/changes/42",
		"https://code.example/",
		"https://code.example",
	} {
		if !safeCanonicalHTTPSURL(value) {
			t.Errorf("safeCanonicalHTTPSURL(%q) = false", value)
		}
	}
	for _, value := range []string{
		"http://code.example/acme/widgets/changes/42",
		"https://user@code.example/acme/widgets/changes/42",
		"https://code.example:443/acme/widgets/changes/42",
		"https://CODE.example/acme/widgets/changes/42",
		"https://code.example/acme/../admin",
		"https://code.example/acme/widgets?token=secret",
		"https://code.example/acme/widgets#fragment",
		"https://code.example./acme/widgets",
		" https://code.example/acme/widgets",
	} {
		if safeCanonicalHTTPSURL(value) {
			t.Errorf("safeCanonicalHTTPSURL(%q) = true", value)
		}
	}
}
