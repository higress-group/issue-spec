package gitidentity

import (
	"strings"
	"testing"
)

func TestNormalizeAcceptsEmptyOrCompleteIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		email string
		want  Identity
	}{
		{want: Identity{}},
		{name: "  Issue Spec Runner  ", email: " runner@example.test ", want: Identity{Name: "Issue Spec Runner", Email: "runner@example.test"}},
		{name: "运行器", email: "runner+automation@example.test", want: Identity{Name: "运行器", Email: "runner+automation@example.test"}},
	} {
		got, err := Normalize(tc.name, tc.email)
		if err != nil {
			t.Fatalf("Normalize(%q, %q): %v", tc.name, tc.email, err)
		}
		if got != tc.want {
			t.Fatalf("Normalize(%q, %q) = %+v, want %+v", tc.name, tc.email, got, tc.want)
		}
	}
}

func TestNormalizeRejectsPartialOrUnsafeIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		email string
	}{
		{name: "Runner"},
		{email: "runner@example.test"},
		{name: "Runner\nInjected", email: "runner@example.test"},
		{name: "Runner", email: "Runner <runner@example.test>"},
		{name: "Runner", email: "runner @example.test"},
		{name: "Runner", email: "runner\u007f@example.test"},
		{name: strings.Repeat("a", maxNameBytes+1), email: "runner@example.test"},
		{name: "Runner", email: strings.Repeat("a", maxEmailBytes) + "@example.test"},
	} {
		if _, err := Normalize(tc.name, tc.email); err == nil {
			t.Fatalf("Normalize(%q, %q) unexpectedly succeeded", tc.name, tc.email)
		}
	}
}
