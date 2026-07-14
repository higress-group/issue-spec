package gitidentity

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode"
)

const (
	maxNameBytes  = 256
	maxEmailBytes = 320
)

// Identity is an operator-selected Git commit author. Empty fields disable
// explicit identity configuration; otherwise both fields are required.
type Identity struct {
	Name  string
	Email string
}

// Normalize validates an optional commit author without consulting host or
// provider-specific identity sources.
func Normalize(name, email string) (Identity, error) {
	identity := Identity{Name: strings.TrimSpace(name), Email: strings.TrimSpace(email)}
	if (identity.Name == "") != (identity.Email == "") {
		return Identity{}, fmt.Errorf("git author name and email must be configured together")
	}
	if identity.Name == "" {
		return Identity{}, nil
	}
	if len(identity.Name) > maxNameBytes || containsControl(identity.Name) {
		return Identity{}, fmt.Errorf("git author name must be printable and at most %d bytes", maxNameBytes)
	}
	if len(identity.Email) > maxEmailBytes || containsControl(identity.Email) || strings.ContainsAny(identity.Email, " \t") || strings.Count(identity.Email, "@") != 1 {
		return Identity{}, fmt.Errorf("git author email must be a single address of at most %d bytes", maxEmailBytes)
	}
	parsed, err := mail.ParseAddress(identity.Email)
	if err != nil || parsed.Name != "" || parsed.Address != identity.Email {
		return Identity{}, fmt.Errorf("git author email must be a single address of at most %d bytes", maxEmailBytes)
	}
	return identity, nil
}

func containsControl(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}
