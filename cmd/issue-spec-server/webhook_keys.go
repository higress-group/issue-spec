package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type webhookKeyFile struct {
	Current string            `json:"current"`
	Keys    map[string]string `json:"keys"`
}

func webhookKeys(raw, fallback []byte) (string, map[string][]byte, error) {
	if len(raw) == 0 {
		return "primary", map[string][]byte{"primary": append([]byte(nil), fallback...)}, nil
	}
	var file webhookKeyFile
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return "", nil, errors.New("webhook encryption keys: invalid JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", nil, errors.New("webhook encryption keys: multiple JSON values are forbidden")
	}
	file.Current = strings.TrimSpace(file.Current)
	if file.Current == "" || len(file.Keys) == 0 {
		return "", nil, errors.New("webhook encryption keys: current and keys are required")
	}
	keys := make(map[string][]byte, len(file.Keys))
	for id, encoded := range file.Keys {
		id = strings.TrimSpace(id)
		if !safeKeyID(id) {
			return "", nil, errors.New("webhook encryption keys: invalid key id")
		}
		if _, exists := keys[id]; exists {
			return "", nil, errors.New("webhook encryption keys: duplicate normalized key id")
		}
		material, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil || len(material) < 32 {
			return "", nil, errors.New("webhook encryption keys: keys must be base64 encoded and at least 32 bytes")
		}
		keys[id] = material
	}
	if _, exists := keys[file.Current]; !exists {
		return "", nil, errors.New("webhook encryption keys: current key id is absent")
	}
	return file.Current, keys, nil
}

func safeKeyID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}
