package githuboauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeAdmissionAndConditionalScopes(t *testing.T) {
	if _, err := NormalizeAdmission(nil, true, "https://github.com", DefaultUserURL); err == nil {
		t.Fatal("production accepted omitted admission policy")
	}
	development, err := NormalizeAdmission(nil, false, "https://github.com", DefaultUserURL)
	if err != nil || development.Mode != AdmissionUnrestricted {
		t.Fatalf("development admission = %+v, %v", development, err)
	}
	unrestricted, err := NormalizeAdmission(&AdmissionConfig{Mode: AdmissionUnrestricted}, true,
		"https://github.com", DefaultUserURL)
	if err != nil || strings.Contains(strings.Join(AdmissionScopes(nil, unrestricted.Mode), " "), "read:org") {
		t.Fatalf("unrestricted admission/scopes = %+v %v %v", unrestricted, AdmissionScopes(nil, unrestricted.Mode), err)
	}
	restricted, err := NormalizeAdmission(&AdmissionConfig{Mode: AdmissionOrganizationRestricted,
		Organizations: []ApprovedOrganization{{Login: " Zeta ", ID: "42"}, {Login: "alpha"}}}, true,
		"https://github.com", DefaultUserURL)
	if err != nil {
		t.Fatal(err)
	}
	if restricted.MembershipURL != DefaultMembershipURL || restricted.Organizations[0].Login != "alpha" ||
		restricted.Organizations[1].stableID != 42 {
		t.Fatalf("normalized restricted admission = %+v", restricted)
	}
	scopes := AdmissionScopes([]string{"read:user", "read:org", "read:user"}, restricted.Mode)
	if strings.Join(scopes, ",") != "read:user,read:org" {
		t.Fatalf("restricted scopes = %v", scopes)
	}
	enterprise, err := NormalizeAdmission(&AdmissionConfig{Mode: AdmissionOrganizationRestricted,
		Organizations: []ApprovedOrganization{{Login: "acme"}}}, false,
		"http://ghe.test", "http://ghe.test/api/v3/user")
	if err != nil || enterprise.MembershipURL != "http://ghe.test/api/v3/user/memberships/orgs" {
		t.Fatalf("derived enterprise membership URL = %q, %v", enterprise.MembershipURL, err)
	}
}

func TestNormalizeAdmissionRejectsUnsafeOrAmbiguousConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		policy AdmissionConfig
		issuer string
		user   string
		prod   bool
	}{
		{name: "unknown mode", policy: AdmissionConfig{Mode: "everyone"}},
		{name: "unrestricted organizations", policy: AdmissionConfig{Mode: AdmissionUnrestricted, Organizations: []ApprovedOrganization{{Login: "acme"}}}},
		{name: "empty restricted", policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted}},
		{name: "duplicate login", policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted, Organizations: []ApprovedOrganization{{Login: "ACME"}, {Login: "acme"}}}},
		{name: "duplicate id", policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted, Organizations: []ApprovedOrganization{{Login: "acme", ID: "7"}, {Login: "other", ID: "7"}}}},
		{name: "noncanonical id", policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted, Organizations: []ApprovedOrganization{{Login: "acme", ID: "007"}}}},
		{name: "unsafe login", policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted, Organizations: []ApprovedOrganization{{Login: "../acme"}}}},
		{name: "cross origin membership", policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted, Organizations: []ApprovedOrganization{{Login: "acme"}}, MembershipURL: "https://evil.example/user/memberships/orgs"}, issuer: "https://github.com", user: DefaultUserURL, prod: true},
		{name: "enterprise missing membership", policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted, Organizations: []ApprovedOrganization{{Login: "acme"}}}, issuer: "https://ghe.example", user: "https://ghe.example/api/v3/user", prod: true},
		{name: "production http", policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted, Organizations: []ApprovedOrganization{{Login: "acme"}}, MembershipURL: "http://ghe.example/api/v3/user/memberships/orgs"}, issuer: "https://ghe.example", user: "http://ghe.example/api/v3/user", prod: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issuer := test.issuer
			if issuer == "" {
				issuer = "https://github.com"
			}
			user := test.user
			if user == "" {
				user = DefaultUserURL
			}
			if _, err := NormalizeAdmission(&test.policy, test.prod, issuer, user); err == nil {
				t.Fatalf("NormalizeAdmission accepted %+v", test.policy)
			}
		})
	}
}

func TestMembershipResponseAndFailureClassMatrix(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		headers     map[string]string
		contentType string
		body        string
		want        AdmissionFailureClass
	}{
		{name: "active", status: 200, contentType: "application/json", body: `{"state":"active","organization":{"id":42,"login":"acme"},"user":{"id":99}}`},
		{name: "pending", status: 200, contentType: "application/json", body: `{"state":"pending","organization":{"id":42,"login":"acme"},"user":{"id":99}}`, want: AdmissionPending},
		{name: "absent", status: 404, want: AdmissionNoActiveMembership},
		{name: "missing scope", status: 403, want: AdmissionMissingScope},
		{name: "sso", status: 403, headers: map[string]string{"X-GitHub-SSO": "required"}, want: AdmissionSSORestricted},
		{name: "rate", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0"}, want: AdmissionRateLimited},
		{name: "too many", status: 429, want: AdmissionRateLimited},
		{name: "upstream", status: 503, want: AdmissionUpstreamUnavailable},
		{name: "wrong media", status: 200, contentType: "text/html", body: `{}`, want: AdmissionInvalidResponse},
		{name: "malformed", status: 200, contentType: "application/json", body: `{`, want: AdmissionInvalidResponse},
		{name: "unknown state", status: 200, contentType: "application/json", body: `{"state":"invited","organization":{"id":42,"login":"acme"}}`, want: AdmissionInvalidResponse},
		{name: "wrong organization", status: 200, contentType: "application/json", body: `{"state":"active","organization":{"id":43,"login":"other"}}`, want: AdmissionOrganizationIdentityMismatch},
		{name: "wrong user", status: 200, contentType: "application/json", body: `{"state":"active","organization":{"id":42,"login":"acme"},"user":{"id":100}}`, want: AdmissionInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/memberships/acme" {
					t.Errorf("membership path = %q", r.URL.Path)
				}
				for name, value := range test.headers {
					w.Header().Set(name, value)
				}
				if test.contentType != "" {
					w.Header().Set("Content-Type", test.contentType)
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			gate := &organizationAdmissionGate{policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted,
				MembershipURL: server.URL + "/memberships"}}
			result, class := gate.lookupMembership(t.Context(), server.Client(), "99", ApprovedOrganization{Login: "acme", stableID: 42})
			if class != test.want {
				t.Fatalf("class = %q, want %q", class, test.want)
			}
			if class == "" && (result.externalID != 42 || result.observedLogin != "acme") {
				t.Fatalf("active result = %+v", result)
			}
		})
	}
}

func TestMembershipClientRejectsRedirectOversizeAndTimeout(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		followed := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/target" {
				followed = true
				return
			}
			http.Redirect(w, r, "/target", http.StatusFound)
		}))
		defer server.Close()
		gate := &organizationAdmissionGate{policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted, MembershipURL: server.URL}}
		_, class := gate.lookupMembership(t.Context(), boundedMembershipClient(server.Client()), "99", ApprovedOrganization{Login: "acme"})
		if class != AdmissionInvalidResponse || followed {
			t.Fatalf("redirect class=%q followed=%v", class, followed)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(strings.Repeat("x", maxMembershipBody+1)))
		}))
		defer server.Close()
		gate := &organizationAdmissionGate{policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted, MembershipURL: server.URL}}
		_, class := gate.lookupMembership(t.Context(), server.Client(), "99", ApprovedOrganization{Login: "acme"})
		if class != AdmissionInvalidResponse {
			t.Fatalf("oversize class=%q", class)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(time.Second):
				fmt.Fprint(w, `{}`)
			case <-r.Context().Done():
			}
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		defer cancel()
		gate := &organizationAdmissionGate{policy: AdmissionConfig{Mode: AdmissionOrganizationRestricted, MembershipURL: server.URL}}
		_, class := gate.lookupMembership(ctx, server.Client(), "99", ApprovedOrganization{Login: "acme"})
		if class != AdmissionUpstreamUnavailable {
			t.Fatalf("timeout class=%q", class)
		}
	})
}
