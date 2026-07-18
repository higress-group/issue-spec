package emaildelivery

import (
	"errors"
	"net/mail"
	"strings"
)

// AddressPolicy is the immutable operator policy applied to notification
// recipients. An empty suffix list preserves the existing any-domain behavior.
type AddressPolicy struct {
	suffixes []string
}

// NewAddressPolicy normalizes and validates domain suffixes once at startup.
func NewAddressPolicy(suffixes []string) (AddressPolicy, error) {
	normalized := make([]string, 0, len(suffixes))
	seen := make(map[string]struct{}, len(suffixes))
	for _, value := range suffixes {
		suffix := strings.ToLower(strings.TrimSpace(value))
		if !validDomain(suffix) {
			return AddressPolicy{}, errors.New("email delivery: invalid allowed email domain suffix")
		}
		if _, ok := seen[suffix]; ok {
			continue
		}
		seen[suffix] = struct{}{}
		normalized = append(normalized, suffix)
	}
	return AddressPolicy{suffixes: normalized}, nil
}

// Suffixes returns a copy safe for exposing in private profile APIs and UIs.
func (p AddressPolicy) Suffixes() []string {
	result := make([]string, len(p.suffixes))
	copy(result, p.suffixes)
	return result
}

// Allows parses a bare mailbox and performs a label-boundary suffix match.
func (p AddressPolicy) Allows(address string) bool {
	address = strings.TrimSpace(address)
	if address == "" || len(address) > 320 || strings.ContainsAny(address, "\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Name != "" || parsed.Address != address || strings.Count(address, "@") != 1 {
		return false
	}
	// With no operator suffix configured, retain the established bare-mailbox
	// contract exactly. net/mail accepts valid non-DNS domains such as
	// internationalized names and address literals; applying validDomain here
	// would silently tighten otherwise-compatible installations.
	if len(p.suffixes) == 0 {
		return true
	}
	domain := strings.ToLower(address[strings.LastIndexByte(address, '@')+1:])
	if !validDomain(domain) {
		return false
	}
	for _, suffix := range p.suffixes {
		if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
			return true
		}
	}
	return false
}

func validDomain(domain string) bool {
	if domain == "" || len(domain) > 253 || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}
