package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestTokenPrefixUsesDelimiterSafeHexPrefix(t *testing.T) {
	prefix, err := TokenPrefix("iss_pat_aabbccddeeff_secret_with_urlsafe_underscores", "pat")
	if err != nil || prefix != "aabbccddeeff" {
		t.Fatalf("TokenPrefix() = %q, %v", prefix, err)
	}
	for _, token := range []string{
		"iss_pat_not-hex_secret", "iss_pat_aabbccddeeff_", "iss_sess_aabbccddeeff_secret", "plaintext",
	} {
		if _, err := TokenPrefix(token, "pat"); !errors.Is(err, ErrInvalidCredential) {
			t.Fatalf("TokenPrefix(%q) error = %v", token, err)
		}
	}
}

func TestSecretsEncryptWithPurposeBinding(t *testing.T) {
	secrets, err := NewSecrets([]byte(strings.Repeat("p", 32)), []byte(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := secrets.Encrypt("oauth-pkce-verifier", []byte("verifier"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := secrets.Decrypt("oauth-pkce-verifier", ciphertext)
	if err != nil || string(plaintext) != "verifier" {
		t.Fatalf("Decrypt() = %q, %v", plaintext, err)
	}
	if _, err := secrets.Decrypt("different-purpose", ciphertext); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("purpose-mixup decrypt error = %v", err)
	}
}
