package codereview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOperatorRegistryConstructsCommandMutationProvider(t *testing.T) {
	path := writeOperatorConfig(t, `{
  "version": 1,
  "providers": {
    "code.example": {
      "path": `+quotedJSON(os.Args[0])+`,
      "args": ["-test.run=^TestCommandProviderHelper$"],
      "environment": ["ISSUE_SPEC_PROVIDER_HELPER=1", "ISSUE_SPEC_PROVIDER_MODE=normal"],
      "timeout": "10s",
      "max_output_bytes": 1048576
    }
  }
}`)
	t.Setenv(OperatorProvidersFileEnv, path)
	registry, err := LoadOperatorRegistryFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := registry.ResolveMutationProvider(t.Context(), "code.example")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := provider.Capabilities(t.Context())
	if err != nil || !capabilities.Has(CapabilityChangeComment) || !capabilities.Has(CapabilityChangeCreate) {
		t.Fatalf("capabilities = %+v err=%v", capabilities, err)
	}
}

func TestLoadOperatorRegistryFailsClosedForUnsafeOrMalformedConfig(t *testing.T) {
	validProvider := `"providers":{"code.example":{"path":` + quotedJSON(os.Args[0]) + `}}`
	for _, test := range []struct {
		name string
		raw  string
		mode os.FileMode
		want string
	}{
		{name: "unknown field", raw: `{"version":1,` + validProvider + `,"repository_command":"/tmp/x"}`, mode: 0o600, want: "unknown field"},
		{name: "duplicate key", raw: `{"version":1,"version":1,` + validProvider + `}`, mode: 0o600, want: "duplicate JSON field"},
		{name: "unsupported version", raw: `{"version":2,` + validProvider + `}`, mode: 0o600, want: "unsupported"},
		{name: "public permissions", raw: `{"version":1,` + validProvider + `}`, mode: 0o644, want: "private regular file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "providers.json")
			if err := os.WriteFile(path, []byte(test.raw), test.mode); err != nil {
				t.Fatal(err)
			}
			t.Setenv(OperatorProvidersFileEnv, path)
			if _, err := LoadOperatorRegistryFromEnvironment(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadOperatorRegistryUnsetHasNoProviders(t *testing.T) {
	t.Setenv(OperatorProvidersFileEnv, "")
	registry, err := LoadOperatorRegistryFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ResolveMutationProvider(t.Context(), "code.example"); err == nil ||
		!strings.Contains(err.Error(), ErrProviderNotFound.Error()) {
		t.Fatalf("error = %v", err)
	}
}

func writeOperatorConfig(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func quotedJSON(value string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}
