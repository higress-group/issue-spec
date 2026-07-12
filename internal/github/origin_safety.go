package github

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// UnsafeOriginError is returned before a redirect or absolute cursor can send
// credentials to a different origin.
type UnsafeOriginError struct {
	Operation      string
	ExpectedOrigin string
	ActualOrigin   string
}

func (e *UnsafeOriginError) Error() string {
	return fmt.Sprintf("refusing %s to origin %q; selected API origin is %q", e.Operation, e.ActualOrigin, e.ExpectedOrigin)
}

type ClientOptions struct {
	Host       string
	BaseURL    string
	Token      string
	CAFile     string
	HTTPClient *http.Client
}

// NewClientWithOptions validates the API base, installs custom CA trust, and
// rejects cross-origin redirects before the redirected request is sent.
func NewClientWithOptions(options ClientOptions) (*Client, error) {
	base, origin, err := canonicalAPIBase(options.BaseURL)
	if err != nil {
		return nil, err
	}
	baseURL, _ := url.Parse(base)
	httpClient, err := securedHTTPClient(options.HTTPClient, origin, baseURL.Path, options.CAFile)
	if err != nil {
		return nil, err
	}
	return &Client{
		Host: normalizeHost(options.Host), BaseURL: base, Token: options.Token,
		HTTPClient: httpClient, apiOrigin: origin,
	}, nil
}

func canonicalAPIBase(raw string) (string, string, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.ContainsAny(raw, "\\\r\n\t") {
		return "", "", errors.New("API base URL is empty or contains forbidden characters")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Opaque != "" {
		return "", "", errors.New("API base URL must be absolute")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", errors.New("API base URL must use http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", "", errors.New("API base URL must not contain userinfo, query, or fragment")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", "", errors.New("API base URL contains an invalid port")
		}
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	u.Host = host
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	if cleaned := pathpkg.Clean("/" + strings.TrimPrefix(u.Path, "/")); cleaned != u.Path && !(cleaned == "/" && u.Path == "") {
		return "", "", errors.New("API base URL path must be canonical")
	}
	origin := u.Scheme + "://" + u.Host
	return u.String(), origin, nil
}

func securedHTTPClient(source *http.Client, origin, basePath, caFile string) (*http.Client, error) {
	var result http.Client
	if source != nil {
		result = *source
	} else {
		result.Timeout = 30 * time.Second
	}
	if caFile != "" {
		transport, err := transportWithCA(result.Transport, caFile)
		if err != nil {
			return nil, err
		}
		result.Transport = transport
	}
	priorCheck := result.CheckRedirect
	result.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		actual := requestOrigin(req.URL)
		if actual != origin {
			return &UnsafeOriginError{Operation: "cross-origin redirect", ExpectedOrigin: origin, ActualOrigin: actual}
		}
		if !withinAPIBasePath(basePath, req.URL.Path) {
			return &UnsafeOriginError{Operation: "redirect outside configured API base path", ExpectedOrigin: origin + basePath, ActualOrigin: actual + req.URL.Path}
		}
		if priorCheck != nil {
			return priorCheck(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &result, nil
}

func transportWithCA(source http.RoundTripper, caFile string) (*http.Transport, error) {
	if !filepath.IsAbs(caFile) {
		return nil, errors.New("custom CA file must be an absolute path")
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read custom CA file: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("custom CA file does not contain a valid PEM certificate")
	}
	var transport *http.Transport
	switch current := source.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = current.Clone()
	default:
		return nil, errors.New("custom CA requires an *http.Transport")
	}
	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		tlsConfig = tlsConfig.Clone()
		if tlsConfig.MinVersion == 0 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	tlsConfig.RootCAs = roots
	transport.TLSClientConfig = tlsConfig
	return transport, nil
}

func resolveEndpoint(base, expectedOrigin, raw string) (string, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.ContainsAny(raw, "\\\r\n\t") {
		return "", errors.New("request path or cursor is empty or malformed")
	}
	target, err := url.Parse(raw)
	if err != nil || target.Opaque != "" || target.User != nil || target.Fragment != "" {
		return "", errors.New("request path or cursor is malformed")
	}
	if target.IsAbs() || target.Host != "" {
		actual := requestOrigin(target)
		if !target.IsAbs() || actual != expectedOrigin {
			return "", &UnsafeOriginError{Operation: "absolute cursor", ExpectedOrigin: expectedOrigin, ActualOrigin: actual}
		}
		baseURL, _ := url.Parse(base)
		if !withinAPIBasePath(baseURL.Path, target.Path) {
			return "", &UnsafeOriginError{Operation: "absolute cursor outside configured API base path", ExpectedOrigin: expectedOrigin + baseURL.Path, ActualOrigin: actual + target.Path}
		}
		return target.String(), nil
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "", errors.New("request path must be root-relative or an absolute same-origin cursor")
	}
	return strings.TrimRight(base, "/") + raw, nil
}

func withinAPIBasePath(basePath, targetPath string) bool {
	basePath = strings.TrimRight(pathpkg.Clean("/"+strings.TrimPrefix(basePath, "/")), "/")
	if basePath == "" || basePath == "." {
		basePath = "/"
	}
	targetPath = pathpkg.Clean("/" + strings.TrimPrefix(targetPath, "/"))
	if basePath == "/" {
		return true
	}
	return targetPath == basePath || strings.HasPrefix(targetPath, basePath+"/")
}

func requestOrigin(u *url.URL) string {
	if u == nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	formattedHost := host
	if strings.Contains(host, ":") {
		formattedHost = "[" + host + "]"
	}
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		return scheme + "://" + formattedHost
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}
