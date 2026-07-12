package auth

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/server/events/networkpolicy"
)

func TestNormalizeExternalAvatarURLRejectsUnsafeAndCanonicalizes(t *testing.T) {
	if got := NormalizeExternalAvatarURL("https://AVATARS.example:443/u.png?v=1"); got != "https://avatars.example/u.png" {
		t.Fatalf("normalized = %q", got)
	}
	for _, raw := range []string{"http://avatars.example/u", "https://user:secret@avatars.example/u", "https://avatars.example/u#x", "javascript:alert(1)"} {
		if got := NormalizeExternalAvatarURL(raw); got != "" {
			t.Fatalf("unsafe %q normalized to %q", raw, got)
		}
	}
}

func TestSecureAvatarDialRejectsPrivateDNSAndConnectRebinding(t *testing.T) {
	policy := networkpolicy.Policy{Production: true}
	private := stubAvatarResolver{addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	dialer := &stubAvatarDialer{remote: netip.MustParseAddr("127.0.0.1")}
	if _, err := secureAvatarDial(private, dialer, policy)(context.Background(), "tcp", "avatars.example:443"); !errors.Is(err, networkpolicy.ErrAddressDenied) {
		t.Fatalf("private DNS error = %v", err)
	}
	public := stubAvatarResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}}
	dialer.remote = netip.MustParseAddr("93.184.216.35")
	if _, err := secureAvatarDial(public, dialer, policy)(context.Background(), "tcp", "avatars.example:443"); !errors.Is(err, networkpolicy.ErrAddressDenied) {
		t.Fatalf("rebind error = %v", err)
	}
}

type stubAvatarResolver struct{ addresses []net.IPAddr }

func (s stubAvatarResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return s.addresses, nil
}

type stubAvatarDialer struct{ remote netip.Addr }

func (s *stubAvatarDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return &stubAvatarConn{remote: &net.TCPAddr{IP: net.IP(s.remote.AsSlice()), Port: 443}}, nil
}

type stubAvatarConn struct{ remote net.Addr }

func (s *stubAvatarConn) Read([]byte) (int, error)         { return 0, errors.New("unused") }
func (s *stubAvatarConn) Write(p []byte) (int, error)      { return len(p), nil }
func (s *stubAvatarConn) Close() error                     { return nil }
func (s *stubAvatarConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (s *stubAvatarConn) RemoteAddr() net.Addr             { return s.remote }
func (s *stubAvatarConn) SetDeadline(time.Time) error      { return nil }
func (s *stubAvatarConn) SetReadDeadline(time.Time) error  { return nil }
func (s *stubAvatarConn) SetWriteDeadline(time.Time) error { return nil }
