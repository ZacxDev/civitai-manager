package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMain insulates the whole package from the developer's real official
// `civitai` CLI config so token resolution stays deterministic across machines.
// The official-CLI fallback test overrides this seam locally.
func TestMain(m *testing.M) {
	officialCLIConfigPath = func() (string, error) {
		return filepath.Join(os.TempDir(), "civitai-manager-test-nonexistent", "config.yaml"), nil
	}
	os.Exit(m.Run())
}

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestResolvePrecedence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, `
token: file-token
base_url: https://file.example.com
model_root: /file/models
default_poll_interval: 2h
`)

	t.Run("file only", func(t *testing.T) {
		t.Setenv(EnvToken, "")
		cfg, err := Resolve(Flags{ConfigPath: cfgPath})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Token != "file-token" {
			t.Errorf("token: got %q want file-token", cfg.Token)
		}
		if cfg.BaseURL != "https://file.example.com" {
			t.Errorf("base_url: got %q", cfg.BaseURL)
		}
		if cfg.DefaultPollInterval.D() != 2*time.Hour {
			t.Errorf("interval: got %v want 2h", cfg.DefaultPollInterval.D())
		}
	})

	t.Run("env overrides file token", func(t *testing.T) {
		t.Setenv(EnvToken, "env-token")
		cfg, err := Resolve(Flags{ConfigPath: cfgPath})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Token != "env-token" {
			t.Errorf("token: got %q want env-token", cfg.Token)
		}
	})

	t.Run("flag overrides env and file", func(t *testing.T) {
		t.Setenv(EnvToken, "env-token")
		cfg, err := Resolve(Flags{ConfigPath: cfgPath, Token: "flag-token", BaseURL: "https://flag.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Token != "flag-token" {
			t.Errorf("token: got %q want flag-token", cfg.Token)
		}
		if cfg.BaseURL != "https://flag.example.com" {
			t.Errorf("base_url: got %q want flag", cfg.BaseURL)
		}
	})
}

func TestResolveDefaults(t *testing.T) {
	// A non-existent config path is not an error; defaults fill in.
	dir := t.TempDir()
	t.Setenv(EnvToken, "")
	cfg, err := Resolve(Flags{ConfigPath: filepath.Join(dir, "missing.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Errorf("base_url default: got %q", cfg.BaseURL)
	}
	if cfg.Addr != DefaultAddr {
		t.Errorf("addr default: got %q", cfg.Addr)
	}
	if cfg.DefaultPollInterval.D() != DefaultPollInterval {
		t.Errorf("interval default: got %v", cfg.DefaultPollInterval.D())
	}
	if cfg.Token != "" {
		t.Errorf("token should be empty by default, got %q", cfg.Token)
	}
}

func TestRedactToken(t *testing.T) {
	cases := map[string]string{
		"":                 "(none)",
		"abc":              "****",
		"abcd":             "****",
		"supersecrettoken": "****oken",
	}
	for in, want := range cases {
		if got := RedactToken(in); got != want {
			t.Errorf("RedactToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigStringRedactsToken(t *testing.T) {
	cfg := &Config{Token: "supersecrettoken", BaseURL: "https://x", Addr: ":1"}
	s := cfg.String()
	if contains(s, "supersecrettoken") {
		t.Fatalf("String() leaked the token: %q", s)
	}
	if !contains(s, "****oken") {
		t.Errorf("String() should include redacted token, got %q", s)
	}
}

func TestXDGConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	dir, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/custom/xdg/civitai-manager" {
		t.Errorf("ConfigDir: got %q", dir)
	}
}

func TestDurationYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "default_poll_interval: 90m\n")
	t.Setenv(EnvToken, "")
	cfg, err := Resolve(Flags{ConfigPath: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultPollInterval.D() != 90*time.Minute {
		t.Errorf("got %v want 90m", cfg.DefaultPollInterval.D())
	}
}

func TestDefaultAddrIsLoopback(t *testing.T) {
	// The UI must not be network-exposed out of the box (finding #4).
	if !contains(DefaultAddr, "127.0.0.1") {
		t.Errorf("DefaultAddr should bind loopback by default, got %q", DefaultAddr)
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"1024", 1024, false},
		{"1kb", 1 << 10, false},
		{"500MB", 500 << 20, false},
		{"2G", 2 << 30, false},
		{"1.5MB", int64(1.5 * float64(1<<20)), false},
		{"nonsense", 0, true},
		{"-5", 0, true},
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseSize(%q): expected error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSize(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMaxFileSizeResolves(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvToken, "")
	cfg, err := Resolve(Flags{ConfigPath: filepath.Join(dir, "missing.yaml"), MaxFileSize: "500MB"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxFileSizeBytes != 500<<20 {
		t.Errorf("MaxFileSizeBytes = %d, want %d", cfg.MaxFileSizeBytes, int64(500<<20))
	}
}

func TestLibraryConfigResolves(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, `
model_root: /file/models
library_paths:
  - /a1111/models/Lora
  - /comfy/models/checkpoints
trash_dir: /file/models/.trash
library_extensions:
  - .safetensors
  - .gguf
`)
	t.Setenv(EnvToken, "")
	cfg, err := Resolve(Flags{ConfigPath: cfgPath, LibraryPaths: []string{"/extra/path"}})
	if err != nil {
		t.Fatal(err)
	}
	// File paths plus the command-line --path are combined, in that order.
	if len(cfg.LibraryPaths) != 3 || cfg.LibraryPaths[2] != "/extra/path" {
		t.Fatalf("LibraryPaths = %v, want file paths + /extra/path", cfg.LibraryPaths)
	}
	if cfg.TrashDir != "/file/models/.trash" {
		t.Errorf("TrashDir = %q", cfg.TrashDir)
	}
	if len(cfg.LibraryExtensions) != 2 || cfg.LibraryExtensions[1] != ".gguf" {
		t.Errorf("LibraryExtensions = %v", cfg.LibraryExtensions)
	}
}

func TestLibraryPathsExpandHome(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "library_paths:\n  - ~/models\ntrash_dir: ~/trash\n")
	t.Setenv(EnvToken, "")
	cfg, err := Resolve(Flags{ConfigPath: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if cfg.LibraryPaths[0] != filepath.Join(home, "models") {
		t.Errorf("LibraryPaths[0] = %q, want expanded ~", cfg.LibraryPaths[0])
	}
	if cfg.TrashDir != filepath.Join(home, "trash") {
		t.Errorf("TrashDir = %q, want expanded ~", cfg.TrashDir)
	}
}

func TestDownloadJitterConfig(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	t.Setenv(EnvToken, "")

	// Default.
	cfg, err := Resolve(Flags{ConfigPath: missing})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DownloadJitter.D() != DefaultDownloadJitter {
		t.Errorf("download jitter default = %v, want %v", cfg.DownloadJitter.D(), DefaultDownloadJitter)
	}

	// Flag override, including an explicit 0 (disabled).
	for _, c := range []struct {
		flag string
		want time.Duration
	}{
		{"0", 0},
		{"5m", 5 * time.Minute},
	} {
		cfg, err := Resolve(Flags{ConfigPath: missing, DownloadJitter: c.flag})
		if err != nil {
			t.Fatalf("resolve --download-jitter=%q: %v", c.flag, err)
		}
		if cfg.DownloadJitter.D() != c.want {
			t.Errorf("--download-jitter=%q → %v, want %v", c.flag, cfg.DownloadJitter.D(), c.want)
		}
	}

	// Invalid duration is a hard error.
	if _, err := Resolve(Flags{ConfigPath: missing, DownloadJitter: "notaduration"}); err == nil {
		t.Error("invalid --download-jitter should error")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestResolveOfficialCLITokenFallback proves finding #5: when nothing higher in
// the precedence chain provides a token, resolution borrows the token from the
// official `civitai` CLI's config; an absent file is not an error; and any
// higher-precedence source (env, this app's own config, a flag) wins over it.
func TestResolveOfficialCLITokenFallback(t *testing.T) {
	dir := t.TempDir()
	officialPath := filepath.Join(dir, "civitai", "config.yaml")

	old := officialCLIConfigPath
	officialCLIConfigPath = func() (string, error) { return officialPath, nil }
	defer func() { officialCLIConfigPath = old }()

	missingOwnCfg := filepath.Join(dir, "cm-missing.yaml")

	t.Run("absent official file is ignored", func(t *testing.T) {
		t.Setenv(EnvToken, "")
		cfg, err := Resolve(Flags{ConfigPath: missingOwnCfg})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if cfg.Token != "" {
			t.Errorf("token should be empty when official file is absent, got %q", cfg.Token)
		}
	})

	// Now create the official CLI config with a token.
	if err := os.MkdirAll(filepath.Dir(officialPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(officialPath, []byte("token: official-cli-token\nbase_url: https://civitai.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("fallback used when nothing higher set", func(t *testing.T) {
		t.Setenv(EnvToken, "")
		cfg, err := Resolve(Flags{ConfigPath: missingOwnCfg})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if cfg.Token != "official-cli-token" {
			t.Errorf("token: got %q want official-cli-token", cfg.Token)
		}
	})

	t.Run("this app's own config wins over the fallback", func(t *testing.T) {
		t.Setenv(EnvToken, "")
		ownCfg := writeConfig(t, t.TempDir(), "token: own-config-token\n")
		cfg, err := Resolve(Flags{ConfigPath: ownCfg})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if cfg.Token != "own-config-token" {
			t.Errorf("own config should win, got %q", cfg.Token)
		}
	})

	t.Run("env wins over the fallback", func(t *testing.T) {
		t.Setenv(EnvToken, "env-token")
		cfg, err := Resolve(Flags{ConfigPath: missingOwnCfg})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if cfg.Token != "env-token" {
			t.Errorf("env should win over fallback, got %q", cfg.Token)
		}
	})

	t.Run("flag wins over the fallback", func(t *testing.T) {
		t.Setenv(EnvToken, "")
		cfg, err := Resolve(Flags{ConfigPath: missingOwnCfg, Token: "flag-token"})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if cfg.Token != "flag-token" {
			t.Errorf("flag should win over fallback, got %q", cfg.Token)
		}
	})

	t.Run("malformed official file is ignored", func(t *testing.T) {
		t.Setenv(EnvToken, "")
		if err := os.WriteFile(officialPath, []byte("::: not yaml :::\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Resolve(Flags{ConfigPath: missingOwnCfg})
		if err != nil {
			t.Fatalf("resolve must not fail on a malformed official file: %v", err)
		}
		if cfg.Token != "" {
			t.Errorf("malformed official file should yield no token, got %q", cfg.Token)
		}
	})
}

// TestOfficialCLITokenSizeCap proves the bounded read: an oversized file at the
// official-CLI seam path (a symlink to something huge, or a bogus binary blob) is
// skipped and yields "" with no error/panic, while a normal small config still
// yields its token.
func TestOfficialCLITokenSizeCap(t *testing.T) {
	dir := t.TempDir()
	officialPath := filepath.Join(dir, "civitai", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(officialPath), 0o700); err != nil {
		t.Fatal(err)
	}

	old := officialCLIConfigPath
	officialCLIConfigPath = func() (string, error) { return officialPath, nil }
	defer func() { officialCLIConfigPath = old }()

	t.Run("oversized file is skipped", func(t *testing.T) {
		// A valid-looking token line, but the file is padded past the 1 MiB cap so
		// the reader must refuse it.
		blob := append([]byte("token: should-not-be-read\n"),
			make([]byte, maxOfficialCLIConfigBytes+1)...)
		if err := os.WriteFile(officialPath, blob, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := officialCLIToken(); got != "" {
			t.Errorf("oversized official config must yield \"\", got %q", got)
		}
	})

	t.Run("normal small config yields the token", func(t *testing.T) {
		if err := os.WriteFile(officialPath, []byte("token: small-token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := officialCLIToken(); got != "small-token" {
			t.Errorf("small config token = %q, want small-token", got)
		}
	})
}
