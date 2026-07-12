package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/google/uuid"
	nativeauth "github.com/higress-group/issue-spec/internal/server/api/native/auth"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/githuboauth"
	"github.com/higress-group/issue-spec/internal/server/auth/oidc"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

type providerFile struct {
	Providers []providerConfig `json:"providers"`
}

type providerConfig struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	Issuer        string    `json:"issuer"`
	ClientID      string    `json:"client_id"`
	ClientSecret  string    `json:"client_secret"`
	Scopes        []string  `json:"scopes,omitempty"`
	AuthURL       string    `json:"auth_url,omitempty"`
	TokenURL      string    `json:"token_url,omitempty"`
	UserURL       string    `json:"user_url,omitempty"`
	AvatarOrigins []string  `json:"avatar_origins,omitempty"`
}

func configureAdapters(ctx context.Context, pool *pgxpool.Pool, secrets *serverauth.Secrets,
	origins publicurl.Origins, raw []byte) (map[string]nativeauth.LoginAdapter, error) {
	result := map[string]nativeauth.LoginAdapter{}
	if len(raw) == 0 {
		return result, nil
	}
	var file providerFile
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("authentication providers: invalid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("authentication providers: multiple JSON values are forbidden")
	}
	if len(file.Providers) == 0 {
		return nil, errors.New("authentication providers: at least one provider is required")
	}
	for index := range file.Providers {
		cfg := &file.Providers[index]
		if len(cfg.AvatarOrigins) == 0 && strings.TrimSpace(cfg.Kind) == "github-oauth" {
			if strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/") == "https://github.com" {
				cfg.AvatarOrigins = []string{"https://avatars.githubusercontent.com"}
			} else if issuer, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/")); err == nil && issuer.Scheme == "https" && issuer.Host != "" {
				cfg.AvatarOrigins = []string{issuer.Scheme + "://" + issuer.Host}
			}
		}
		for avatarIndex, rawOrigin := range cfg.AvatarOrigins {
			origin, originErr := publicurl.ParseOrigin("avatar origin", strings.TrimSpace(rawOrigin))
			if originErr != nil || !strings.HasPrefix(origin.String(), "https://") {
				return nil, fmt.Errorf("authentication provider %q: avatar origins must be canonical HTTPS origins", cfg.Name)
			}
			cfg.AvatarOrigins[avatarIndex] = origin.String()
		}
	}
	transactions := serverauth.NewLoginTransactions(pool, secrets)
	seenIDs := map[uuid.UUID]struct{}{}
	for _, cfg := range file.Providers {
		cfg.Name = strings.TrimSpace(cfg.Name)
		cfg.Kind = strings.TrimSpace(cfg.Kind)
		cfg.Issuer = strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/")
		if cfg.ID == uuid.Nil || !providerName(cfg.Name) || cfg.Issuer == "" ||
			strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
			return nil, errors.New("authentication providers: id, safe name, issuer, client id and secret are required")
		}
		if _, exists := result[cfg.Name]; exists {
			return nil, fmt.Errorf("authentication providers: duplicate name %q", cfg.Name)
		}
		if _, exists := seenIDs[cfg.ID]; exists {
			return nil, fmt.Errorf("authentication providers: duplicate id %s", cfg.ID)
		}
		seenIDs[cfg.ID] = struct{}{}
		redirect := origins.API.MustURL("/api/v1/auth/" + url.PathEscape(cfg.Name) + "/callback")
		var adapter nativeauth.LoginAdapter
		var err error
		switch cfg.Kind {
		case "oidc":
			adapter, err = oidc.New(ctx, oidc.Config{ProviderID: cfg.ID, Name: cfg.Name, Issuer: cfg.Issuer,
				ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: redirect, Scopes: cfg.Scopes}, transactions)
		case "github-oauth":
			endpoint := oauth2.Endpoint{AuthURL: strings.TrimSpace(cfg.AuthURL), TokenURL: strings.TrimSpace(cfg.TokenURL)}
			if (endpoint.AuthURL == "") != (endpoint.TokenURL == "") {
				return nil, errors.New("authentication providers: github auth_url and token_url must be configured together")
			}
			adapter, err = githuboauth.New(githuboauth.Config{ProviderID: cfg.ID, Issuer: cfg.Issuer,
				ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: redirect,
				Scopes: cfg.Scopes, Endpoint: endpoint, UserURL: strings.TrimSpace(cfg.UserURL)}, transactions)
		default:
			return nil, fmt.Errorf("authentication providers: unsupported kind %q", cfg.Kind)
		}
		if err != nil {
			return nil, fmt.Errorf("authentication provider %q: %w", cfg.Name, err)
		}
		result[cfg.Name] = adapter
	}
	if err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		for _, cfg := range file.Providers {
			metadata, _ := json.Marshal(map[string]any{"scopes": cfg.Scopes, "avatar_origins": cfg.AvatarOrigins})
			if _, err := tx.Exec(ctx, `INSERT INTO auth_providers (id, name, kind, issuer, enabled, config)
				VALUES ($1, $2, $3, $4, true, $5)
				ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, kind = EXCLUDED.kind,
				issuer = EXCLUDED.issuer, enabled = true, config = EXCLUDED.config,
				representation_version = auth_providers.representation_version + 1,
				updated_at = clock_timestamp()`, cfg.ID, cfg.Name, cfg.Kind, strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/"), metadata); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("authentication providers: persist public metadata: %w", err)
	}
	return result, nil
}

func configuredAvatarOrigins(raw []byte) (map[uuid.UUID][]string, error) {
	result := map[uuid.UUID][]string{}
	if len(raw) == 0 {
		return result, nil
	}
	var file providerFile
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}
	for _, cfg := range file.Providers {
		origins := append([]string(nil), cfg.AvatarOrigins...)
		if len(origins) == 0 && strings.TrimSpace(cfg.Kind) == "github-oauth" {
			if strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/") == "https://github.com" {
				origins = []string{"https://avatars.githubusercontent.com"}
			} else if issuer, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/")); err == nil && issuer.Scheme == "https" && issuer.Host != "" {
				origins = []string{issuer.Scheme + "://" + issuer.Host}
			}
		}
		for index, raw := range origins {
			origin, err := publicurl.ParseOrigin("avatar origin", strings.TrimSpace(raw))
			if err != nil || !strings.HasPrefix(origin.String(), "https://") {
				return nil, errors.New("avatar origins must be canonical HTTPS origins")
			}
			origins[index] = origin.String()
		}
		result[cfg.ID] = origins
	}
	return result, nil
}

func providerName(value string) bool {
	if value == "" || len(value) > 64 {
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
