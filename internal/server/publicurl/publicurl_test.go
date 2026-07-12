package publicurl

import (
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestOriginCanonicalizationAndAuthoritativeURLs(t *testing.T) {
	origins, err := New("https://API.Example.test:443", "https://issues.example.test", []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if err != nil {
		t.Fatal(err)
	}
	query := url.Values{"state": {"open"}, "page": {"2"}}
	got, err := origins.API.URL("/repos/o/r/issues", query)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.example.test/repos/o/r/issues?page=2&state=open" {
		t.Fatalf("URL = %q", got)
	}
	req := httptest.NewRequest("GET", "http://evil.example/", nil)
	req.Host = "evil.example"
	req.RemoteAddr = "203.0.113.4:1234"
	req.Header.Set("X-Forwarded-Host", "evil.example")
	if origins.TrustedProxies.RequestCameThroughTrustedProxy(req) {
		t.Fatal("untrusted request was treated as trusted")
	}
	if origins.API.String() != "https://api.example.test" {
		t.Fatalf("configured origin changed: %s", origins.API.String())
	}
}

func TestParseOriginRejectsInjectionAndNonOrigins(t *testing.T) {
	bad := []string{
		"https://user:secret@example.test",
		"https://example.test/path",
		"https://example.test?next=evil",
		"https://example.test#fragment",
		"https://example.test\\@evil.test",
		"https://example.test\r\nX-Evil: yes",
		"//example.test",
		"javascript:alert(1)",
	}
	for _, value := range bad {
		t.Run(strings.ReplaceAll(value, "/", "_"), func(t *testing.T) {
			if _, err := ParseOrigin("test", value); err == nil {
				t.Fatalf("ParseOrigin(%q) succeeded", value)
			}
		})
	}
}

func TestValidateSameOriginCursor(t *testing.T) {
	origin, err := ParseOrigin("api", "https://api.example.test")
	if err != nil {
		t.Fatal(err)
	}
	got, err := origin.ValidateSameOriginURL("https://API.EXAMPLE.TEST:443/repos/o/r/issues?page=2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "api.example.test" || got.Query().Get("page") != "2" {
		t.Fatalf("cursor = %s", got)
	}
	for _, cursor := range []string{
		"https://evil.example/repos/o/r/issues?page=2",
		"//api.example.test/repos/o/r/issues?page=2",
		"/repos/o/r/issues?page=2",
		"https://token@api.example.test/repos/o/r/issues?page=2",
	} {
		if _, err := origin.ValidateSameOriginURL(cursor); err == nil {
			t.Fatalf("cursor %q accepted", cursor)
		}
	}
}

func TestURLRejectsHostAndHeaderSyntaxInPath(t *testing.T) {
	origin, err := ParseOrigin("api", "https://api.example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"//evil.example/repos/o/r", "/\\evil.example/repos/o/r", "/safe\r\nX-Evil: yes", "/safe?next=evil", "/safe#evil"} {
		if _, err := origin.URL(path, nil); err == nil {
			t.Fatalf("path %q accepted", path)
		}
	}
}

func TestTrustedProxyPolicy(t *testing.T) {
	policy, err := NewProxyPolicy([]netip.Prefix{netip.MustParsePrefix("10.0.0.9/8"), netip.MustParsePrefix("2001:db8::/32")})
	if err != nil {
		t.Fatal(err)
	}
	for _, remote := range []string{"10.2.3.4:443", "[2001:db8::4]:443"} {
		if !policy.IsTrustedRemote(remote) {
			t.Fatalf("%q should be trusted", remote)
		}
	}
	if policy.IsTrustedRemote("192.0.2.1:443") {
		t.Fatal("unconfigured proxy trusted")
	}
}

func TestExplicitTransportPostureRejectsMixedOrConflictingSchemes(t *testing.T) {
	httpOrigins, err := NewWithPosture("http://10.0.0.8", "http://issues.internal", nil, TransportTrustedInternalHTTP)
	if err != nil || httpOrigins.Posture.SecureCookies() {
		t.Fatalf("trusted HTTP origins=%+v err=%v", httpOrigins, err)
	}
	if _, err := NewWithPosture("http://10.0.0.8", "https://issues.internal", nil, TransportTrustedInternalHTTP); err == nil {
		t.Fatal("mixed schemes accepted")
	}
	if _, err := NewWithPosture("http://10.0.0.8", "http://issues.internal", nil, TransportHTTPS); err == nil {
		t.Fatal("HTTP accepted for HTTPS posture")
	}
}
