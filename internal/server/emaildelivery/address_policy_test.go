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
	policies := []AddressPolicy{{}}
	for _, configured := range [][]string{nil, {}} {
		policy, err := NewAddressPolicy(configured)
		if err != nil {
			t.Fatal(err)
		}
		policies = append(policies, policy)
	}
	for _, policy := range policies {
		for _, address := range []string{"person@personal.example", "person@例子.测试", "person@[192.0.2.1]"} {
			if !policy.Allows(address) {
				t.Errorf("empty policy Allows(%q) = false", address)
			}
		}
		for _, address := range []string{"not an address", "Name <person@example.test>", "person@example.test\r\nBcc: other@example.test"} {
			if policy.Allows(address) {
				t.Errorf("empty policy Allows(%q) = true", address)
			}
		}
	}
}

func TestAddressPolicyConfiguredSuffixStillRequiresASCIIDNSDomain(t *testing.T) {
	policy, err := NewAddressPolicy([]string{"example.test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"person@例子.测试", "person@[192.0.2.1]", "person@evilexample.test", "person@example.test.evil.test"} {
		if policy.Allows(address) {
			t.Errorf("configured policy Allows(%q) = true", address)
		}
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
