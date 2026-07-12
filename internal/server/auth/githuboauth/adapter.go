// Package githuboauth implements GitHub's OAuth application flow. It does not
// model GitHub Actions' OIDC issuer as an interactive identity provider.
package githuboauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
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
	if cfg.UserURL == "" {
		cfg.UserURL = "https://api.github.com/user"
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

func (a *Adapter) Begin(ctx context.Context, returnTo string) (string, error) {
	tx, err := a.transactions.Begin(ctx, a.config.ProviderID, a.config.RedirectURL, returnTo)
	if err != nil {
		return "", err
	}
	return a.oauth.AuthCodeURL(tx.State,
		oauth2.SetAuthURLParam("code_challenge", tx.PKCEChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	), nil
}

func (a *Adapter) Callback(ctx context.Context, state, code string) (CallbackResult, error) {
	if strings.TrimSpace(code) == "" {
		return CallbackResult{}, serverauth.ErrInvalidCredential
	}
	tx, err := a.transactions.Consume(ctx, a.config.ProviderID, state)
	if err != nil {
		return CallbackResult{}, err
	}
	token, err := a.oauth.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", tx.PKCEVerifier))
	if err != nil {
		return CallbackResult{}, fmt.Errorf("githuboauth: code exchange failed: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.userURL, nil)
	if err != nil {
		return CallbackResult{}, fmt.Errorf("githuboauth: create user request: %w", err)
	}
	resp, err := a.oauth.Client(ctx, token).Do(req)
	if err != nil {
		return CallbackResult{}, fmt.Errorf("githuboauth: resolve user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CallbackResult{}, fmt.Errorf("githuboauth: user endpoint returned status %d", resp.StatusCode)
	}
	var user struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&user); err != nil {
		return CallbackResult{}, fmt.Errorf("githuboauth: decode user: %w", err)
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
		evidence, err := a.admissionGate.Evaluate(ctx, AdmissionRequest{Client: a.oauth.Client(ctx, token), Identity: identity,
			RequestID: serverauth.AdmissionRequestID(ctx)})
		if err != nil {
			return CallbackResult{}, err
		}
		admission = &evidence
	}
	return CallbackResult{Identity: identity, ReturnTo: tx.ReturnTo, Admission: admission}, nil
}

func (a *Adapter) CompleteLogin(ctx context.Context, state, code string) (serverauth.LoginCompletion, error) {
	result, err := a.Callback(ctx, state, code)
	return serverauth.LoginCompletion{Identity: result.Identity, ReturnTo: result.ReturnTo, Admission: result.Admission}, err
}

func (a *Adapter) Complete(ctx context.Context, state, code string) (serverauth.ExternalIdentity, string, error) {
	result, err := a.Callback(ctx, state, code)
	return result.Identity, result.ReturnTo, err
}
