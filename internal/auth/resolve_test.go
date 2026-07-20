package auth

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

// envMapLookup builds a hermetic EnvLookup from a map so tests never read the
// host account. A missing key reports (", false) exactly like os.LookupEnv.
func envMapLookup(env map[string]string) EnvLookup {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

func TestResolveConfigDirOrderAcrossPlatforms(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		env     map[string]string
		want    string
		wantErr error
	}{
		{
			name: "override wins over xdg and home",
			goos: "linux",
			env:  map[string]string{ConfigDirEnv: "/explicit/config", "XDG_CONFIG_HOME": "/xdg", "HOME": "/home/u"},
			want: "/explicit/config",
		},
		{
			name: "override trimmed",
			goos: "linux",
			env:  map[string]string{ConfigDirEnv: "  /explicit/config  ", "HOME": "/home/u"},
			want: "/explicit/config",
		},
		{
			name: "empty override falls through to xdg",
			goos: "linux",
			env:  map[string]string{ConfigDirEnv: "   ", "XDG_CONFIG_HOME": "/xdg", "HOME": "/home/u"},
			want: filepath.Join("/xdg", "issue-spec"),
		},
		{
			name: "xdg honored on linux",
			goos: "linux",
			env:  map[string]string{"XDG_CONFIG_HOME": "/xdg", "HOME": "/home/u"},
			want: filepath.Join("/xdg", "issue-spec"),
		},
		{
			name: "xdg honored on macOS (closes os.UserConfigDir gap)",
			goos: "darwin",
			env:  map[string]string{"XDG_CONFIG_HOME": "/xdg", "HOME": "/Users/u"},
			want: filepath.Join("/xdg", "issue-spec"),
		},
		{
			name: "relative xdg ignored, native default used",
			goos: "linux",
			env:  map[string]string{"XDG_CONFIG_HOME": "relative/dir", "HOME": "/home/u"},
			want: filepath.Join("/home/u", ".config", "issue-spec"),
		},
		{
			name: "native default linux",
			goos: "linux",
			env:  map[string]string{"HOME": "/home/u"},
			want: filepath.Join("/home/u", ".config", "issue-spec"),
		},
		{
			name: "native default macOS",
			goos: "darwin",
			env:  map[string]string{"HOME": "/Users/u"},
			want: filepath.Join("/Users/u", "Library", "Application Support", "issue-spec"),
		},
		{
			name: "native default windows uses AppData",
			goos: "windows",
			env:  map[string]string{"AppData": `C:\Users\u\AppData\Roaming`},
			want: filepath.Join(`C:\Users\u\AppData\Roaming`, "issue-spec"),
		},
		{
			name:    "home unset is distinct error",
			goos:    "linux",
			env:     map[string]string{},
			wantErr: ErrHomeUnset,
		},
		{
			name:    "home empty is invalid, not unset",
			goos:    "linux",
			env:     map[string]string{"HOME": "   "},
			wantErr: ErrHomeInvalid,
		},
		{
			name:    "home relative is invalid",
			goos:    "linux",
			env:     map[string]string{"HOME": "relative/home"},
			wantErr: ErrHomeInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveConfigDirFor(envMapLookup(tt.env), tt.goos)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("resolveConfigDirFor error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveConfigDirFor error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveConfigDirFor = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveCacheDirOrderAcrossPlatforms(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		env     map[string]string
		want    string
		wantErr error
	}{
		{
			name: "override wins",
			goos: "linux",
			env:  map[string]string{CacheDirEnv: "/explicit/cache", "XDG_CACHE_HOME": "/xdgc", "HOME": "/home/u"},
			want: "/explicit/cache",
		},
		{
			name: "xdg cache honored on macOS",
			goos: "darwin",
			env:  map[string]string{"XDG_CACHE_HOME": "/xdgc", "HOME": "/Users/u"},
			want: filepath.Join("/xdgc", "issue-spec"),
		},
		{
			name: "native default linux",
			goos: "linux",
			env:  map[string]string{"HOME": "/home/u"},
			want: filepath.Join("/home/u", ".cache", "issue-spec"),
		},
		{
			name: "native default macOS",
			goos: "darwin",
			env:  map[string]string{"HOME": "/Users/u"},
			want: filepath.Join("/Users/u", "Library", "Caches", "issue-spec"),
		},
		{
			name: "native default windows uses LocalAppData",
			goos: "windows",
			env:  map[string]string{"LocalAppData": `C:\Users\u\AppData\Local`},
			want: filepath.Join(`C:\Users\u\AppData\Local`, "issue-spec"),
		},
		{
			name:    "home unset distinct",
			goos:    "linux",
			env:     map[string]string{},
			wantErr: ErrHomeUnset,
		},
		{
			name:    "home empty invalid",
			goos:    "linux",
			env:     map[string]string{"HOME": ""},
			wantErr: ErrHomeInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCacheDirFor(envMapLookup(tt.env), tt.goos)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("resolveCacheDirFor error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCacheDirFor error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveCacheDirFor = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNativeDefaultsByteForByte pins the native default formulas so a future
// refactor cannot silently move existing installs' config/cache paths when no
// override or XDG variable is set.
func TestNativeDefaultsByteForByte(t *testing.T) {
	const home = "/home/byteforbyte"
	cases := []struct {
		goos, wantConfig, wantCache string
	}{
		{"linux", filepath.Join(home, ".config", "issue-spec"), filepath.Join(home, ".cache", "issue-spec")},
		{"darwin", filepath.Join(home, "Library", "Application Support", "issue-spec"), filepath.Join(home, "Library", "Caches", "issue-spec")},
	}
	for _, c := range cases {
		env := envMapLookup(map[string]string{"HOME": home})
		gotConfig, err := resolveConfigDirFor(env, c.goos)
		if err != nil || gotConfig != c.wantConfig {
			t.Fatalf("%s config = %q, %v; want %q", c.goos, gotConfig, err, c.wantConfig)
		}
		gotCache, err := resolveCacheDirFor(env, c.goos)
		if err != nil || gotCache != c.wantCache {
			t.Fatalf("%s cache = %q, %v; want %q", c.goos, gotCache, err, c.wantCache)
		}
	}
}

// TestConfigDirDelegatesToResolver proves the exported ConfigDir uses the same
// resolution as the injectable resolver for the host platform, so the default
// os.LookupEnv wiring is covered without depending on the developer's real HOME.
func TestConfigDirDelegatesToResolver(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigDirEnv, dir)
	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir error = %v", err)
	}
	if got != dir {
		t.Fatalf("ConfigDir = %q, want override %q", got, dir)
	}

	cacheDir := t.TempDir()
	t.Setenv(CacheDirEnv, cacheDir)
	gotCache, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir error = %v", err)
	}
	if gotCache != cacheDir {
		t.Fatalf("CacheDir = %q, want override %q", gotCache, cacheDir)
	}

	// The host-platform resolver and the exported function agree when the
	// override is cleared and a hermetic HOME is injected.
	hermetic := envMapLookup(map[string]string{"HOME": "/home/hermetic"})
	if _, err := resolveConfigDirFor(hermetic, runtime.GOOS); err != nil {
		t.Fatalf("resolveConfigDirFor(host) error = %v", err)
	}
}
