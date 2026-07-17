package requirements

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
)

func TestActiveContextRoundTripIsPrivateNonSecretAndIdempotent(t *testing.T) {
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	context := ActiveContext{Profile: "issues", ServerInstanceID: "issue-spec:realm-a", Repository: "owner/repo", Agent: TargetCodex}
	changed, err := SaveActiveContext(context)
	if err != nil || !changed {
		t.Fatalf("first save changed=%t err=%v", changed, err)
	}
	path, err := ContextPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "api_url") {
		t.Fatalf("context contains credential or duplicated endpoint: %s", raw)
	}
	before := info.ModTime()
	time.Sleep(time.Millisecond)
	changed, err = SaveActiveContext(context)
	if err != nil || changed {
		t.Fatalf("second save changed=%t err=%v", changed, err)
	}
	info, _ = os.Stat(path)
	if !info.ModTime().Equal(before) {
		t.Fatal("idempotent save rewrote the context")
	}
	loaded, err := LoadActiveContext()
	if err != nil || loaded != context {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestLoadActiveContextRejectsPermissiveAndLinkedFiles(t *testing.T) {
	for _, test := range []struct {
		name string
		make func(t *testing.T, path string)
	}{
		{name: "permissive", make: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"profile":"issues","server_instance_id":"issue-spec:a","repository":"o/r","agent":"codex"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", make: func(t *testing.T, path string) {
			target := path + ".target"
			if err := os.WriteFile(target, []byte(`{"profile":"issues","server_instance_id":"issue-spec:a","repository":"o/r","agent":"codex"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(auth.ConfigDirEnv, dir)
			path, _ := ContextPath()
			test.make(t, path)
			if _, err := LoadActiveContext(); err == nil || !strings.Contains(err.Error(), "private regular file") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestLoadActiveContextMissing(t *testing.T) {
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	_, err := LoadActiveContext()
	if !errors.Is(err, ErrContextNotConfigured) {
		t.Fatalf("err=%v", err)
	}
}
