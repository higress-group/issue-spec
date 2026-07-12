package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretReferencesRequirePrivateFilesAndEraseEnvironment(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "secret")
	if err := os.WriteFile(private, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := (SecretReference{File: private}).Load()
	if err != nil || string(value) != "file-secret" {
		t.Fatalf("private file value=%q err=%v", value, err)
	}
	public := filepath.Join(dir, "public")
	if err := os.WriteFile(public, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (SecretReference{File: public}).Load(); err == nil {
		t.Fatal("group/world-readable secret file accepted")
	}
	symlink := filepath.Join(dir, "link")
	if err := os.Symlink(private, symlink); err == nil {
		if _, err := (SecretReference{File: symlink}).Load(); err == nil {
			t.Fatal("secret symlink accepted")
		}
	}
	t.Setenv("RUNNER_WEBHOOK_SECRET_TEST", "environment-secret")
	value, err = (SecretReference{Env: "RUNNER_WEBHOOK_SECRET_TEST"}).Load()
	if err != nil || string(value) != "environment-secret" {
		t.Fatalf("environment value=%q err=%v", value, err)
	}
	if _, exists := os.LookupEnv("RUNNER_WEBHOOK_SECRET_TEST"); exists {
		t.Fatal("webhook secret remained in process environment")
	}
	oversized := filepath.Join(dir, "oversized")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", (64<<10)+2)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (SecretReference{File: oversized}).Load(); err == nil {
		t.Fatal("oversized secret file accepted")
	}
	t.Setenv("RUNNER_OVERSIZED_SECRET_TEST", strings.Repeat("x", (64<<10)+1))
	if _, err := (SecretReference{Env: "RUNNER_OVERSIZED_SECRET_TEST"}).Load(); err == nil {
		t.Fatal("oversized secret environment value accepted")
	}
	if _, exists := os.LookupEnv("RUNNER_OVERSIZED_SECRET_TEST"); exists {
		t.Fatal("oversized secret remained in process environment")
	}
	oversizedPEM := filepath.Join(dir, "oversized.pem")
	if err := os.WriteFile(oversizedPEM, []byte(strings.Repeat("p", (1<<20)+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readLimitedFile(oversizedPEM, 1<<20); err == nil {
		t.Fatal("oversized TLS certificate input accepted")
	}
}
