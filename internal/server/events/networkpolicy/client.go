package networkpolicy

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Policy                 Policy
	Resolver               Resolver
	Dialer                 Dialer
	RequestTimeout         time.Duration
	DialTimeout            time.Duration
	TLSHandshakeTimeout    time.Duration
	ResponseHeaderTimeout  time.Duration
	MaxResponseHeaderBytes int64
	MaxResponseBodyBytes   int64
	TLSConfig              *tls.Config
}

type Client struct {
	client          *http.Client
	policy          Policy
	maxResponseBody int64
}

type Request struct {
	URL              string
	Secret           []byte
	EventID          string
	DeliveryID       string
	Timestamp        time.Time
	Body             []byte
	DeliveryFormat   string
	EventName        string
	Action           string
	Signature        string
	DestinationQuery []byte
}

type Result struct {
	StatusCode int
	Header     http.Header
	BodyBytes  int64
}

func NewClient(config Config) (*Client, error) {
	if config.Resolver == nil {
		config.Resolver = net.DefaultResolver
	}
	if config.Dialer == nil {
		config.Dialer = &net.Dialer{Timeout: defaultDuration(config.DialTimeout, 5*time.Second), KeepAlive: 30 * time.Second}
	}
	config.RequestTimeout = defaultDuration(config.RequestTimeout, 15*time.Second)
	config.TLSHandshakeTimeout = defaultDuration(config.TLSHandshakeTimeout, 5*time.Second)
	config.ResponseHeaderTimeout = defaultDuration(config.ResponseHeaderTimeout, 10*time.Second)
	if config.MaxResponseHeaderBytes <= 0 {
		config.MaxResponseHeaderBytes = 32 << 10
	}
	if config.MaxResponseBodyBytes <= 0 {
		config.MaxResponseBodyBytes = 64 << 10
	}
	tlsConfig := config.TLSConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	if tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	transport := &http.Transport{
		Proxy: nil, ForceAttemptHTTP2: true, DisableCompression: true,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   config.TLSHandshakeTimeout,
		ResponseHeaderTimeout: config.ResponseHeaderTimeout,
		IdleConnTimeout:       30 * time.Second, MaxIdleConns: 32, MaxIdleConnsPerHost: 4,
		MaxResponseHeaderBytes: config.MaxResponseHeaderBytes,
	}
	transport.DialContext = secureDialContext(config.Resolver, config.Dialer, config.Policy)
	return &Client{client: &http.Client{Transport: transport, Timeout: config.RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		policy: config.Policy, maxResponseBody: config.MaxResponseBodyBytes}, nil
}

func (c *Client) Send(ctx context.Context, input Request) (Result, error) {
	format := input.DeliveryFormat
	if format == "" {
		format = "issue-spec.v1"
	}
	if c == nil || c.client == nil || input.EventID == "" ||
		input.DeliveryID == "" || input.Timestamp.IsZero() {
		return Result{}, ErrInvalidDestination
	}
	if (format == "issue-spec.v1" && len(input.Secret) == 0) ||
		(format == "github.v3" && input.EventName == "") ||
		(format != "issue-spec.v1" && format != "github.v3") {
		return Result{}, ErrInvalidDestination
	}
	parsed, err := c.policy.ValidateURL(input.URL)
	if err != nil {
		return Result{}, err
	}
	parsed.RawQuery = string(input.DestinationQuery)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(input.Body))
	if err != nil {
		return Result{}, ErrInvalidDestination
	}
	request.Header.Set("Content-Type", "application/json")
	if format == "github.v3" {
		request.Header.Set("User-Agent", "GitHub-Hookshot/issue-spec")
		request.Header.Set("X-GitHub-Event", input.EventName)
		request.Header.Set("X-GitHub-Delivery", input.DeliveryID)
		if input.Signature != "" {
			request.Header.Set("X-Hub-Signature-256", input.Signature)
		}
	} else {
		request.Header.Set("Authorization", "Bearer "+string(input.Secret))
		request.Header.Set("User-Agent", "issue-spec-webhook/1")
		request.Header.Set("X-Issue-Spec-Event", input.EventID)
		request.Header.Set("X-Issue-Spec-Delivery", input.DeliveryID)
		request.Header.Set("X-Issue-Spec-Timestamp", strconv.FormatInt(input.Timestamp.Unix(), 10))
	}
	response, err := c.client.Do(request)
	if err != nil {
		return Result{}, redactError(err, input.Secret, input.DestinationQuery)
	}
	defer response.Body.Close()
	read, err := io.Copy(io.Discard, io.LimitReader(response.Body, c.maxResponseBody+1))
	if err != nil {
		return Result{}, redactError(err, input.Secret, input.DestinationQuery)
	}
	if read > c.maxResponseBody {
		return Result{}, errors.New("webhook response body exceeds configured limit")
	}
	return Result{StatusCode: response.StatusCode, Header: response.Header.Clone(), BodyBytes: read}, nil
}

func secureDialContext(resolver Resolver, dialer Dialer, policy Policy) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrInvalidDestination
		}
		addresses, err := resolveAllowed(ctx, resolver, policy, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, approved := range addresses {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(approved.String(), port))
			if err != nil {
				lastErr = err
				continue
			}
			remote, ok := remoteIP(connection.RemoteAddr())
			if !ok || remote.Unmap() != approved.Unmap() || policy.CheckAddress(remote) != nil {
				_ = connection.Close()
				return nil, ErrAddressDenied
			}
			return connection, nil
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, ErrAddressDenied
	}
}

func remoteIP(address net.Addr) (netip.Addr, bool) {
	switch value := address.(type) {
	case *net.TCPAddr:
		result, ok := netip.AddrFromSlice(value.IP)
		return result, ok
	default:
		host, _, err := net.SplitHostPort(address.String())
		if err != nil {
			return netip.Addr{}, false
		}
		result, err := netip.ParseAddr(strings.Trim(host, "[]"))
		return result, err == nil
	}
}

func redactError(err error, sensitive ...[]byte) error {
	message := err.Error()
	for _, secret := range sensitive {
		if len(secret) > 0 {
			message = strings.ReplaceAll(message, string(secret), "[REDACTED]")
		}
	}
	if len(message) > 512 {
		message = message[:512]
	}
	return fmt.Errorf("webhook request failed: %s", message)
}

func defaultDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}
