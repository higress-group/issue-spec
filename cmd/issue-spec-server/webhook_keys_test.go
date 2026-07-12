package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestWebhookKeysValidation(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: `{"current":"a","keys":{"a":"` + key + `"},"extra":true}`},
		{name: "current missing", raw: `{"current":"b","keys":{"a":"` + key + `"}}`},
		{name: "duplicate normalized", raw: `{"current":"a","keys":{"a":"` + key + `"," a ":"` + key + `"}}`},
		{name: "short", raw: `{"current":"a","keys":{"a":"` + base64.StdEncoding.EncodeToString([]byte("short")) + `"}}`},
		{name: "unsafe id", raw: `{"current":"bad/id","keys":{"bad/id":"` + key + `"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := webhookKeys([]byte(test.raw), nil); err == nil {
				t.Fatal("invalid keyring was accepted")
			}
		})
	}
	current, keys, err := webhookKeys([]byte(`{"current":"key-2026.07","keys":{"key-2026.07":"`+key+`"}}`), nil)
	if err != nil || current != "key-2026.07" || len(keys[current]) != 32 {
		t.Fatalf("valid keyring current=%q keys=%v err=%v", current, keys, err)
	}
}
