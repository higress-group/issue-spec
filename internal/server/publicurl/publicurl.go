// Package publicurl validates configured public origins and builds absolute
// URLs without consulting request Host or forwarding headers.
package publicurl

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Origin is a validated canonical http(s) origin. It never contains userinfo,
// a path, query, or fragment.
type Origin struct {
	value url.URL
}

// Origins contains the two independently configured URL realms emitted by the
// compatibility and browser surfaces.
type Origins struct {
	API            Origin
	Web            Origin
	TrustedProxies ProxyPolicy
}

// ProxyPolicy determines whether transport metadata came from a configured
// reverse proxy. It never changes API or web origins; those remain authoritative.
type ProxyPolicy struct {
	prefixes []netip.Prefix
}

// New validates the configured origins and trusted proxy prefixes.
func New(api, web string, trusted []netip.Prefix) (Origins, error) {
	apiOrigin, err := ParseOrigin("API public origin", api)
	if err != nil {
		return Origins{}, err
	}
	webOrigin, err := ParseOrigin("web public origin", web)
	if err != nil {
		return Origins{}, err
	}
	policy, err := NewProxyPolicy(trusted)
	if err != nil {
		return Origins{}, err
	}
	return Origins{API: apiOrigin, Web: webOrigin, TrustedProxies: policy}, nil
}

// ParseOrigin parses one required canonical origin.
func ParseOrigin(name, raw string) (Origin, error) {
	if strings.TrimSpace(raw) != raw || raw == "" {
		return Origin{}, fmt.Errorf("%s is required and must not contain surrounding whitespace", name)
	}
	if strings.ContainsAny(raw, "\\\r\n\t") {
		return Origin{}, fmt.Errorf("%s contains forbidden characters", name)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || !u.IsAbs() || u.Host == "" {
		return Origin{}, fmt.Errorf("%s must be an absolute http(s) origin", name)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return Origin{}, fmt.Errorf("%s must use http or https", name)
	}
	if u.User != nil {
		return Origin{}, fmt.Errorf("%s must not contain userinfo", name)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return Origin{}, fmt.Errorf("%s must not contain a query", name)
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return Origin{}, fmt.Errorf("%s must not contain a fragment", name)
	}
	if u.Path != "" && u.Path != "/" {
		return Origin{}, fmt.Errorf("%s must not contain a path", name)
	}
	if u.Hostname() == "" {
		return Origin{}, fmt.Errorf("%s must contain a hostname", name)
	}
	port := u.Port()
	if port != "" {
		numericPort, err := strconv.Atoi(port)
		if err != nil || numericPort < 1 || numericPort > 65535 {
			return Origin{}, fmt.Errorf("%s contains an invalid port", name)
		}
	}
	u.Host = canonicalHost(u)
	u.Path = ""
	u.RawPath = ""
	return Origin{value: *u}, nil
}

func canonicalHost(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		return net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	return host
}

// String returns the canonical origin without a trailing slash.
func (o Origin) String() string { return o.value.String() }

// URL constructs an absolute URL from the configured origin and a root-relative
// path. Query values are encoded canonically by net/url.
func (o Origin) URL(path string, query url.Values) (string, error) {
	if o.value.Host == "" {
		return "", errors.New("public origin is not configured")
	}
	if strings.ContainsAny(path, "\\\r\n\t") {
		return "", errors.New("public URL path contains forbidden characters")
	}
	ref, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || ref.IsAbs() || ref.Host != "" || ref.User != nil {
		return "", errors.New("public URL path must be root-relative")
	}
	if ref.RawQuery != "" || ref.ForceQuery || ref.Fragment != "" {
		return "", errors.New("public URL path must not contain query or fragment")
	}
	result := o.value
	result.Path = ref.Path
	result.RawPath = ref.RawPath
	if query != nil {
		result.RawQuery = query.Encode()
	}
	return result.String(), nil
}

// MustURL is intended for constant server-owned paths.
func (o Origin) MustURL(path string) string {
	value, err := o.URL(path, nil)
	if err != nil {
		panic(err)
	}
	return value
}

// URLWithFragment adds a separately supplied fragment to a canonical URL.
// Keeping it separate from path prevents callers from smuggling query or host
// syntax into the path argument.
func (o Origin) URLWithFragment(path, fragment string) (string, error) {
	value, err := o.URL(path, nil)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(fragment, "\r\n") {
		return "", errors.New("public URL fragment contains forbidden characters")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	parsed.Fragment = fragment
	return parsed.String(), nil
}

// MustURLWithFragment is intended for server-owned fragment values.
func (o Origin) MustURLWithFragment(path, fragment string) string {
	value, err := o.URLWithFragment(path, fragment)
	if err != nil {
		panic(err)
	}
	return value
}

// ValidateSameOriginURL accepts only an absolute URL in the configured origin.
// It is suitable for redirects and RFC5988 cursor URLs before credentials are
// forwarded.
func (o Origin) ValidateSameOriginURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.ContainsAny(raw, "\\\r\n\t") {
		return nil, errors.New("cursor URL is empty or malformed")
	}
	target, err := url.Parse(raw)
	if err != nil || !target.IsAbs() || target.Host == "" || target.Opaque != "" {
		return nil, errors.New("cursor URL must be absolute")
	}
	if target.User != nil || target.Fragment != "" || target.RawFragment != "" {
		return nil, errors.New("cursor URL must not contain userinfo or fragment")
	}
	if !sameOrigin(o.value, *target) {
		return nil, fmt.Errorf("cursor URL origin %q does not match configured origin %q", originString(*target), o.String())
	}
	target.Scheme = o.value.Scheme
	target.Host = o.value.Host
	return target, nil
}

// SameOrigin reports whether both absolute URLs have the same normalized
// scheme, hostname, and effective port.
func SameOrigin(left, right string) bool {
	l, err := url.Parse(left)
	if err != nil || !l.IsAbs() || l.User != nil {
		return false
	}
	r, err := url.Parse(right)
	if err != nil || !r.IsAbs() || r.User != nil {
		return false
	}
	return sameOrigin(*l, *r)
}

func sameOrigin(left, right url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectivePort(left) == effectivePort(right)
}

func effectivePort(u url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func originString(u url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + canonicalHost(&u)
}

// NewProxyPolicy validates and canonicalizes trusted proxy prefixes.
func NewProxyPolicy(prefixes []netip.Prefix) (ProxyPolicy, error) {
	canonical := make([]netip.Prefix, 0, len(prefixes))
	seen := make(map[netip.Prefix]struct{}, len(prefixes))
	for _, prefix := range prefixes {
		if !prefix.IsValid() {
			return ProxyPolicy{}, errors.New("trusted proxy prefix is invalid")
		}
		prefix = prefix.Masked()
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		canonical = append(canonical, prefix)
	}
	slices.SortFunc(canonical, func(a, b netip.Prefix) int { return strings.Compare(a.String(), b.String()) })
	return ProxyPolicy{prefixes: canonical}, nil
}

// IsTrustedRemote reports whether remoteAddr belongs to a configured proxy.
// It accepts both host:port and a bare IP address.
func (p ProxyPolicy) IsTrustedRemote(remoteAddr string) bool {
	host := remoteAddr
	if parsedHost, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = parsedHost
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range p.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// RequestCameThroughTrustedProxy is intentionally the only request-derived
// public URL signal exposed here. Callers may use it for audit metadata, but
// must still use Origins.API/Web for emitted links.
func (p ProxyPolicy) RequestCameThroughTrustedProxy(r *http.Request) bool {
	return r != nil && p.IsTrustedRemote(r.RemoteAddr)
}

// Prefixes returns a copy for redacted diagnostics.
func (p ProxyPolicy) Prefixes() []netip.Prefix { return slices.Clone(p.prefixes) }
