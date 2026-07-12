package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/githuboauth"
	serveroidc "github.com/higress-group/issue-spec/internal/server/auth/oidc"
	"golang.org/x/oauth2"
)

func TestOIDCDiscoveryPKCEValidationReplayAndFailureMatrix(t *testing.T) {
	pool := migratedPool(t)
	secrets := testSecrets(t)
	transactions := serverauth.NewLoginTransactions(pool, secrets)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const clientID = "issue-spec-client"
	var server *httptest.Server
	var nonce, challenge, mode string
	var tokenCalls int
	handler := http.NewServeMux()
	handler.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]any{
			"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize",
			"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"code_challenge_methods_supported":      []string{"S256"},
		})
	})
	handler.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &privateKey.PublicKey, KeyID: "primary", Algorithm: string(jose.RS256), Use: "sig"}}})
	})
	handler.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := pkceForTest(r.Form.Get("code_verifier")); got != challenge {
			t.Errorf("PKCE challenge = %q, want %q", got, challenge)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		now := time.Now()
		issuer := server.URL
		audience := jwt.Audience{clientID}
		expiry := now.Add(5 * time.Minute)
		tokenNonce := nonce
		signingKey := privateKey
		switch mode {
		case "wrong-issuer":
			issuer = server.URL + "/other"
		case "wrong-audience":
			audience = jwt.Audience{"other-client"}
		case "expired":
			expiry = now.Add(-time.Minute)
		case "wrong-nonce":
			tokenNonce = "attacker-nonce"
		case "bad-signature":
			signingKey = otherKey
		}
		idToken := signIDToken(t, signingKey, issuer, "subject-123", audience, expiry, tokenNonce)
		writeTestJSON(w, map[string]any{"access_token": "access", "token_type": "Bearer", "expires_in": 300, "id_token": idToken})
	})
	server = httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider := insertProvider(t, pool, "generic-oidc", "oidc", server.URL)
	adapter, err := serveroidc.New(t.Context(), serveroidc.Config{
		ProviderID: provider.ID, Issuer: server.URL, ClientID: clientID, ClientSecret: "secret",
		RedirectURL: "https://issues.example.test/api/v1/auth/oidc/callback",
	}, transactions)
	if err != nil {
		t.Fatal(err)
	}

	begin := func(t *testing.T) (string, string) {
		t.Helper()
		start, err := adapter.Begin(t.Context(), "/dashboard")
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(start.AuthorizationURL)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		if query.Get("code_challenge_method") != "S256" || query.Get("state") == "" || query.Get("nonce") == "" {
			t.Fatalf("authorization query missing state/nonce/PKCE: %s", parsed.RawQuery)
		}
		if start.BrowserNonce == "" || start.BrowserNonce == query.Get("state") {
			t.Fatalf("browser nonce is missing or aliases OAuth state")
		}
		nonce, challenge = query.Get("nonce"), query.Get("code_challenge")
		return query.Get("state"), start.BrowserNonce
	}

	mode = "valid"
	state, browserNonce := begin(t)
	if _, _, err := adapter.Complete(t.Context(), state, "code", "wrong-browser"); !errors.Is(err, serverauth.ErrInvalidState) {
		t.Fatalf("wrong-browser callback error = %v", err)
	}
	if tokenCalls != 0 {
		t.Fatalf("wrong-browser callback exchanged code %d times", tokenCalls)
	}
	identity, returnTo, err := adapter.Complete(t.Context(), state, "code", browserNonce)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Issuer != server.URL || identity.Subject != "subject-123" || identity.Login != "alice" || identity.AvatarURL != "https://images.example/alice.png" || returnTo != "/dashboard" {
		t.Fatalf("OIDC result = %+v returnTo=%q", identity, returnTo)
	}
	if _, _, err := adapter.Complete(t.Context(), state, "code", browserNonce); !errors.Is(err, serverauth.ErrInvalidState) {
		t.Fatalf("callback replay error = %v", err)
	}

	otherProvider := insertProvider(t, pool, "other-oidc", "oidc", server.URL+"/tenant")
	transaction, err := transactions.Begin(t.Context(), provider.ID, "https://issues.example.test/callback", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transactions.Consume(t.Context(), otherProvider.ID, transaction.State, transaction.BrowserNonce); !errors.Is(err, serverauth.ErrInvalidState) {
		t.Fatalf("provider mix-up error = %v", err)
	}
	if _, err := transactions.Consume(t.Context(), provider.ID, transaction.State, transaction.BrowserNonce); err != nil {
		t.Fatalf("wrong-provider attempt consumed valid transaction: %v", err)
	}

	for _, failureMode := range []string{"wrong-issuer", "wrong-audience", "expired", "wrong-nonce", "bad-signature"} {
		t.Run(failureMode, func(t *testing.T) {
			mode = failureMode
			state, browserNonce := begin(t)
			if _, _, err := adapter.Complete(t.Context(), state, "code", browserNonce); err == nil {
				t.Fatalf("OIDC callback unexpectedly accepted %s token", failureMode)
			}
		})
	}
}

func TestGitHubOAuthUsesStableNumericIdentityWithPKCEAndReplayProtection(t *testing.T) {
	pool := migratedPool(t)
	transactions := serverauth.NewLoginTransactions(pool, testSecrets(t))
	var server *httptest.Server
	var challenge string
	var tokenCalls int
	login, email := "octocat", "old@example.test"
	githubID := int64(987654321)
	var gateRequest githuboauth.AdmissionRequest
	handler := http.NewServeMux()
	handler.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if pkceForTest(r.Form.Get("code_verifier")) != challenge {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeTestJSON(w, map[string]any{"access_token": "github-access", "token_type": "bearer", "scope": "read:user,user:email"})
	})
	handler.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer github-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(w, map[string]any{"id": githubID, "login": login, "name": "Octo Cat", "email": email, "avatar_url": "https://avatars.githubusercontent.com/u/1?v=4"})
	})
	server = httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider := insertProvider(t, pool, "github", "github-oauth", server.URL)
	adapter, err := githuboauth.New(githuboauth.Config{
		ProviderID: provider.ID, Issuer: server.URL, ClientID: "client", ClientSecret: "secret",
		RedirectURL: "https://issues.example.test/api/v1/auth/github/callback",
		Endpoint:    oauth2.Endpoint{AuthURL: server.URL + "/authorize", TokenURL: server.URL + "/token"},
		UserURL:     server.URL + "/user",
		AdmissionGate: admissionGateFunc(func(_ context.Context, request githuboauth.AdmissionRequest) (serverauth.AdmissionEvidence, error) {
			gateRequest = request
			return serverauth.AdmissionEvidence{Policy: "github-organization", Decision: "allowed", Subject: request.Identity.Subject, RequestID: request.RequestID, Audited: true}, nil
		}),
	}, transactions)
	if err != nil {
		t.Fatal(err)
	}
	complete := func(t *testing.T) (serverauth.ExternalIdentity, string) {
		t.Helper()
		start, err := adapter.Begin(t.Context(), "/settings")
		if err != nil {
			t.Fatal(err)
		}
		parsed, _ := url.Parse(start.AuthorizationURL)
		query := parsed.Query()
		if query.Get("code_challenge_method") != "S256" || query.Get("state") == "" {
			t.Fatalf("GitHub authorize query missing state/PKCE: %s", parsed.RawQuery)
		}
		if start.BrowserNonce == "" || start.BrowserNonce == query.Get("state") {
			t.Fatalf("GitHub browser nonce is missing or aliases OAuth state")
		}
		challenge = query.Get("code_challenge")
		ctx := serverauth.WithAdmissionRequestID(t.Context(), "login-request")
		beforeTokenCalls := tokenCalls
		if _, err := adapter.CompleteLogin(ctx, query.Get("state"), "code", "wrong-browser"); !errors.Is(err, serverauth.ErrInvalidState) {
			t.Fatalf("wrong-browser GitHub callback error = %v", err)
		}
		if tokenCalls != beforeTokenCalls {
			t.Fatalf("wrong-browser GitHub callback exchanged code")
		}
		completion, err := adapter.CompleteLogin(ctx, query.Get("state"), "code", start.BrowserNonce)
		if err != nil {
			t.Fatal(err)
		}
		identity, returnTo := completion.Identity, completion.ReturnTo
		if completion.Admission == nil || completion.Admission.RequestID != "login-request" || gateRequest.Client == nil || gateRequest.RequestID != "login-request" {
			t.Fatalf("admission completion=%+v request=%+v", completion.Admission, gateRequest)
		}
		if _, _, err := adapter.Complete(t.Context(), query.Get("state"), "code", start.BrowserNonce); !errors.Is(err, serverauth.ErrInvalidState) {
			t.Fatalf("GitHub callback replay error = %v", err)
		}
		return identity, returnTo
	}
	first, returnTo := complete(t)
	if first.Subject != "987654321" || first.Login != "octocat" || first.Email == nil || *first.Email != email || returnTo != "/settings" {
		t.Fatalf("first GitHub identity = %+v returnTo=%q", first, returnTo)
	}
	login, email = "renamed-octocat", "new@example.test"
	second, _ := complete(t)
	if second.Subject != first.Subject || second.Login == first.Login || second.Email == nil || *second.Email != email {
		t.Fatalf("GitHub stable numeric identity/drift = first %+v second %+v", first, second)
	}
	githubID = 0
	start, err := adapter.Begin(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	challenge = parsed.Query().Get("code_challenge")
	if _, _, err := adapter.Complete(t.Context(), parsed.Query().Get("state"), "code", start.BrowserNonce); err == nil {
		t.Fatal("GitHub OAuth accepted a user response without a stable positive numeric id")
	}
}

type admissionGateFunc func(context.Context, githuboauth.AdmissionRequest) (serverauth.AdmissionEvidence, error)

func (f admissionGateFunc) Evaluate(ctx context.Context, request githuboauth.AdmissionRequest) (serverauth.AdmissionEvidence, error) {
	return f(ctx, request)
}

func signIDToken(t *testing.T, key *rsa.PrivateKey, issuer, subject string, audience jwt.Audience, expiry time.Time, nonce string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "primary"))
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: issuer, Subject: subject, Audience: audience,
		IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)), Expiry: jwt.NewNumericDate(expiry),
	}).Claims(map[string]any{
		"nonce": nonce, "preferred_username": "alice", "name": "Alice", "email": "alice@example.test", "picture": "https://images.example/alice.png?tracking=1",
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return serialized
}

func pkceForTest(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func writeTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(fmt.Sprintf("encode test response: %v", err))
	}
}
