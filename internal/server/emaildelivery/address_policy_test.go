package emaildelivery

import (
	"reflect"
	"strings"
	"testing"
)

func TestAddressPolicyNormalizesDeduplicatesAndMatchesDomainBoundaries(t *testing.T) {
	policy, err := NewAddressPolicy([]string{" Example.Test ", "example.test", "CORP.EXAMPLE"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := policy.Suffixes(), []string{"example.test", "corp.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Suffixes() = %v, want %v", got, want)
	}
	for _, address := range []string{"person@example.test", "person@EXAMPLE.TEST", "person@team.example.test", "person@corp.example"} {
		if !policy.Allows(address) {
			t.Errorf("Allows(%q) = false", address)
		}
	}
	for _, address := range []string{"person@evilexample.test", "person@example.test.evil.test", "Name <person@example.test>", "broken"} {
		if policy.Allows(address) {
			t.Errorf("Allows(%q) = true", address)
		}
	}
}

func TestAddressPolicyEmptyAllowsAnyValidMailbox(t *testing.T) {
	policy, err := NewAddressPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Allows("person@personal.example") || policy.Allows("not an address") {
		t.Fatal("empty policy did not preserve valid any-domain behavior")
	}
}

func TestAddressPolicyRejectsInvalidSuffixesWithoutEchoingValues(t *testing.T) {
	for _, suffix := range []string{"", ".example.test", "example.test.", "*.example.test", "person@example.test", "bad_domain.test", "-bad.test"} {
		_, err := NewAddressPolicy([]string{suffix})
		if err == nil {
			t.Errorf("NewAddressPolicy(%q) succeeded", suffix)
		} else if suffix != "" && strings.Contains(err.Error(), suffix) {
			t.Errorf("error echoed configured suffix: %v", err)
		}
	}
}
