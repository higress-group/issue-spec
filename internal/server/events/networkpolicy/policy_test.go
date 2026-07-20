package networkpolicy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPolicyBlocksSpecialAddressesAndRequiresExplicitPrivateAllowance(t *testing.T) {
	policy := Policy{Production: true}
	for _, raw := range []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.1.1",
		"169.254.169.254", "0.0.0.0", "224.0.0.1", "::1", "::", "fe80::1",
		"fc00::1", "ff02::1", "fd00:ec2::254",
	} {
		if err := policy.CheckAddress(netip.MustParseAddr(raw)); !errors.Is(err, ErrAddressDenied) {
			t.Fatalf("address %s error = %v", raw, err)
		}
	}
	if err := policy.CheckAddress(netip.MustParseAddr("93.184.216.34")); err != nil {
		t.Fatalf("public address denied: %v", err)
	}
	allowed := Policy{Production: true, AllowedPrivate: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}
	if err := allowed.CheckAddress(netip.MustParseAddr("10.20.1.2")); err != nil {
		t.Fatalf("operator-allowed private address denied: %v", err)
	}
	broadlyAllowed := Policy{Production: true, AllowedPrivate: []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}}
	for _, raw := range []string{"169.254.169.254", "100.100.100.200", "fd00:ec2::254",
		"127.0.0.1", "169.254.1.1", "0.0.0.0", "224.0.0.1", "::1", "fe80::1", "::", "ff02::1"} {
		if err := broadlyAllowed.CheckAddress(netip.MustParseAddr(raw)); !errors.Is(err, ErrAddressDenied) {
			t.Fatalf("unsafe address %s was allowlisted: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://example.test/hook", "https://user:pass@example.test/hook",
		"https://example.test/hook?access_token=secret", "https://example.test/hook?",
		"https://example.test/hook#fragment", " https://example.test/hook", "https://example.test/hook ",
		"https://example.test\\@private.test/hook", "https://example.test:https/hook", "https://example.test:70000/hook"} {
		_, err := policy.ValidateURL(raw)
		if !errors.Is(err, ErrInvalidDestination) {
			t.Fatalf("production URL %q error = %v", raw, err)
		}
	}
}

func TestPolicyAllowAnyPrivateBypassesCIDRAllowlistButKeepsSpecialDenials(t *testing.T) {
	policy := Policy{Production: true, AllowAnyPrivate: true}
	for _, raw := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.1", "fc00::1"} {
		if err := policy.CheckAddress(netip.MustParseAddr(raw)); err != nil {
			t.Fatalf("private address %s denied without allowlist: %v", raw, err)
		}
	}
	for _, raw := range []string{"127.0.0.1", "169.254.1.1", "169.254.169.254",
		"100.100.100.200", "fd00:ec2::254", "0.0.0.0", "224.0.0.1", "::1", "fe80::1", "ff02::1"} {
		if err := policy.CheckAddress(netip.MustParseAddr(raw)); !errors.Is(err, ErrAddressDenied) {
			t.Fatalf("unsafe address %s allowed under AllowAnyPrivate: %v", raw, err)
		}
	}
	if err := policy.CheckAddress(netip.MustParseAddr("93.184.216.34")); err != nil {
		t.Fatalf("public address denied: %v", err)
	}
}

func TestPolicyAllowsHTTPForTrustedInternalProduction(t *testing.T) {
	if _, err := (Policy{Production: true, AllowHTTP: true}).ValidateURL("http://runner.intra.example/api/v1/runner/webhooks"); err != nil {
		t.Fatalf("trusted internal HTTP receiver rejected: %v", err)
	}
	if _, err := (Policy{Production: true}).ValidateURL("http://runner.intra.example/api/v1/runner/webhooks"); !errors.Is(err, ErrInvalidDestination) {
		t.Fatalf("default production policy error = %v, want ErrInvalidDestination", err)
	}
}

func TestTrustedInternalHTTPRequiresExplicitDestinationCIDR(t *testing.T) {
	resolver := staticResolver{addresses: map[string][]net.IPAddr{
		"runner.intra.example": {{IP: net.ParseIP("10.20.1.2")}},
		"public.example":       {{IP: net.ParseIP("93.184.216.34")}},
	}}
	policy := Policy{Production: true, AllowHTTP: true, AllowedPrivate: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}
	preflight := Preflight{Policy: policy, Resolver: resolver}
	if err := preflight.Validate(t.Context(), "http://runner.intra.example/hook"); err != nil {
		t.Fatalf("allowlisted internal HTTP destination rejected: %v", err)
	}
	if err := preflight.Validate(t.Context(), "http://public.example/hook"); !errors.Is(err, ErrAddressDenied) {
		t.Fatalf("public HTTP destination error = %v, want ErrAddressDenied", err)
	}
	if err := preflight.Validate(t.Context(), "https://public.example/hook"); err != nil {
		t.Fatalf("public HTTPS destination rejected: %v", err)
	}
}

func TestTrustedInternalHTTPAllowsExplicitNonPrivateCIDR(t *testing.T) {
	address := netip.MustParseAddr("100.64.1.2")
	if address.IsPrivate() {
		t.Fatalf("regression address %s unexpectedly classified as private", address)
	}
	resolver := staticResolver{addresses: map[string][]net.IPAddr{
		"runner.intra.example": {{IP: net.IP(address.AsSlice())}},
		"mixed.intra.example": {
			{IP: net.IP(address.AsSlice())},
			{IP: net.ParseIP("93.184.216.34")},
		},
	}}
	policy := Policy{Production: true, AllowHTTP: true, AllowedPrivate: []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
	}}
	preflight := Preflight{Policy: policy, Resolver: resolver}
	for _, raw := range []string{
		"http://" + address.String() + "/hook",
		"http://runner.intra.example/hook",
	} {
		if err := preflight.Validate(t.Context(), raw); err != nil {
			t.Errorf("allowlisted non-private HTTP destination %q rejected: %v", raw, err)
		}
	}
	if err := preflight.Validate(t.Context(), "http://mixed.intra.example/hook"); !errors.Is(err, ErrAddressDenied) {
		t.Errorf("mixed allowlisted and non-allowlisted DNS answer error = %v, want ErrAddressDenied", err)
	}
}

func TestTrustedInternalHTTPConnectTimeHonorsCIDRAndPreventsRebinding(t *testing.T) {
	allowed := netip.MustParseAddr("100.64.1.2")
	policy := Policy{Production: true, AllowHTTP: true, AllowedPrivate: []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
	}}
	resolver := staticResolver{addresses: map[string][]net.IPAddr{
		"runner.intra.example": {{IP: net.IP(allowed.AsSlice())}},
	}}
	dial := secureDialContext(resolver, fakeDialer{remote: allowed}, policy, "http")
	connection, err := dial(t.Context(), "tcp", "runner.intra.example:80")
	if err != nil {
		t.Fatalf("connect-time check rejected allowlisted non-private address: %v", err)
	}
	_ = connection.Close()

	rebound := secureDialContext(resolver, fakeDialer{remote: netip.MustParseAddr("100.64.1.3")}, policy, "http")
	if _, err := rebound(t.Context(), "tcp", "runner.intra.example:80"); !errors.Is(err, ErrAddressDenied) {
		t.Fatalf("connect-time rebinding error = %v, want ErrAddressDenied", err)
	}
}

func TestResolutionAndConnectTimeAddressChecksPreventRebinding(t *testing.T) {
	resolver := staticResolver{addresses: map[string][]net.IPAddr{
		"mixed.example":  {{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("10.0.0.2")}},
		"public.example": {{IP: net.ParseIP("93.184.216.34")}},
	}}
	policy := Policy{Production: true}
	if _, err := resolveAllowed(t.Context(), resolver, policy, "https", "mixed.example"); !errors.Is(err, ErrAddressDenied) {
		t.Fatalf("mixed DNS answer error = %v", err)
	}
	dial := secureDialContext(resolver, fakeDialer{remote: netip.MustParseAddr("10.0.0.9")}, policy, "https")
	if _, err := dial(t.Context(), "tcp", "public.example:443"); !errors.Is(err, ErrAddressDenied) {
		t.Fatalf("connect-time rebinding error = %v", err)
	}
}

func TestClientRejectsPublicHTTPInTrustedInternalProduction(t *testing.T) {
	public := netip.MustParseAddr("93.184.216.34")
	client, err := NewClient(Config{
		Policy:   Policy{Production: true, AllowHTTP: true},
		Resolver: staticResolver{addresses: map[string][]net.IPAddr{"public.example": {{IP: net.IP(public.AsSlice())}}}},
		Dialer:   fakeDialer{remote: public},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Send(t.Context(), Request{URL: "http://public.example/hook", Secret: []byte("secret"),
		EventID: "event", DeliveryID: "delivery", Timestamp: time.Now(), Body: []byte(`{}`)})
	if !errors.Is(err, ErrAddressDenied) && (err == nil || !strings.Contains(err.Error(), ErrAddressDenied.Error())) {
		t.Fatalf("public HTTP delivery error = %v, want ErrAddressDenied", err)
	}
}

func TestClientDisablesRedirectsBoundsResponsesAndVerifiesTLS(t *testing.T) {
	public := netip.MustParseAddr("93.184.216.34")
	var targetCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			targetCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Header.Get("Authorization") != "Bearer top-secret" {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer server.Close()
	_, serverPort, _ := net.SplitHostPort(server.Listener.Addr().String())
	client, err := NewClient(Config{Policy: Policy{}, MaxResponseBodyBytes: 32,
		Resolver: staticResolver{addresses: map[string][]net.IPAddr{"webhook.example": {{IP: net.IP(public.AsSlice())}}}},
		Dialer:   forwardingDialer{target: server.Listener.Addr().String(), reported: public}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Send(t.Context(), Request{URL: "http://webhook.example:" + serverPort, Secret: []byte("top-secret"),
		EventID: "event", DeliveryID: "delivery", Timestamp: time.Now(), Body: []byte(`{}`)})
	if err != nil || result.StatusCode != http.StatusFound || targetCalls.Load() != 0 {
		t.Fatalf("redirect result=%+v target=%d err=%v", result, targetCalls.Load(), err)
	}

	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 64))
	}))
	defer large.Close()
	_, largePort, _ := net.SplitHostPort(large.Listener.Addr().String())
	largeClient, _ := NewClient(Config{Policy: Policy{}, MaxResponseBodyBytes: 32,
		Resolver: staticResolver{addresses: map[string][]net.IPAddr{"large.example": {{IP: net.IP(public.AsSlice())}}}},
		Dialer:   forwardingDialer{target: large.Listener.Addr().String(), reported: public}})
	if _, err := largeClient.Send(t.Context(), Request{URL: "http://large.example:" + largePort, Secret: []byte("top-secret"),
		EventID: "event", DeliveryID: "delivery", Timestamp: time.Now(), Body: []byte(`{}`)}); err == nil {
		t.Fatal("oversized response body was accepted")
	}
	headerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Large", strings.Repeat("h", 1024))
		w.WriteHeader(http.StatusOK)
	}))
	defer headerServer.Close()
	_, headerPort, _ := net.SplitHostPort(headerServer.Listener.Addr().String())
	headerClient, _ := NewClient(Config{Policy: Policy{}, MaxResponseHeaderBytes: 128,
		Resolver: staticResolver{addresses: map[string][]net.IPAddr{"header.example": {{IP: net.IP(public.AsSlice())}}}},
		Dialer:   forwardingDialer{target: headerServer.Listener.Addr().String(), reported: public}})
	if _, err := headerClient.Send(t.Context(), Request{URL: "http://header.example:" + headerPort, Secret: []byte("top-secret"),
		EventID: "event", DeliveryID: "delivery", Timestamp: time.Now(), Body: []byte(`{}`)}); err == nil {
		t.Fatal("oversized response headers were accepted")
	}
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowServer.Close()
	_, slowPort, _ := net.SplitHostPort(slowServer.Listener.Addr().String())
	timeoutClient, _ := NewClient(Config{Policy: Policy{}, RequestTimeout: 20 * time.Millisecond,
		Resolver: staticResolver{addresses: map[string][]net.IPAddr{"slow.example": {{IP: net.IP(public.AsSlice())}}}},
		Dialer:   forwardingDialer{target: slowServer.Listener.Addr().String(), reported: public}})
	if _, err := timeoutClient.Send(t.Context(), Request{URL: "http://slow.example:" + slowPort, Secret: []byte("top-secret"),
		EventID: "event", DeliveryID: "delivery", Timestamp: time.Now(), Body: []byte(`{}`)}); err == nil {
		t.Fatal("request timeout was not enforced")
	}

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer tlsServer.Close()
	_, tlsPort, _ := net.SplitHostPort(strings.TrimPrefix(tlsServer.URL, "https://"))
	tlsConfig := tlsServer.Client().Transport.(*http.Transport).TLSClientConfig
	tlsClient, err := NewClient(Config{Policy: Policy{Production: true}, TLSConfig: tlsConfig,
		Resolver: staticResolver{addresses: map[string][]net.IPAddr{"example.com": {{IP: net.IP(public.AsSlice())}},
			"wrong.example": {{IP: net.IP(public.AsSlice())}}}},
		Dialer: forwardingDialer{target: tlsServer.Listener.Addr().String(), reported: public}})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := tlsClient.Send(t.Context(), Request{URL: "https://example.com:" + tlsPort, Secret: []byte("secret"),
		EventID: "event", DeliveryID: "delivery", Timestamp: time.Now(), Body: []byte(`{}`)}); err != nil || result.StatusCode != http.StatusOK {
		t.Fatalf("TLS result=%+v err=%v", result, err)
	}
	if _, err := tlsClient.Send(t.Context(), Request{URL: "https://wrong.example:" + tlsPort, Secret: []byte("secret"),
		EventID: "event", DeliveryID: "delivery", Timestamp: time.Now(), Body: []byte(`{}`)}); err == nil {
		t.Fatal("TLS hostname mismatch was accepted")
	}
}

func TestClientGitHubHeadersQueryAndBearerIsolation(t *testing.T) {
	public := netip.MustParseAddr("93.184.216.34")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "access_token=opaque%2Bvalue&mode=sync" {
			t.Errorf("raw query=%q", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("authorization=%q", got)
		}
		if r.Header.Get("X-GitHub-Event") != "issues" || r.Header.Get("X-GitHub-Delivery") != "stable-delivery" ||
			r.Header.Get("X-Hub-Signature-256") != "sha256=abc" || !strings.HasPrefix(r.Header.Get("User-Agent"), "GitHub-Hookshot/") {
			t.Errorf("github headers=%v", r.Header)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	client, err := NewClient(Config{Policy: Policy{},
		Resolver: staticResolver{addresses: map[string][]net.IPAddr{"robot.example": {{IP: net.IP(public.AsSlice())}}}},
		Dialer:   forwardingDialer{target: server.Listener.Addr().String(), reported: public}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Send(t.Context(), Request{URL: "http://robot.example:" + port + "/hook",
		EventID: "event", DeliveryID: "stable-delivery", Timestamp: time.Now(), Body: []byte(`{"action":"opened"}`),
		DeliveryFormat: "github.v3", EventName: "issues", Signature: "sha256=abc",
		DestinationQuery: []byte("access_token=opaque%2Bvalue&mode=sync")})
	if err != nil || result.StatusCode != http.StatusNoContent {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type staticResolver struct{ addresses map[string][]net.IPAddr }

func (r staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return r.addresses[host], nil
}

type fakeDialer struct{ remote netip.Addr }

func (d fakeDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return &fakeConn{remote: &net.TCPAddr{IP: net.IP(d.remote.AsSlice()), Port: 443}}, nil
}

type forwardingDialer struct {
	target   string
	reported netip.Addr
}

func (d forwardingDialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, network, d.target)
	if err != nil {
		return nil, err
	}
	return &reportedConn{Conn: connection,
		remote: &net.TCPAddr{IP: net.IP(d.reported.AsSlice()), Port: 443}}, nil
}

type reportedConn struct {
	net.Conn
	remote net.Addr
}

func (c *reportedConn) RemoteAddr() net.Addr { return c.remote }

type fakeConn struct{ remote net.Addr }

func (*fakeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (*fakeConn) Write(value []byte) (int, error)  { return len(value), nil }
func (*fakeConn) Close() error                     { return nil }
func (*fakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr           { return c.remote }
func (*fakeConn) SetDeadline(time.Time) error      { return nil }
func (*fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (*fakeConn) SetWriteDeadline(time.Time) error { return nil }
