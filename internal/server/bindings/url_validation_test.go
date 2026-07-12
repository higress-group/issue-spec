package bindings

import "testing"

func TestPersistedExternalURLValidation(t *testing.T) {
	for _, valid := range []string{
		"https://code.example",
		"https://code.example/",
		"https://code.example/acme/widgets",
		"https://code.example:8443/acme/widgets",
	} {
		if err := validateHTTPSURL(valid); err != nil {
			t.Errorf("valid HTTPS URL %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"https://code.example/acme/widgets?access_token=secret",
		"https://code.example/acme/widgets?",
		"https://code.example/acme/widgets#token",
		"https://user:secret@code.example/acme/widgets",
		"https://CODE.example/acme/widgets",
		"https://code.example:443/acme/widgets",
		"https://code.example/acme/../widgets",
		"https://code.example/acme//widgets",
		" https://code.example/acme/widgets",
		"https://code.example/acme/widgets\nnext",
		"https://code.example/acme/%0awidgets",
		"https://code.example./acme/widgets",
		"http://code.example/acme/widgets",
	} {
		if err := validateHTTPSURL(invalid); err == nil {
			t.Errorf("unsafe/non-canonical HTTPS URL %q accepted", invalid)
		}
	}
	if err := validateCloneURL("ssh://code.example/acme/widgets.git"); err != nil {
		t.Fatalf("valid SSH clone URL: %v", err)
	}
	for _, invalid := range []string{
		"ssh://code.example/acme/widgets.git?token=secret",
		"ssh://code.example/acme/widgets.git?",
		"ssh://user@code.example/acme/widgets.git",
	} {
		if err := validateCloneURL(invalid); err == nil {
			t.Errorf("unsafe clone URL %q accepted", invalid)
		}
	}
}
