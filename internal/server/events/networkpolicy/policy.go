// Package networkpolicy provides the only outbound transport allowed for
// webhook delivery. Resolution and connect-time addresses are both checked.
package networkpolicy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

var (
	ErrInvalidDestination = errors.New("webhook network policy: invalid destination")
	ErrAddressDenied      = errors.New("webhook network policy: address denied")
)

type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type Policy struct {
	Production bool
	// AllowHTTP permits HTTP webhook receivers in an explicitly trusted internal
	// deployment. It does not relax address validation: private destinations
	// still require an AllowedPrivate CIDR and loopback, link-local, multicast,
	// and metadata addresses remain denied.
	AllowHTTP      bool
	AllowedPrivate []netip.Prefix
}

type Preflight struct {
	Policy   Policy
	Resolver Resolver
}

func (p Preflight) Validate(ctx context.Context, raw string) error {
	parsed, err := p.Policy.ValidateURL(raw)
	if err != nil {
		return err
	}
	resolver := p.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	_, err = resolveAllowed(ctx, resolver, p.Policy, parsed.Scheme, parsed.Hostname())
	return err
}

func (p Policy) ValidateURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "?#\\") {
		return nil, ErrInvalidDestination
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Opaque != "" {
		return nil, ErrInvalidDestination
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, ErrInvalidDestination
	}
	if parsed.Scheme == "http" && p.Production && !p.AllowHTTP {
		return nil, ErrInvalidDestination
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, ErrInvalidDestination
		}
	}
	return parsed, nil
}

func (p Policy) CheckAddress(address netip.Addr) error {
	address = address.Unmap()
	if !address.IsValid() || isMetadata(address) || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified() ||
		address.IsMulticast() {
		return ErrAddressDenied
	}
	if address.IsPrivate() {
		for _, allowed := range p.AllowedPrivate {
			if allowed.Contains(address) {
				return nil
			}
		}
		return ErrAddressDenied
	}
	if !address.IsGlobalUnicast() {
		return ErrAddressDenied
	}
	return nil
}

func (p Policy) checkAddressForScheme(scheme string, address netip.Addr) error {
	address = address.Unmap()
	if err := p.CheckAddress(address); err != nil {
		return err
	}
	if scheme == "http" && p.Production {
		if !address.IsPrivate() {
			return ErrAddressDenied
		}
		for _, allowed := range p.AllowedPrivate {
			if allowed.Contains(address) {
				return nil
			}
		}
		return ErrAddressDenied
	}
	return nil
}

func isMetadata(address netip.Addr) bool {
	_, denied := metadataAddresses[address]
	return denied
}

var metadataAddresses = map[netip.Addr]struct{}{
	netip.MustParseAddr("169.254.169.254"): {},
	netip.MustParseAddr("100.100.100.200"): {},
	netip.MustParseAddr("fd00:ec2::254"):   {},
}

func resolveAllowed(ctx context.Context, resolver Resolver, policy Policy, scheme, host string) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		if err := policy.checkAddressForScheme(scheme, literal); err != nil {
			return nil, err
		}
		return []netip.Addr{literal.Unmap()}, nil
	}
	resolved, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve destination: %w", err)
	}
	if len(resolved) == 0 {
		return nil, ErrAddressDenied
	}
	result := make([]netip.Addr, 0, len(resolved))
	for _, item := range resolved {
		address, ok := netip.AddrFromSlice(item.IP)
		if !ok {
			return nil, ErrAddressDenied
		}
		address = address.Unmap()
		if err := policy.checkAddressForScheme(scheme, address); err != nil {
			return nil, err
		}
		result = append(result, address)
	}
	return result, nil
}
