package subscriptions

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
)

type Keyring struct {
	current string
	keys    map[string]cipher.AEAD
	random  io.Reader
}

func NewKeyring(current string, keys map[string][]byte) (*Keyring, error) {
	current = strings.TrimSpace(current)
	if current == "" || len(keys) == 0 {
		return nil, errors.New("webhook keyring: current key and key material are required")
	}
	result := &Keyring{current: current, keys: make(map[string]cipher.AEAD, len(keys)), random: rand.Reader}
	for id, material := range keys {
		if strings.TrimSpace(id) == "" || len(material) < 32 {
			return nil, errors.New("webhook keyring: key identifiers must be non-empty and keys at least 32 bytes")
		}
		digest := sha256.Sum256(material)
		block, err := aes.NewCipher(digest[:])
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		result.keys[id] = aead
	}
	if _, ok := result.keys[current]; !ok {
		return nil, errors.New("webhook keyring: current key id is not present")
	}
	return result, nil
}

func (k *Keyring) GenerateSecret() (string, error) {
	if k == nil {
		return "", errors.New("webhook keyring: nil keyring")
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(k.random, raw); err != nil {
		return "", err
	}
	return "iss_webhook_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func (k *Keyring) Encrypt(subscriptionID uuid.UUID, version int64, plaintext []byte) (string, []byte, error) {
	return k.EncryptPurpose(subscriptionID, version, "signing-secret", plaintext)
}

func (k *Keyring) EncryptPurpose(subscriptionID uuid.UUID, version int64, purpose string, plaintext []byte) (string, []byte, error) {
	aead, ok := k.keys[k.current]
	if !ok || subscriptionID == uuid.Nil || version < 1 || strings.TrimSpace(purpose) == "" || len(plaintext) == 0 {
		return "", nil, ErrInvalidInput
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(k.random, nonce); err != nil {
		return "", nil, err
	}
	ciphertext := append(nonce, aead.Seal(nil, nonce, plaintext, []byte(secretPurpose(subscriptionID, version, purpose)))...)
	return k.current, ciphertext, nil
}

func (k *Keyring) Decrypt(keyID string, subscriptionID uuid.UUID, version int64, ciphertext []byte) ([]byte, error) {
	return k.DecryptPurpose(keyID, subscriptionID, version, "signing-secret", ciphertext)
}

func (k *Keyring) DecryptPurpose(keyID string, subscriptionID uuid.UUID, version int64, purpose string, ciphertext []byte) ([]byte, error) {
	aead, ok := k.keys[keyID]
	if !ok || strings.TrimSpace(purpose) == "" || len(ciphertext) < aead.NonceSize() {
		return nil, ErrNotFound
	}
	nonce, payload := ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, payload, []byte(secretPurpose(subscriptionID, version, purpose)))
	if err != nil {
		return nil, ErrNotFound
	}
	return plaintext, nil
}

func secretPurpose(subscriptionID uuid.UUID, version int64, purpose string) string {
	if purpose == "signing-secret" {
		return fmt.Sprintf("webhook-subscription:%s:v%d", subscriptionID, version)
	}
	return fmt.Sprintf("webhook-subscription:%s:%s:v%d", subscriptionID, purpose, version)
}
