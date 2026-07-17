package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"strings"
)

// MailSettings is the validated, provider-neutral configuration for the
// optional outbound mail capability. Formatting never reveals any field: the
// settings may contain both an account identifier and its credential.
type MailSettings struct {
	host        string
	port        int
	username    string
	password    string
	fromAddress string
}

func (m MailSettings) Enabled() bool { return m.host != "" }

func (m MailSettings) Host() string { return m.host }

func (m MailSettings) Port() int { return m.port }

func (m MailSettings) Username() string { return m.username }

func (m MailSettings) Password() string { return m.password }

func (m MailSettings) FromAddress() string { return m.fromAddress }

func (m MailSettings) String() string {
	if !m.Enabled() {
		return "<mail disabled>"
	}
	return "<mail configured>"
}

func (m MailSettings) GoString() string { return m.String() }

func (m MailSettings) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, m.String())
}

// ParseMailSettings decodes the complete SMTP secret file. Errors identify
// fields only and intentionally omit submitted values.
func ParseMailSettings(secret SecretFile) (MailSettings, error) {
	if secret.IsZero() {
		return MailSettings{}, nil
	}
	var payload struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
		From     string `json:"from_address"`
	}
	decoder := json.NewDecoder(bytes.NewReader(secret.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return MailSettings{}, errors.New("SMTP_CONFIG_FILE must contain one valid JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return MailSettings{}, errors.New("SMTP_CONFIG_FILE must contain one valid JSON object")
	}
	host := strings.ToLower(strings.TrimSpace(payload.Host))
	if !validMailHost(host) {
		return MailSettings{}, errors.New("SMTP_CONFIG_FILE host is invalid")
	}
	if payload.Port < 1 || payload.Port > 65535 {
		return MailSettings{}, errors.New("SMTP_CONFIG_FILE port must be between 1 and 65535")
	}
	username := strings.TrimSpace(payload.Username)
	if !printableBounded(username, 320) {
		return MailSettings{}, errors.New("SMTP_CONFIG_FILE username is invalid")
	}
	if payload.Password == "" || len(payload.Password) > 4096 || strings.ContainsRune(payload.Password, 0) {
		return MailSettings{}, errors.New("SMTP_CONFIG_FILE password is invalid")
	}
	from := strings.TrimSpace(payload.From)
	address, err := mail.ParseAddress(from)
	if err != nil || address.Name != "" || address.Address != from || len(from) > 320 || strings.ContainsAny(from, "\r\n") {
		return MailSettings{}, errors.New("SMTP_CONFIG_FILE from_address is invalid")
	}
	return MailSettings{host: host, port: payload.Port, username: username,
		password: payload.Password, fromAddress: from}, nil
}

func validMailHost(host string) bool {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "\x00\r\n:/[] ") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func printableBounded(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char == 0x7f {
			return false
		}
	}
	return true
}

func mailSensitiveValues(raw []byte) []string {
	var payload struct {
		Host     string `json:"host"`
		Username string `json:"username"`
		Password string `json:"password"`
		From     string `json:"from_address"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	return []string{payload.Host, payload.Username, payload.Password, payload.From}
}
