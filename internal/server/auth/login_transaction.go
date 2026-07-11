package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const loginTransactionTTL = 10 * time.Minute

type LoginTransaction struct {
	ID            uuid.UUID
	State         string
	Nonce         string
	PKCEVerifier  string
	PKCEChallenge string
	RedirectURI   string
	ReturnTo      string
	ExpiresAt     time.Time
	NonceHash     []byte
}

type LoginTransactions struct {
	pool    *pgxpool.Pool
	secrets *Secrets
	now     func() time.Time
}

func NewLoginTransactions(pool *pgxpool.Pool, secrets *Secrets) *LoginTransactions {
	return &LoginTransactions{pool: pool, secrets: secrets, now: func() time.Time { return time.Now().UTC() }}
}

func (s *LoginTransactions) Begin(ctx context.Context, providerID uuid.UUID, redirectURI, returnTo string) (LoginTransaction, error) {
	if s == nil || s.pool == nil || s.secrets == nil || providerID == uuid.Nil || strings.TrimSpace(redirectURI) == "" {
		return LoginTransaction{}, ErrInvalidCredential
	}
	state, _, err := s.secrets.RandomToken("state")
	if err != nil {
		return LoginTransaction{}, err
	}
	nonce, _, err := s.secrets.RandomToken("nonce")
	if err != nil {
		return LoginTransaction{}, err
	}
	verifier, _, err := s.secrets.RandomToken("pkce")
	if err != nil {
		return LoginTransaction{}, err
	}
	ciphertext, err := s.secrets.Encrypt("oauth-pkce-verifier", []byte(verifier))
	if err != nil {
		return LoginTransaction{}, err
	}
	now := s.now()
	tx := LoginTransaction{
		ID:            uuid.New(),
		State:         state,
		Nonce:         nonce,
		PKCEVerifier:  verifier,
		PKCEChallenge: pkceChallenge(verifier),
		RedirectURI:   redirectURI,
		ReturnTo:      returnTo,
		ExpiresAt:     now.Add(loginTransactionTTL),
		NonceHash:     s.secrets.Digest("oauth-nonce", nonce),
	}
	var returnValue any
	if returnTo != "" {
		returnValue = returnTo
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO oauth_login_transactions
		(id, provider_id, state_hash, nonce_hash, pkce_verifier_ciphertext, redirect_uri, return_to, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, tx.ID, providerID,
		s.secrets.Digest("oauth-state", state), tx.NonceHash, ciphertext, redirectURI, returnValue, tx.ExpiresAt, now)
	if err != nil {
		return LoginTransaction{}, fmt.Errorf("auth: persist login transaction: %w", err)
	}
	return tx, nil
}

// Consume atomically changes the transaction from pending to consumed. The
// provider predicate prevents OAuth/OIDC adapter mix-up and the update prevents
// callback replay.
func (s *LoginTransactions) Consume(ctx context.Context, providerID uuid.UUID, state string) (LoginTransaction, error) {
	if providerID == uuid.Nil || state == "" {
		return LoginTransaction{}, ErrInvalidState
	}
	now := s.now()
	var tx LoginTransaction
	var encrypted []byte
	var returnTo *string
	err := s.pool.QueryRow(ctx, `UPDATE oauth_login_transactions
		SET consumed_at = $3
		WHERE provider_id = $1 AND state_hash = $2 AND consumed_at IS NULL AND expires_at > $3
		RETURNING id, nonce_hash, pkce_verifier_ciphertext, redirect_uri, return_to, expires_at`,
		providerID, s.secrets.Digest("oauth-state", state), now).
		Scan(&tx.ID, &tx.NonceHash, &encrypted, &tx.RedirectURI, &returnTo, &tx.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginTransaction{}, ErrInvalidState
	}
	if err != nil {
		return LoginTransaction{}, fmt.Errorf("auth: consume login transaction: %w", err)
	}
	verifier, err := s.secrets.Decrypt("oauth-pkce-verifier", encrypted)
	if err != nil {
		return LoginTransaction{}, err
	}
	tx.State = state
	tx.PKCEVerifier = string(verifier)
	if returnTo != nil {
		tx.ReturnTo = *returnTo
	}
	return tx, nil
}

// NonceDigest lets provider adapters validate nonce without exposing the
// pepper or retaining plaintext nonce in the database.
func (s *LoginTransactions) NonceDigest(nonce string) []byte {
	return s.secrets.Digest("oauth-nonce", nonce)
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
