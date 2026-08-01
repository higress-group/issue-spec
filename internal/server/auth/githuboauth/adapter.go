// Package githuboauth implements GitHub's OAuth application flow. It does not
// model GitHub Actions' OIDC issuer as an interactive identity provider.
package githuboauth

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

type Config struct {
	ProviderID    uuid.UUID
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	Scopes        []string
	Endpoint      oauth2.Endpoint
	UserURL       string
	AdmissionGate AdmissionGate
	Production    bool
}

const maxUserResponseBytes = 1 << 20

type CallbackFailureClass string

const (
	CallbackTokenExchangeFailed             CallbackFailureClass = "github_token_exchange_failed"
	CallbackTokenIncorrectClientCredentials CallbackFailureClass = "github_token_incorrect_client_credentials"
	CallbackTokenRedirectURIMismatch        CallbackFailureClass = "github_token_redirect_uri_mismatch"
	CallbackTokenBadVerificationCode        CallbackFailureClass = "github_token_bad_verification_code"
	CallbackTokenUnverifiedUserEmail        CallbackFailureClass = "github_token_unverified_user_email"
)

type tokenExchangeError struct{ cause error }

func (e *tokenExchangeError) Error() string { return "githuboauth: token exchange failed" }

// CallbackFailure returns only stable, non-secret diagnostics. Provider error
// descriptions and response bodies are deliberately excluded because they may
// contain callback or operator-controlled data.
func CallbackFailure(err error) (CallbackFailureClass, bool) {
	var exchangeErr *tokenExchangeError
	if !errors.As(err, &exchangeErr) {
		return "", false
	}
	var retrieveErr *oauth2.RetrieveError
	if !errors.As(exchangeErr.cause, &retrieveErr) {
		return CallbackTokenExchangeFailed, true
	}
	switch strings.TrimSpace(retrieveErr.ErrorCode) {
	case "incorrect_client_credentials", "invalid_client":
		return CallbackTokenIncorrectClientCredentials, true
	case "redirect_uri_mismatch":
		return CallbackTokenRedirectURIMismatch, true
	case "bad_verification_code", "invalid_grant":
		return CallbackTokenBadVerificationCode, true
	case "unverified_user_email":
		return CallbackTokenUnverifiedUserEmail, true
	default:
		return CallbackTokenExchangeFailed, true
	}
}

type AdmissionRequest struct {
	Client    *http.Client
	Identity  serverauth.ExternalIdentity
	RequestID string
}

type AdmissionGate interface {
	Evaluate(context.Context, AdmissionRequest) (serverauth.AdmissionEvidence, error)
}

type Adapter struct {
	config        Config
	transactions  *serverauth.LoginTransactions
	oauth         oauth2.Config
	userURL       string
	admissionGate AdmissionGate
}

type CallbackResult struct {
	Identity  serverauth.ExternalIdentity
	ReturnTo  string
	Admission *serverauth.AdmissionEvidence
}

type profileUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

func New(cfg Config, transactions *serverauth.LoginTransactions) (*Adapter, error) {
	if cfg.ProviderID == uuid.Nil || strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" || transactions == nil {
		return nil, errors.New("githuboauth: incomplete provider configuration")
	}
	if cfg.Issuer == "" {
		cfg.Issuer = "https://github.com"
	}
	redirect, err := url.Parse(cfg.RedirectURL)
	if err != nil || !redirect.IsAbs() || redirect.Host == "" {
		return nil, errors.New("githuboauth: redirect URL must be absolute")
	}
	if cfg.Endpoint.AuthURL == "" || cfg.Endpoint.TokenURL == "" {
		cfg.Endpoint = github.Endpoint
	}
	cfg.UserURL, err = NormalizeUserURL(cfg.Issuer, cfg.UserURL, cfg.Production)
	if err != nil {
		return nil, err
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"read:user", "user:email"}
	}
	return &Adapter{config: cfg, transactions: transactions, userURL: cfg.UserURL, admissionGate: cfg.AdmissionGate, oauth: oauth2.Config{
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL,
		Endpoint: cfg.Endpoint, Scopes: append([]string(nil), cfg.Scopes...),
	}}, nil
}

func (a *Adapter) ProviderID() uuid.UUID { return a.config.ProviderID }
func (a *Adapter) Kind() string          { return "github-oauth" }

func (a *Adapter) Begin(ctx context.Context, returnTo string) (serverauth.LoginStart, error) {
	tx, err := a.transactions.Begin(ctx, a.config.ProviderID, a.config.RedirectURL, returnTo)
	if err != nil {
		return serverauth.LoginStart{}, err
	}
	return serverauth.LoginStart{AuthorizationURL: a.oauth.AuthCodeURL(tx.State,
		oauth2.SetAuthURLParam("code_challenge", tx.PKCEChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	), BrowserNonce: tx.BrowserNonce, ExpiresAt: tx.ExpiresAt}, nil
}

func (a *Adapter) Callback(ctx context.Context, state, code, browserNonce string) (CallbackResult, error) {
	if strings.TrimSpace(code) == "" {
		return CallbackResult{}, serverauth.ErrInvalidCredential
	}
	tx, err := a.transactions.Consume(ctx, a.config.ProviderID, state, browserNonce)
	if err != nil {
		return CallbackResult{}, err
	}
	token, err := a.oauth.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", tx.PKCEVerifier))
	if err != nil {
		return CallbackResult{}, &tokenExchangeError{cause: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.userURL, nil)
	if err != nil {
		return CallbackResult{}, fmt.Errorf("githuboauth: create user request: %w", err)
	}
	profileClient := newProfileClient(token)
	resp, err := profileClient.Do(req)
	if err != nil {
		return CallbackResult{}, fmt.Errorf("githuboauth: resolve user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CallbackResult{}, fmt.Errorf("githuboauth: user endpoint returned status %d", resp.StatusCode)
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		return CallbackResult{}, errors.New("githuboauth: user endpoint returned an invalid content type")
	}
	user, err := decodeProfileUser(resp.Body)
	if err != nil {
		return CallbackResult{}, err
	}
	if user.ID <= 0 || strings.TrimSpace(user.Login) == "" {
		return CallbackResult{}, errors.New("githuboauth: user endpoint omitted stable numeric id")
	}
	rawClaims, _ := json.Marshal(user)
	var email *string
	if user.Email != "" {
		email = &user.Email
	}
	identity := serverauth.ExternalIdentity{
		Issuer: a.config.Issuer, Subject: strconv.FormatInt(user.ID, 10), Login: user.Login,
		DisplayName: user.Name, Email: email, Claims: rawClaims,
		AvatarURL: serverauth.NormalizeExternalAvatarURL(user.AvatarURL),
	}
	var admission *serverauth.AdmissionEvidence
	if a.admissionGate != nil {
		evidence, err := a.admissionGate.Evaluate(ctx, AdmissionRequest{Client: profileClient, Identity: identity,
			RequestID: serverauth.AdmissionRequestID(ctx)})
		if err != nil {
			return CallbackResult{}, err
		}
		admission = &evidence
	}
	return CallbackResult{Identity: identity, ReturnTo: tx.ReturnTo, Admission: admission}, nil
}

func decodeProfileUser(reader io.Reader) (profileUser, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxUserResponseBytes+1))
	if err != nil {
		return profileUser{}, fmt.Errorf("githuboauth: read user: %w", err)
	}
	if len(body) > maxUserResponseBytes {
		return profileUser{}, errors.New("githuboauth: user endpoint response is too large")
	}
	var user profileUser
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&user); err != nil {
		return profileUser{}, fmt.Errorf("githuboauth: decode user: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return profileUser{}, errors.New("githuboauth: user endpoint returned multiple JSON values")
	}
	return user, nil
}

// NormalizeUserURL binds the GitHub profile endpoint to the provider's
// expected API origin. github.com uses api.github.com; enterprise providers
// use their issuer origin. Production endpoints are HTTPS-only.
func NormalizeUserURL(issuer, raw string, production bool) (string, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	issuerOrigin, err := publicurl.ParseOrigin("githuboauth issuer", issuer)
	if err != nil {
		return "", fmt.Errorf("githuboauth: invalid issuer: %w", err)
	}
	if production && !strings.HasPrefix(issuerOrigin.String(), "https://") {
		return "", errors.New("githuboauth: issuer must use HTTPS in production")
	}
	expectedOrigin := issuerOrigin.String()
	if expectedOrigin == "https://github.com" {
		expectedOrigin = "https://api.github.com"
	}
	if strings.TrimSpace(raw) == "" {
		if expectedOrigin == "https://api.github.com" {
			raw = DefaultUserURL
		} else {
			raw = expectedOrigin + "/api/v3/user"
		}
	}
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\\\r\n\t") {
		return "", errors.New("githuboauth: user_url must be canonical")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" ||
		parsed.Path == "" || parsed.Path[0] != '/' || parsed.RawPath != "" || path.Clean(parsed.Path) != parsed.Path {
		return "", errors.New("githuboauth: user_url must be a canonical absolute endpoint")
	}
	userOrigin, err := publicurl.ParseOrigin("githuboauth user origin", parsed.Scheme+"://"+parsed.Host)
	if err != nil {
		return "", fmt.Errorf("githuboauth: invalid user_url: %w", err)
	}
	if production && !strings.HasPrefix(userOrigin.String(), "https://") {
		return "", errors.New("githuboauth: user_url must use HTTPS in production")
	}
	if userOrigin.String() != expectedOrigin {
		return "", fmt.Errorf("githuboauth: user_url origin must be %s", expectedOrigin)
	}
	canonical := userOrigin.String() + parsed.EscapedPath()
	if canonical != raw {
		return "", errors.New("githuboauth: user_url must be canonical")
	}
	return canonical, nil
}

func newProfileClient(token *oauth2.Token) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	base := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           8,
		MaxConnsPerHost:        4,
		MaxResponseHeaderBytes: 64 << 10,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: &oauth2.Transport{Source: oauth2.StaticTokenSource(token), Base: base},
		Timeout:   10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (a *Adapter) CompleteLogin(ctx context.Context, state, code, browserNonce string) (serverauth.LoginCompletion, error) {
	result, err := a.Callback(ctx, state, code, browserNonce)
	return serverauth.LoginCompletion{Identity: result.Identity, ReturnTo: result.ReturnTo, Admission: result.Admission}, err
}

func (a *Adapter) Complete(ctx context.Context, state, code, browserNonce string) (serverauth.ExternalIdentity, string, error) {
	result, err := a.Callback(ctx, state, code, browserNonce)
	return result.Identity, result.ReturnTo, err
}
