package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const randomSecretBytes = 32

type Secrets struct {
	pepper []byte
	aead   cipher.AEAD
	random io.Reader
}

func NewSecrets(pepper, encryptionKey []byte) (*Secrets, error) {
	if len(pepper) < 32 {
		return nil, errors.New("auth: token pepper must contain at least 32 bytes")
	}
	key := sha256.Sum256(encryptionKey)
	if len(encryptionKey) < 32 {
		return nil, errors.New("auth: encryption key must contain at least 32 bytes")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("auth: initialize encryption: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("auth: initialize encryption mode: %w", err)
	}
	return &Secrets{pepper: append([]byte(nil), pepper...), aead: aead, random: rand.Reader}, nil
}

func (s *Secrets) RandomToken(kind string) (token, prefix string, err error) {
	if s == nil {
		return "", "", errors.New("auth: nil secret manager")
	}
	prefixRaw := make([]byte, 6)
	secretRaw := make([]byte, randomSecretBytes)
	if _, err := io.ReadFull(s.random, prefixRaw); err != nil {
		return "", "", fmt.Errorf("auth: generate token prefix: %w", err)
	}
	if _, err := io.ReadFull(s.random, secretRaw); err != nil {
		return "", "", fmt.Errorf("auth: generate token secret: %w", err)
	}
	// Prefixes are hexadecimal so the underscore separator cannot occur inside
	// the indexed prefix. The high-entropy secret remains base64url encoded.
	prefix = hex.EncodeToString(prefixRaw)
	token = "iss_" + kind + "_" + prefix + "_" + base64.RawURLEncoding.EncodeToString(secretRaw)
	return token, prefix, nil
}

func TokenPrefix(token, kind string) (string, error) {
	rest, ok := strings.CutPrefix(token, "iss_"+kind+"_")
	if !ok {
		return "", ErrInvalidCredential
	}
	prefix, secret, ok := strings.Cut(rest, "_")
	if !ok || prefix == "" || secret == "" {
		return "", ErrInvalidCredential
	}
	if _, err := hex.DecodeString(prefix); err != nil {
		return "", ErrInvalidCredential
	}
	return prefix, nil
}

func (s *Secrets) Digest(purpose, value string) []byte {
	h := hmac.New(sha256.New, s.pepper)
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write([]byte(value))
	return h.Sum(nil)
}

func EqualDigest(got, want []byte) bool {
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (s *Secrets) Encrypt(purpose string, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(s.random, nonce); err != nil {
		return nil, fmt.Errorf("auth: generate encryption nonce: %w", err)
	}
	return append(nonce, s.aead.Seal(nil, nonce, plaintext, []byte(purpose))...), nil
}

func (s *Secrets) Decrypt(purpose string, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < s.aead.NonceSize() {
		return nil, ErrInvalidCredential
	}
	nonce, payload := ciphertext[:s.aead.NonceSize()], ciphertext[s.aead.NonceSize():]
	plaintext, err := s.aead.Open(nil, nonce, payload, []byte(purpose))
	if err != nil {
		return nil, ErrInvalidCredential
	}
	return plaintext, nil
}
