package codereview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// OperatorProvidersFileEnv points at trusted process configuration.  It is
	// intentionally separate from repository workflow configuration: a checked
	// out repository may select a registered key, but can never supply the
	// executable, arguments, environment, or credentials behind that key.
	OperatorProvidersFileEnv = "ISSUE_SPEC_CODE_PROVIDERS_FILE"
	maximumOperatorConfig    = int64(1 << 20)
	maximumOperatorProviders = 32
)

type operatorConfigFile struct {
	Version   int                              `json:"version"`
	Providers map[string]operatorCommandConfig `json:"providers"`
}

type operatorCommandConfig struct {
	Path        string              `json:"path"`
	Args        []string            `json:"args,omitempty"`
	Environment []string            `json:"environment,omitempty"`
	Timeout     string              `json:"timeout,omitempty"`
	MaxOutput   int64               `json:"max_output_bytes,omitempty"`
	Description ProviderDescription `json:"description,omitempty"`
}

// LoadOperatorRegistryFromEnvironment constructs the immutable provider
// registry used by a CLI process. An unset environment variable means no
// mutation provider is installed. A configured but malformed file is returned
// as an error and must fail the selected self-hosted operation closed.
func LoadOperatorRegistryFromEnvironment() (Registry, error) {
	registry, _, err := LoadOperatorRegistry("")
	return registry, err
}

// LoadOperatorRegistry resolves trusted process configuration before the
// selected profile reference. Repository content is never consulted.
func LoadOperatorRegistry(profileReference string) (Registry, string, error) {
	path := strings.TrimSpace(os.Getenv(OperatorProvidersFileEnv))
	source := "env:" + OperatorProvidersFileEnv
	if path == "" {
		path = strings.TrimSpace(profileReference)
		source = "profile"
	}
	if path == "" {
		registry, err := NewRegistry(nil)
		return registry, "none", err
	}
	raw, err := readPrivateOperatorConfig(path)
	if err != nil {
		return Registry{}, source, err
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return Registry{}, source, fmt.Errorf("read %s: %w", OperatorProvidersFileEnv, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config operatorConfigFile
	if err := decoder.Decode(&config); err != nil {
		return Registry{}, source, fmt.Errorf("read operator provider configuration: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Registry{}, source, fmt.Errorf("read operator provider configuration: %w", err)
	}
	if config.Version != 1 {
		return Registry{}, source, fmt.Errorf("read operator provider configuration: unsupported version %d", config.Version)
	}
	if len(config.Providers) == 0 || len(config.Providers) > maximumOperatorProviders {
		return Registry{}, source, fmt.Errorf("read operator provider configuration: providers must contain between 1 and %d registrations", maximumOperatorProviders)
	}
	keys := make([]string, 0, len(config.Providers))
	for key := range config.Providers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	registrations := make([]Registration, 0, len(keys))
	for _, key := range keys {
		entry := config.Providers[key]
		var timeout time.Duration
		if strings.TrimSpace(entry.Timeout) != "" {
			timeout, err = time.ParseDuration(entry.Timeout)
			if err != nil {
				return Registry{}, source, fmt.Errorf("read %s: provider %q has invalid timeout", OperatorProvidersFileEnv, key)
			}
		}
		provider, err := NewCommandProvider(CommandConfig{Path: entry.Path, Args: entry.Args,
			Environment: entry.Environment, Timeout: timeout, MaxOutput: entry.MaxOutput})
		if err != nil {
			return Registry{}, source, fmt.Errorf("read %s: provider %q: %w", OperatorProvidersFileEnv, key, err)
		}
		registrations = append(registrations, Registration{Key: key, Provider: provider, Description: entry.Description})
	}
	registry, err := NewRegistry(registrations)
	return registry, source, err
}

func readPrivateOperatorConfig(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("%s must name a clean absolute private file", OperatorProvidersFileEnv)
	}
	before, err := os.Lstat(path)
	if err != nil || !privateOperatorFile(before) {
		return nil, fmt.Errorf("read %s: operator provider file is not a private regular file", OperatorProvidersFileEnv)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: operator provider file is unavailable", OperatorProvidersFileEnv)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !privateOperatorFile(after) || !os.SameFile(before, after) {
		return nil, fmt.Errorf("read %s: operator provider file changed while opening", OperatorProvidersFileEnv)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximumOperatorConfig+1))
	if err != nil || int64(len(raw)) > maximumOperatorConfig {
		return nil, fmt.Errorf("read %s: operator provider file exceeds 1 MiB", OperatorProvidersFileEnv)
	}
	return raw, nil
}

func privateOperatorFile(info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular() &&
		info.Size() <= maximumOperatorConfig && operatorFileIsPrivate(info)
}

// ResolveMutationProvider returns only providers that implement the mutation
// half of the neutral contract. The context parameter lets the immutable
// registry be installed directly as the app dependency without an adapter.
func (r Registry) ResolveMutationProvider(_ context.Context, key string) (MutationProvider, error) {
	provider, err := r.Lookup(key)
	if err != nil {
		return nil, err
	}
	mutation, ok := provider.(MutationProvider)
	if !ok {
		return nil, fmt.Errorf("%w: %s does not implement mutations", ErrCapabilityMissing, strings.TrimSpace(key))
	}
	return mutation, nil
}

func (r Registry) ResolveMergeAuthorityProvider(_ context.Context, key string) (MergeAuthorityProvider, error) {
	provider, err := r.Lookup(key)
	if err != nil {
		return nil, err
	}
	authority, ok := provider.(MergeAuthorityProvider)
	if !ok {
		return nil, fmt.Errorf("%w: %s does not implement merge authority", ErrCapabilityMissing, strings.TrimSpace(key))
	}
	return authority, nil
}
