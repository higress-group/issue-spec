package auth

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/events/networkpolicy"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const MaxAvatarBytes int64 = 2 << 20

type AvatarConfig struct {
	ProviderOrigins map[uuid.UUID][]string
	Resolver        avatarResolver
	Dialer          avatarDialer
	Timeout         time.Duration
	CacheTTL        time.Duration
}

type avatarResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}
type avatarDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type Avatar struct {
	ContentType string
	Data        []byte
	ETag        string
}
type cachedAvatar struct {
	Avatar
	expires time.Time
}

type AvatarService struct {
	pool     *pgxpool.Pool
	client   *http.Client
	policy   networkpolicy.Policy
	origins  map[uuid.UUID]map[string]struct{}
	cacheTTL time.Duration
	mu       sync.Mutex
	cache    map[string]cachedAvatar
}

func NewAvatarService(pool *pgxpool.Pool, config AvatarConfig) (*AvatarService, error) {
	if pool == nil {
		return nil, errors.New("avatar: database is required")
	}
	if config.Resolver == nil {
		config.Resolver = net.DefaultResolver
	}
	if config.Dialer == nil {
		config.Dialer = &net.Dialer{Timeout: 3 * time.Second}
	}
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}
	if config.CacheTTL <= 0 {
		config.CacheTTL = 5 * time.Minute
	}
	origins := make(map[uuid.UUID]map[string]struct{}, len(config.ProviderOrigins))
	for providerID, values := range config.ProviderOrigins {
		if providerID == uuid.Nil {
			return nil, errors.New("avatar: provider id is required")
		}
		allowed := map[string]struct{}{}
		for _, raw := range values {
			origin, err := publicurl.ParseOrigin("avatar origin", raw)
			if err != nil || !strings.HasPrefix(origin.String(), "https://") {
				return nil, errors.New("avatar: allowed origins must be canonical HTTPS origins")
			}
			allowed[origin.String()] = struct{}{}
		}
		origins[providerID] = allowed
	}
	policy := networkpolicy.Policy{Production: true}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 3 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: 16 << 10}
	transport.DialContext = secureAvatarDial(config.Resolver, config.Dialer, policy)
	client := &http.Client{Transport: transport, Timeout: config.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &AvatarService{pool: pool, client: client, policy: policy, origins: origins,
		cacheTTL: config.CacheTTL, cache: map[string]cachedAvatar{}}, nil
}

func (s *AvatarService) FetchForLogin(ctx context.Context, login string) (Avatar, error) {
	login = strings.TrimSpace(login)
	if s == nil || s.pool == nil || login == "" {
		return Avatar{}, ErrNotFound
	}
	var providerID uuid.UUID
	var raw string
	if err := s.pool.QueryRow(ctx, `SELECT i.provider_id, i.avatar_url
		FROM users u JOIN identities i ON i.user_id=u.id AND i.id=u.profile_identity_id
		WHERE u.login_key=lower($1) AND u.status='active' AND i.avatar_url IS NOT NULL`, login).
		Scan(&providerID, &raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Avatar{}, ErrNotFound
		}
		return Avatar{}, errors.New("avatar: profile lookup failed")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return Avatar{}, ErrNotFound
	}
	parsedOrigin, originErr := publicurl.ParseOrigin("avatar source", parsed.Scheme+"://"+parsed.Host)
	if originErr != nil {
		return Avatar{}, ErrNotFound
	}
	origin := parsedOrigin.String()
	if _, ok := s.origins[providerID][origin]; !ok {
		return Avatar{}, ErrNotFound
	}
	if _, err := s.policy.ValidateURL(raw); err != nil {
		return Avatar{}, ErrNotFound
	}
	s.mu.Lock()
	if cached, ok := s.cache[raw]; ok && time.Now().Before(cached.expires) {
		s.mu.Unlock()
		return cached.Avatar, nil
	}
	s.mu.Unlock()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return Avatar{}, ErrNotFound
	}
	request.Header.Set("Accept", "image/png,image/jpeg,image/gif,image/webp")
	response, err := s.client.Do(request)
	if err != nil {
		return Avatar{}, ErrNotFound
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > MaxAvatarBytes {
		return Avatar{}, ErrNotFound
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return Avatar{}, ErrNotFound
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxAvatarBytes+1))
	if err != nil || int64(len(data)) > MaxAvatarBytes || len(data) == 0 {
		return Avatar{}, ErrNotFound
	}
	detected := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	if !allowedAvatarMedia(detected) || detected != contentType {
		return Avatar{}, ErrNotFound
	}
	digest := sha256.Sum256(data)
	avatar := Avatar{ContentType: contentType, Data: data, ETag: `"sha256-` + hex.EncodeToString(digest[:]) + `"`}
	s.mu.Lock()
	s.cache[raw] = cachedAvatar{Avatar: avatar, expires: time.Now().Add(s.cacheTTL)}
	s.mu.Unlock()
	return avatar, nil
}

func allowedAvatarMedia(value string) bool {
	switch value {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func secureAvatarDial(resolver avatarResolver, dialer avatarDialer, policy networkpolicy.Policy) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, networkpolicy.ErrAddressDenied
		}
		resolved, err := resolver.LookupIPAddr(ctx, host)
		if err != nil || len(resolved) == 0 {
			return nil, networkpolicy.ErrAddressDenied
		}
		for _, item := range resolved {
			approved, ok := netip.AddrFromSlice(item.IP)
			if !ok || policy.CheckAddress(approved) != nil {
				return nil, networkpolicy.ErrAddressDenied
			}
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(approved.String(), port))
			if err != nil {
				continue
			}
			remoteHost, _, splitErr := net.SplitHostPort(conn.RemoteAddr().String())
			remote, parseErr := netip.ParseAddr(strings.Trim(remoteHost, "[]"))
			if splitErr != nil || parseErr != nil || remote.Unmap() != approved.Unmap() || policy.CheckAddress(remote) != nil {
				_ = conn.Close()
				return nil, networkpolicy.ErrAddressDenied
			}
			return conn, nil
		}
		return nil, networkpolicy.ErrAddressDenied
	}
}
