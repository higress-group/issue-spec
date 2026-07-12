// Package oidc implements generic OpenID Connect discovery and authorization
// code login. GitHub interactive login intentionally lives in githuboauth.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"golang.org/x/oauth2"
)

type Config struct {
	ProviderID   uuid.UUID
	Name         string
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
}

type Adapter struct {
	config       Config
	transactions *serverauth.LoginTransactions
	provider     *coreoidc.Provider
	verifier     *coreoidc.IDTokenVerifier
	oauth        oauth2.Config
}

type CallbackResult struct {
	Identity serverauth.ExternalIdentity
	ReturnTo string
}

func New(ctx context.Context, cfg Config, transactions *serverauth.LoginTransactions) (*Adapter, error) {
	if cfg.ProviderID == uuid.Nil || strings.TrimSpace(cfg.Issuer) == "" || strings.TrimSpace(cfg.ClientID) == "" ||
		strings.TrimSpace(cfg.ClientSecret) == "" || transactions == nil {
		return nil, errors.New("oidc: incomplete provider configuration")
	}
	redirect, err := url.Parse(cfg.RedirectURL)
	if err != nil || !redirect.IsAbs() || redirect.Host == "" {
		return nil, errors.New("oidc: redirect URL must be absolute")
	}
	provider, err := coreoidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discovery failed: %w", err)
	}
	scopes := append([]string{coreoidc.ScopeOpenID, "profile", "email"}, cfg.Scopes...)
	return &Adapter{
		config:       cfg,
		transactions: transactions,
		provider:     provider,
		verifier:     provider.Verifier(&coreoidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       unique(scopes),
		},
	}, nil
}

func (a *Adapter) ProviderID() uuid.UUID { return a.config.ProviderID }
func (a *Adapter) Kind() string          { return "oidc" }

func (a *Adapter) Begin(ctx context.Context, returnTo string) (serverauth.LoginStart, error) {
	tx, err := a.transactions.Begin(ctx, a.config.ProviderID, a.config.RedirectURL, returnTo)
	if err != nil {
		return serverauth.LoginStart{}, err
	}
	return serverauth.LoginStart{AuthorizationURL: a.oauth.AuthCodeURL(tx.State,
		coreoidc.Nonce(tx.Nonce),
		oauth2.SetAuthURLParam("code_challenge", tx.PKCEChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.AccessTypeOffline,
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
		return CallbackResult{}, fmt.Errorf("oidc: code exchange failed: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return CallbackResult{}, errors.New("oidc: token response did not include id_token")
	}
	idToken, err := a.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return CallbackResult{}, fmt.Errorf("oidc: id token validation failed: %w", err)
	}
	var claims struct {
		Issuer            string `json:"iss"`
		Subject           string `json:"sub"`
		Nonce             string `json:"nonce"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Email             string `json:"email"`
		Picture           string `json:"picture"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return CallbackResult{}, fmt.Errorf("oidc: decode claims: %w", err)
	}
	if claims.Subject == "" || claims.Issuer != a.config.Issuer ||
		!serverauth.EqualDigest(a.transactionsNonceDigest(claims.Nonce), tx.NonceHash) {
		return CallbackResult{}, serverauth.ErrInvalidCredential
	}
	var rawClaims json.RawMessage
	if err := idToken.Claims(&rawClaims); err != nil {
		return CallbackResult{}, fmt.Errorf("oidc: preserve claims: %w", err)
	}
	var email *string
	if strings.TrimSpace(claims.Email) != "" {
		value := claims.Email
		email = &value
	}
	return CallbackResult{Identity: serverauth.ExternalIdentity{
		Issuer:      claims.Issuer,
		Subject:     claims.Subject,
		Login:       claims.PreferredUsername,
		DisplayName: claims.Name,
		Email:       email,
		AvatarURL:   serverauth.NormalizeExternalAvatarURL(claims.Picture),
		Claims:      rawClaims,
	}, ReturnTo: tx.ReturnTo}, nil
}

func (a *Adapter) Complete(ctx context.Context, state, code, browserNonce string) (serverauth.ExternalIdentity, string, error) {
	result, err := a.Callback(ctx, state, code, browserNonce)
	return result.Identity, result.ReturnTo, err
}

func (a *Adapter) CompleteLogin(ctx context.Context, state, code, browserNonce string) (serverauth.LoginCompletion, error) {
	identity, returnTo, err := a.Complete(ctx, state, code, browserNonce)
	return serverauth.LoginCompletion{Identity: identity, ReturnTo: returnTo}, err
}

func (a *Adapter) transactionsNonceDigest(nonce string) []byte {
	return a.transactions.NonceDigest(nonce)
}

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
