package githuboauth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

const (
	DefaultUserURL       = "https://api.github.com/user"
	DefaultMembershipURL = "https://api.github.com/user/memberships/orgs"
	maxApprovedOrgs      = 16
	maxMembershipBody    = 1 << 20
	membershipTimeout    = 8 * time.Second
)

type AdmissionMode string

const (
	AdmissionUnrestricted           AdmissionMode = "unrestricted"
	AdmissionOrganizationRestricted AdmissionMode = "organization-restricted"
)

type ApprovedOrganization struct {
	Login string `json:"login"`
	ID    string `json:"id,omitempty"`

	stableID int64
}

type AdmissionConfig struct {
	Mode          AdmissionMode          `json:"mode"`
	Organizations []ApprovedOrganization `json:"organizations,omitempty"`
	MembershipURL string                 `json:"membership_url,omitempty"`
}

func NormalizeAdmission(raw *AdmissionConfig, production bool, issuer, userURL string) (AdmissionConfig, error) {
	if raw == nil {
		if production {
			return AdmissionConfig{}, errors.New("githuboauth: production admission policy must be explicit")
		}
		return AdmissionConfig{Mode: AdmissionUnrestricted}, nil
	}
	result := *raw
	result.Mode = AdmissionMode(strings.ToLower(strings.TrimSpace(string(result.Mode))))
	if result.Mode != AdmissionUnrestricted && result.Mode != AdmissionOrganizationRestricted {
		return AdmissionConfig{}, errors.New("githuboauth: admission mode must be unrestricted or organization-restricted")
	}
	if result.Mode == AdmissionUnrestricted {
		if len(result.Organizations) != 0 || strings.TrimSpace(result.MembershipURL) != "" {
			return AdmissionConfig{}, errors.New("githuboauth: unrestricted admission must not configure organizations or membership_url")
		}
		return AdmissionConfig{Mode: AdmissionUnrestricted}, nil
	}
	if len(result.Organizations) == 0 || len(result.Organizations) > maxApprovedOrgs {
		return AdmissionConfig{}, fmt.Errorf("githuboauth: organization-restricted admission requires between 1 and %d organizations", maxApprovedOrgs)
	}
	seenLogins := make(map[string]struct{}, len(result.Organizations))
	seenIDs := make(map[int64]struct{}, len(result.Organizations))
	for index := range result.Organizations {
		organization := &result.Organizations[index]
		organization.Login = strings.ToLower(strings.TrimSpace(organization.Login))
		if !validOrganizationLogin(organization.Login) {
			return AdmissionConfig{}, errors.New("githuboauth: admission organization login is invalid")
		}
		if _, exists := seenLogins[organization.Login]; exists {
			return AdmissionConfig{}, errors.New("githuboauth: admission organization logins must be unique")
		}
		seenLogins[organization.Login] = struct{}{}
		organization.ID = strings.TrimSpace(organization.ID)
		if organization.ID != "" {
			value, err := strconv.ParseInt(organization.ID, 10, 64)
			if err != nil || value <= 0 || strconv.FormatInt(value, 10) != organization.ID {
				return AdmissionConfig{}, errors.New("githuboauth: admission organization id must be a canonical positive decimal string")
			}
			if _, exists := seenIDs[value]; exists {
				return AdmissionConfig{}, errors.New("githuboauth: admission organization ids must be unique")
			}
			seenIDs[value] = struct{}{}
			organization.stableID = value
		}
	}
	sort.Slice(result.Organizations, func(i, j int) bool { return result.Organizations[i].Login < result.Organizations[j].Login })
	userURL = strings.TrimSpace(userURL)
	if userURL == "" {
		userURL = DefaultUserURL
	}
	membershipURL := strings.TrimRight(strings.TrimSpace(result.MembershipURL), "/")
	if membershipURL == "" {
		if strings.TrimRight(strings.TrimSpace(issuer), "/") != "https://github.com" && production {
			return AdmissionConfig{}, errors.New("githuboauth: GitHub Enterprise restricted admission requires membership_url")
		}
		membershipURL = derivedMembershipURL(userURL)
	}
	normalizedMembership, err := validateMembershipURL(membershipURL, userURL, production)
	if err != nil {
		return AdmissionConfig{}, err
	}
	result.MembershipURL = normalizedMembership
	return result, nil
}

func AdmissionScopes(scopes []string, mode AdmissionMode) []string {
	result := append([]string(nil), scopes...)
	if len(result) == 0 {
		result = []string{"read:user", "user:email"}
	}
	if mode == AdmissionOrganizationRestricted {
		result = append(result, "read:org")
	}
	seen := make(map[string]struct{}, len(result))
	compact := result[:0]
	for _, scope := range result {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		compact = append(compact, scope)
	}
	return compact
}

type AdmissionFailureClass string

const (
	AdmissionNoActiveMembership           AdmissionFailureClass = "github_admission_no_active_membership"
	AdmissionPending                      AdmissionFailureClass = "github_admission_pending"
	AdmissionOrganizationIdentityMismatch AdmissionFailureClass = "github_admission_organization_identity_mismatch"
	AdmissionMissingScope                 AdmissionFailureClass = "github_admission_missing_scope"
	AdmissionSSORestricted                AdmissionFailureClass = "github_admission_sso_restricted"
	AdmissionRateLimited                  AdmissionFailureClass = "github_admission_rate_limited"
	AdmissionUpstreamUnavailable          AdmissionFailureClass = "github_admission_upstream_unavailable"
	AdmissionInvalidResponse              AdmissionFailureClass = "github_admission_invalid_response"
)

type AdmissionError struct{ Class AdmissionFailureClass }

func (e *AdmissionError) Error() string { return "githuboauth: admission failed: " + string(e.Class) }

func AdmissionFailure(err error) (AdmissionFailureClass, bool) {
	var target *AdmissionError
	if !errors.As(err, &target) {
		return "", false
	}
	return target.Class, true
}

type OrganizationAdmissionGateConfig struct {
	ProviderID uuid.UUID
	Policy     AdmissionConfig
	UserURL    string
	Pool       *pgxpool.Pool
	Secrets    *serverauth.Secrets
}

type organizationAdmissionGate struct {
	providerID uuid.UUID
	policy     AdmissionConfig
	store      *admissionStore
}

func NewOrganizationAdmissionGate(cfg OrganizationAdmissionGateConfig) (AdmissionGate, error) {
	if cfg.ProviderID == uuid.Nil || cfg.Pool == nil || cfg.Secrets == nil ||
		(cfg.Policy.Mode != AdmissionUnrestricted && cfg.Policy.Mode != AdmissionOrganizationRestricted) {
		return nil, errors.New("githuboauth: incomplete admission gate configuration")
	}
	if cfg.Policy.Mode == AdmissionOrganizationRestricted && (len(cfg.Policy.Organizations) == 0 || cfg.Policy.MembershipURL == "") {
		return nil, errors.New("githuboauth: restricted admission gate requires organizations and membership URL")
	}
	return &organizationAdmissionGate{providerID: cfg.ProviderID, policy: cfg.Policy,
		store: &admissionStore{pool: cfg.Pool, secrets: cfg.Secrets, providerID: cfg.ProviderID}}, nil
}

type membershipResult struct {
	organization  ApprovedOrganization
	externalID    int64
	observedLogin string
}

func (g *organizationAdmissionGate) Evaluate(ctx context.Context, request AdmissionRequest) (serverauth.AdmissionEvidence, error) {
	if strings.TrimSpace(request.Identity.Subject) == "" || strings.TrimSpace(request.RequestID) == "" {
		return serverauth.AdmissionEvidence{}, &AdmissionError{Class: AdmissionInvalidResponse}
	}
	if g.policy.Mode == AdmissionUnrestricted {
		return g.store.record(ctx, request.Identity.Subject, request.RequestID, AdmissionUnrestricted, "allowed", "explicit_unrestricted", uuid.Nil)
	}
	if request.Client == nil {
		return serverauth.AdmissionEvidence{}, g.deny(ctx, request, "indeterminate", AdmissionUpstreamUnavailable)
	}
	client := boundedMembershipClient(request.Client)
	var firstIndeterminate AdmissionFailureClass
	pending := false
	for _, organization := range g.policy.Organizations {
		result, class := g.lookupMembership(ctx, client, request.Identity.Subject, organization)
		if class == "" {
			bindingID, err := g.store.bindVerifiedOrganization(ctx, organization, result.externalID, result.observedLogin)
			if err != nil {
				if errors.Is(err, errOrganizationIdentityMismatch) {
					return serverauth.AdmissionEvidence{}, g.deny(ctx, request, "denied", AdmissionOrganizationIdentityMismatch)
				}
				return serverauth.AdmissionEvidence{}, g.deny(ctx, request, "indeterminate", AdmissionUpstreamUnavailable)
			}
			return g.store.record(ctx, request.Identity.Subject, request.RequestID, AdmissionOrganizationRestricted,
				"allowed", "active_membership", bindingID)
		}
		if class == AdmissionPending {
			pending = true
			continue
		}
		if class != AdmissionNoActiveMembership && firstIndeterminate == "" {
			firstIndeterminate = class
		}
	}
	if firstIndeterminate != "" {
		return serverauth.AdmissionEvidence{}, g.deny(ctx, request, "indeterminate", firstIndeterminate)
	}
	if pending {
		return serverauth.AdmissionEvidence{}, g.deny(ctx, request, "denied", AdmissionPending)
	}
	return serverauth.AdmissionEvidence{}, g.deny(ctx, request, "denied", AdmissionNoActiveMembership)
}

func (g *organizationAdmissionGate) deny(ctx context.Context, request AdmissionRequest, decision string, class AdmissionFailureClass) error {
	if _, err := g.store.record(ctx, request.Identity.Subject, request.RequestID, AdmissionOrganizationRestricted,
		decision, string(class), uuid.Nil); err != nil {
		return &AdmissionError{Class: AdmissionUpstreamUnavailable}
	}
	return &AdmissionError{Class: class}
}

func (g *organizationAdmissionGate) lookupMembership(ctx context.Context, client *http.Client, subject string,
	organization ApprovedOrganization) (membershipResult, AdmissionFailureClass) {
	endpoint := strings.TrimRight(g.policy.MembershipURL, "/") + "/" + url.PathEscape(organization.Login)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return membershipResult{}, AdmissionInvalidResponse
	}
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return membershipResult{}, AdmissionUpstreamUnavailable
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusNotFound:
		return membershipResult{}, AdmissionNoActiveMembership
	case response.StatusCode == http.StatusUnauthorized:
		return membershipResult{}, AdmissionMissingScope
	case response.StatusCode == http.StatusForbidden && strings.TrimSpace(response.Header.Get("X-GitHub-SSO")) != "":
		return membershipResult{}, AdmissionSSORestricted
	case response.StatusCode == http.StatusTooManyRequests ||
		(response.StatusCode == http.StatusForbidden && response.Header.Get("X-RateLimit-Remaining") == "0"):
		return membershipResult{}, AdmissionRateLimited
	case response.StatusCode == http.StatusForbidden:
		return membershipResult{}, AdmissionMissingScope
	case response.StatusCode >= 300 && response.StatusCode < 400:
		return membershipResult{}, AdmissionInvalidResponse
	case response.StatusCode >= 500:
		return membershipResult{}, AdmissionUpstreamUnavailable
	case response.StatusCode != http.StatusOK:
		return membershipResult{}, AdmissionInvalidResponse
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && mediaType != "application/vnd.github+json") {
		return membershipResult{}, AdmissionInvalidResponse
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxMembershipBody+1))
	if err != nil || len(body) > maxMembershipBody {
		return membershipResult{}, AdmissionInvalidResponse
	}
	var payload struct {
		State        string `json:"state"`
		Organization struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"organization"`
		User *struct {
			ID int64 `json:"id"`
		} `json:"user,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF || payload.Organization.ID <= 0 {
		return membershipResult{}, AdmissionInvalidResponse
	}
	observedLogin := strings.ToLower(strings.TrimSpace(payload.Organization.Login))
	if payload.User != nil {
		userID, err := strconv.ParseInt(subject, 10, 64)
		if err != nil || payload.User.ID != userID {
			return membershipResult{}, AdmissionInvalidResponse
		}
	}
	if observedLogin != organization.Login || (organization.stableID > 0 && organization.stableID != payload.Organization.ID) {
		return membershipResult{}, AdmissionOrganizationIdentityMismatch
	}
	switch strings.ToLower(strings.TrimSpace(payload.State)) {
	case "active":
		return membershipResult{organization: organization, externalID: payload.Organization.ID, observedLogin: observedLogin}, ""
	case "pending":
		return membershipResult{}, AdmissionPending
	default:
		return membershipResult{}, AdmissionInvalidResponse
	}
}

func boundedMembershipClient(source *http.Client) *http.Client {
	result := *source
	result.Timeout = membershipTimeout
	result.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if oauthTransport, ok := source.Transport.(*oauth2.Transport); ok {
		base := http.DefaultTransport.(*http.Transport).Clone()
		if configured, ok := oauthTransport.Base.(*http.Transport); ok {
			base = configured.Clone()
		}
		base.Proxy = nil
		base.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		base.TLSClientConfig = cloneTLSConfig(base.TLSClientConfig)
		base.TLSClientConfig.MinVersion = tls.VersionTLS12
		base.MaxResponseHeaderBytes = 64 << 10
		result.Transport = &oauth2.Transport{Source: oauthTransport.Source, Base: base}
	}
	return &result
}

func cloneTLSConfig(source *tls.Config) *tls.Config {
	if source == nil {
		return &tls.Config{}
	}
	return source.Clone()
}

func derivedMembershipURL(userURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(userURL))
	if err != nil {
		return DefaultMembershipURL
	}
	parsed.Path = path.Join(strings.TrimSuffix(parsed.Path, "/user"), "/user/memberships/orgs")
	parsed.RawPath, parsed.RawQuery, parsed.Fragment = "", "", ""
	return strings.TrimRight(parsed.String(), "/")
}

func validateMembershipURL(raw, userURL string, production bool) (string, error) {
	membership, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !membership.IsAbs() || membership.Host == "" || membership.User != nil || membership.Opaque != "" ||
		membership.RawQuery != "" || membership.Fragment != "" {
		return "", errors.New("githuboauth: membership_url must be an absolute URL without credentials, query, or fragment")
	}
	user, err := url.Parse(strings.TrimSpace(userURL))
	if err != nil || !user.IsAbs() || user.Host == "" {
		return "", errors.New("githuboauth: user_url must be absolute for restricted admission")
	}
	if production && membership.Scheme != "https" {
		return "", errors.New("githuboauth: membership_url must use https in production")
	}
	if !strings.EqualFold(membership.Scheme, user.Scheme) || !strings.EqualFold(membership.Host, user.Host) {
		return "", errors.New("githuboauth: membership_url must use the same origin as user_url")
	}
	membership.Scheme = strings.ToLower(membership.Scheme)
	membership.Host = strings.ToLower(membership.Host)
	membership.Path = strings.TrimRight(membership.Path, "/")
	return membership.String(), nil
}

func validOrganizationLogin(value string) bool {
	if value == "" || len(value) > 39 || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "--") {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return false
	}
	return true
}
