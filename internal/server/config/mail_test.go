package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseMailSettingsOptionalAndRedacted(t *testing.T) {
	disabled, err := ParseMailSettings(SecretFile{})
	if err != nil || disabled.Enabled() {
		t.Fatalf("disabled settings = %v, %v", disabled, err)
	}
	const credential = "example-credential-value"
	settings, err := ParseMailSettings(SecretFile{path: "/run/secrets/mail", value: []byte(`{
		"host":"mail.example.test","port":2465,"username":"mailer@example.test",
		"password":"` + credential + `","from_address":"notices@example.test"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled() || settings.Host() != "mail.example.test" || settings.Port() != 2465 ||
		settings.Username() != "mailer@example.test" || settings.Password() != credential ||
		settings.FromAddress() != "notices@example.test" {
		t.Fatalf("settings were not parsed: %v", settings)
	}
	for _, rendered := range []string{settings.String(), fmt.Sprintf("%v", settings), fmt.Sprintf("%+v", settings), fmt.Sprintf("%#v", settings)} {
		if strings.Contains(rendered, credential) || strings.Contains(rendered, "mailer@example.test") {
			t.Fatalf("settings formatting leaked secret material: %q", rendered)
		}
	}
}

func TestParseMailSettingsRejectsUnsafeInputsWithoutEchoingThem(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		field   string
	}{
		{name: "unknown field", payload: `{"host":"mail.example.test","port":2465,"username":"mailer","password":"secret","from_address":"notices@example.test","mode":"starttls"}`, field: "valid JSON object"},
		{name: "host", payload: `{"host":"mail.example.test:2465","port":2465,"username":"mailer","password":"secret","from_address":"notices@example.test"}`, field: "host"},
		{name: "port", payload: `{"host":"mail.example.test","port":0,"username":"mailer","password":"secret","from_address":"notices@example.test"}`, field: "port"},
		{name: "username", payload: `{"host":"mail.example.test","port":2465,"username":"bad user","password":"secret","from_address":"notices@example.test"}`, field: "username"},
		{name: "from", payload: `{"host":"mail.example.test","port":2465,"username":"mailer","password":"secret","from_address":"Name <notices@example.test>"}`, field: "from_address"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseMailSettings(SecretFile{path: "/run/secrets/mail", value: []byte(test.payload)})
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("error = %v, want field %q", err, test.field)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "notices@example.test") {
				t.Fatalf("validation error echoed configured values: %v", err)
			}
		})
	}
}

func TestLoadMailSecretAndRedactCompletePayload(t *testing.T) {
	resetEnv(t)
	for name, value := range baseEnv("", "") {
		if name != "" {
			t.Setenv(name, value)
		}
	}
	payload, err := json.Marshal(map[string]any{
		"host": "mail.example.test", "port": 2465, "username": "mailer@example.test",
		"password": "example-credential-value", "from_address": "notices@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := writeSecret(t, string(payload), 0o600)
	t.Setenv(SMTPConfigFileEnv, path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := ParseMailSettings(cfg.SMTPConfig)
	if err != nil || !settings.Enabled() {
		t.Fatalf("mail settings = %v, %v", settings, err)
	}
	redacted := cfg.RedactError(errors.New("relay mail.example.test rejected mailer@example.test example-credential-value notices@example.test"))
	if strings.Contains(redacted.Error(), "example-credential-value") || strings.Contains(redacted.Error(), "mailer@example.test") ||
		strings.Contains(redacted.Error(), "mail.example.test") || !strings.Contains(redacted.Error(), "[REDACTED]") {
		t.Fatalf("redacted error = %q", redacted)
	}
}
