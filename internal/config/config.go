// Package config resolves civitai-manager's runtime configuration from, in
// order of precedence, command-line flags, environment variables, and a YAML
// config file under the user's XDG config directory.
//
// The CivitAI API token is a secret: it is NEVER logged. Any diagnostic
// rendering of the configuration routes the token through RedactToken.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultBaseURL is the public CivitAI API host.
	DefaultBaseURL = "https://civitai.com"
	// DefaultAddr is the web UI listen address. It binds to loopback by default
	// so the UI is not network-exposed out of the box; override with --addr (or
	// the addr config key) to listen on a LAN interface.
	DefaultAddr = "127.0.0.1:8787"
	// DefaultDownloadJitter is the default anti-stampede window for auto-detected
	// downloads: each install schedules a new-version download at a random point
	// in [0, window) so a fleet does not begin the same download in unison.
	DefaultDownloadJitter = 15 * time.Minute
	// DefaultPollInterval is the per-subscription default polling cadence. It is
	// deliberately on the order of an hour: the API fronts its read routes with
	// a ~5-minute edge cache, so polling faster wastes requests without seeing
	// fresher data.
	DefaultPollInterval = time.Hour
	// MinPollInterval is the hard floor enforced on any subscription's poll
	// interval to stay a good API citizen (well above the 5-minute edge cache).
	MinPollInterval = 15 * time.Minute
	// DefaultWebScanTimeout bounds a web-triggered library scan: the "Scan now"
	// button walks/hashes host directories, so a deadline keeps a large (or
	// adversarial) path from tying up the server indefinitely. The CLI scan is
	// unbounded (the operator typed the path knowingly).
	DefaultWebScanTimeout = 2 * time.Minute
	// DefaultWebScanMaxFiles caps how many model-extension files a web-triggered
	// scan will walk before aborting with "scan too large; narrow the path". It
	// bounds the arbitrary-path walk primitive the web endpoint exposes.
	DefaultWebScanMaxFiles = 50000
	// EnvToken is the environment variable holding the API token.
	EnvToken = "CIVITAI_TOKEN"

	appDir = "civitai-manager"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	// Token is the CivitAI personal API key. It is optional: the public read
	// endpoints work anonymously, but a token is required to download most
	// files. NEVER log this value; use RedactToken for any diagnostic output.
	Token string `yaml:"token"`
	// ModelRoot is the root directory under which downloaded files are laid out.
	ModelRoot string `yaml:"model_root"`
	// BaseURL is the CivitAI API base URL.
	BaseURL string `yaml:"base_url"`
	// DefaultPollInterval is the fallback poll cadence for subscriptions that do
	// not specify their own.
	DefaultPollInterval Duration `yaml:"default_poll_interval"`
	// Addr is the web UI listen address (host:port).
	Addr string `yaml:"addr"`
	// DBPath is the SQLite database file path.
	DBPath string `yaml:"db_path"`
	// MaxFileSize caps the primary-file size the poller will auto-download,
	// as a human string ("500MB", "2GB") or a plain byte count. Empty / "0"
	// means unlimited. It is parsed into MaxFileSizeBytes.
	MaxFileSize string `yaml:"max_file_size"`
	// DownloadJitter is the anti-stampede window for auto-detected downloads
	// (see DefaultDownloadJitter). "0" disables it (downloads start at once).
	DownloadJitter Duration `yaml:"download_jitter"`
	// LibraryPaths are extra directories the library `scan` walks (in addition to
	// ModelRoot). Point these at an existing A1111/ComfyUI models folder to
	// inventory it without moving anything.
	LibraryPaths []string `yaml:"library_paths"`
	// TrashDir is where library quarantine moves flagged files. Empty means the
	// default, <ModelRoot>/.trash.
	TrashDir string `yaml:"trash_dir"`
	// LibraryExtensions overrides the model-weight extension set the scanner
	// recognises. Empty means the built-in default.
	LibraryExtensions []string `yaml:"library_extensions"`
	// WebScanTimeout bounds a web-triggered "Scan now". Empty/<=0 means the
	// built-in default (DefaultWebScanTimeout).
	WebScanTimeout Duration `yaml:"web_scan_timeout"`
	// WebScanMaxFiles caps the model-file count a web-triggered scan walks before
	// aborting. <=0 means the built-in default (DefaultWebScanMaxFiles).
	WebScanMaxFiles int `yaml:"web_scan_max_files"`

	// MaxFileSizeBytes is the resolved byte value of MaxFileSize (0 = unlimited).
	MaxFileSizeBytes int64 `yaml:"-"`
}

// Flags carries the command-line overrides. Empty-string / nil fields mean "not
// set on the command line" and fall through to the env/file/default layers.
type Flags struct {
	Token          string
	ModelRoot      string
	BaseURL        string
	Addr           string
	DBPath         string
	MaxFileSize    string
	DownloadJitter string
	// LibraryPaths are extra scan directories set on the command line (repeatable
	// --path). They are appended to any configured library_paths.
	LibraryPaths []string
	// TrashDir overrides the quarantine trash directory.
	TrashDir string
	// WebScanTimeout overrides the web "Scan now" deadline (a Go duration string).
	WebScanTimeout string
	// ConfigPath overrides the config-file location (default: the XDG path).
	ConfigPath string
}

// Duration is a time.Duration that (un)marshals from a Go duration string
// ("1h", "15m") in YAML, so config files stay human-readable.
type Duration time.Duration

// UnmarshalYAML parses a duration string (e.g. "1h30m").
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	if strings.TrimSpace(s) == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML renders the duration as a string.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// ConfigDir returns the directory holding the config file, honouring
// XDG_CONFIG_HOME and falling back to ~/.config.
func ConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, appDir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", appDir), nil
}

// DefaultConfigPath returns the default config-file path.
func DefaultConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// officialCLIConfigPath resolves the official `civitai` CLI's config-file
// location (~/.config/civitai/config.yaml, honouring XDG_CONFIG_HOME). It is a
// package var so tests can point it at a fixture and never read the developer's
// real config.
var officialCLIConfigPath = func() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "civitai", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "civitai", "config.yaml"), nil
}

// maxOfficialCLIConfigBytes caps the official-CLI config file read. A real config
// is a few hundred bytes; a larger file (or a symlink pointing at something huge)
// is almost certainly not a config, so we refuse to slurp it into memory.
const maxOfficialCLIConfigBytes = 1 << 20 // 1 MiB

// officialCLIToken best-effort reads the `token:` field from the official
// `civitai` CLI's config file. Any problem (missing file, unreadable, oversized,
// malformed YAML, absent field) yields "" with no error, so this can never break
// token resolution — it is only ever a last-resort fallback.
func officialCLIToken() string {
	path, err := officialCLIConfigPath()
	if err != nil {
		return ""
	}
	// Bound the read: stat first and skip anything implausibly large for a config
	// (guards against a symlink to a huge/binary file at the seam path).
	if fi, err := os.Stat(path); err != nil || fi.IsDir() || fi.Size() > maxOfficialCLIConfigBytes {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var body struct {
		Token string `yaml:"token"`
	}
	if err := yaml.Unmarshal(data, &body); err != nil {
		return ""
	}
	return strings.TrimSpace(body.Token)
}

// defaults returns a Config populated with built-in defaults.
func defaults() (*Config, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	return &Config{
		BaseURL:             DefaultBaseURL,
		Addr:                DefaultAddr,
		ModelRoot:           filepath.Join(home, "civitai-models"),
		DefaultPollInterval: Duration(DefaultPollInterval),
		DownloadJitter:      Duration(DefaultDownloadJitter),
		DBPath:              filepath.Join(dir, "civitai-manager.db"),
		WebScanTimeout:      Duration(DefaultWebScanTimeout),
		WebScanMaxFiles:     DefaultWebScanMaxFiles,
	}, nil
}

// loadFile reads and parses the YAML config at path into cfg (overlaying only
// the fields the file sets). A missing file is not an error.
func loadFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return nil
}

// Resolve applies the precedence chain (flags > env > file > defaults) and
// returns the effective configuration.
//
// The token specifically resolves flag > env (CIVITAI_TOKEN) > file, matching
// the documented precedence; other fields resolve flag > file > default.
func Resolve(flags Flags) (*Config, error) {
	cfg, err := defaults()
	if err != nil {
		return nil, err
	}

	path := flags.ConfigPath
	if path == "" {
		if path, err = DefaultConfigPath(); err != nil {
			return nil, err
		}
	}
	if err := loadFile(cfg, path); err != nil {
		return nil, err
	}

	// Env layer: only the token is env-configurable.
	if env := os.Getenv(EnvToken); env != "" {
		cfg.Token = env
	}

	// Flag layer (highest precedence).
	if flags.Token != "" {
		cfg.Token = flags.Token
	}

	// Lowest-precedence fallback: if nothing above (flag / env / this app's own
	// config file) provided a token, borrow the one from the official `civitai`
	// CLI's config. Best-effort — a missing or malformed file is ignored so it
	// can never break resolution.
	if cfg.Token == "" {
		cfg.Token = officialCLIToken()
	}
	if flags.ModelRoot != "" {
		cfg.ModelRoot = flags.ModelRoot
	}
	if flags.BaseURL != "" {
		cfg.BaseURL = flags.BaseURL
	}
	if flags.Addr != "" {
		cfg.Addr = flags.Addr
	}
	if flags.DBPath != "" {
		cfg.DBPath = flags.DBPath
	}
	if flags.MaxFileSize != "" {
		cfg.MaxFileSize = flags.MaxFileSize
	}
	if flags.DownloadJitter != "" {
		d, err := time.ParseDuration(flags.DownloadJitter)
		if err != nil {
			return nil, fmt.Errorf("invalid --download-jitter %q: %w", flags.DownloadJitter, err)
		}
		cfg.DownloadJitter = Duration(d)
	}
	if len(flags.LibraryPaths) > 0 {
		cfg.LibraryPaths = append(cfg.LibraryPaths, flags.LibraryPaths...)
	}
	if flags.TrashDir != "" {
		cfg.TrashDir = flags.TrashDir
	}
	if flags.WebScanTimeout != "" {
		d, err := time.ParseDuration(flags.WebScanTimeout)
		if err != nil {
			return nil, fmt.Errorf("invalid --web-scan-timeout %q: %w", flags.WebScanTimeout, err)
		}
		cfg.WebScanTimeout = Duration(d)
	}

	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// normalize expands ~ in path fields and validates required values.
func (c *Config) normalize() error {
	var err error
	if c.ModelRoot, err = expandHome(c.ModelRoot); err != nil {
		return err
	}
	if c.DBPath, err = expandHome(c.DBPath); err != nil {
		return err
	}
	if c.TrashDir != "" {
		if c.TrashDir, err = expandHome(c.TrashDir); err != nil {
			return err
		}
	}
	for i, p := range c.LibraryPaths {
		if c.LibraryPaths[i], err = expandHome(p); err != nil {
			return err
		}
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.DefaultPollInterval.D() <= 0 {
		c.DefaultPollInterval = Duration(DefaultPollInterval)
	}
	// A negative download-jitter is meaningless; clamp to 0 (disabled). Zero is
	// a valid, explicit "start immediately" and is preserved.
	if c.DownloadJitter.D() < 0 {
		c.DownloadJitter = 0
	}
	// A non-positive web-scan budget is meaningless; fall back to the safe
	// built-in bounds so the arbitrary-path walk primitive is always capped.
	if c.WebScanTimeout.D() <= 0 {
		c.WebScanTimeout = Duration(DefaultWebScanTimeout)
	}
	if c.WebScanMaxFiles <= 0 {
		c.WebScanMaxFiles = DefaultWebScanMaxFiles
	}
	bytes, err := ParseSize(c.MaxFileSize)
	if err != nil {
		return fmt.Errorf("invalid max_file_size %q: %w", c.MaxFileSize, err)
	}
	c.MaxFileSizeBytes = bytes
	return nil
}

// IsLoopbackAddr reports whether a listen address (host:port, a bare host, or a
// bare :port) binds ONLY the loopback interface. It is the gate the web server
// uses to decide whether the arbitrary extra-scan-path capability is safe to
// expose: only a loopback bind is single-user-local.
//
// The default is deliberately SAFE — anything it cannot positively prove is
// loopback (an empty host, a bare :port that binds every interface, 0.0.0.0/::,
// or an unresolved hostname) is treated as non-loopback.
func IsLoopbackAddr(addr string) bool {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return false
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false // a bare ":port" binds all interfaces
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false // an unresolved hostname is not provably loopback
}

// ParseSize parses a human file-size string into a byte count. It accepts a
// plain integer ("1048576"), or a number with a binary-unit suffix (case
// -insensitive, base 1024): B, K/KB, M/MB, G/GB, T/TB. An empty string or "0"
// yields 0 (meaning "unlimited" to callers). Negative values are rejected.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	upper := strings.ToUpper(s)
	mult := int64(1)
	switch {
	case strings.HasSuffix(upper, "TB"):
		mult, upper = 1<<40, strings.TrimSuffix(upper, "TB")
	case strings.HasSuffix(upper, "GB"):
		mult, upper = 1<<30, strings.TrimSuffix(upper, "GB")
	case strings.HasSuffix(upper, "MB"):
		mult, upper = 1<<20, strings.TrimSuffix(upper, "MB")
	case strings.HasSuffix(upper, "KB"):
		mult, upper = 1<<10, strings.TrimSuffix(upper, "KB")
	case strings.HasSuffix(upper, "T"):
		mult, upper = 1<<40, strings.TrimSuffix(upper, "T")
	case strings.HasSuffix(upper, "G"):
		mult, upper = 1<<30, strings.TrimSuffix(upper, "G")
	case strings.HasSuffix(upper, "M"):
		mult, upper = 1<<20, strings.TrimSuffix(upper, "M")
	case strings.HasSuffix(upper, "K"):
		mult, upper = 1<<10, strings.TrimSuffix(upper, "K")
	case strings.HasSuffix(upper, "B"):
		mult, upper = 1, strings.TrimSuffix(upper, "B")
	}
	upper = strings.TrimSpace(upper)
	n, err := strconv.ParseFloat(upper, 64)
	if err != nil {
		return 0, fmt.Errorf("not a valid size (expected e.g. 500MB, 2GB, or a byte count)")
	}
	if n < 0 {
		return 0, fmt.Errorf("size must not be negative")
	}
	return int64(n * float64(mult)), nil
}

func expandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~ in %q: %w", p, err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// RedactToken masks a secret token for safe display. An empty token renders as
// "(none)"; a non-empty one keeps only its last 4 characters, e.g. "****cdef".
// It never returns the full secret.
func RedactToken(token string) string {
	if token == "" {
		return "(none)"
	}
	if len(token) <= 4 {
		return "****"
	}
	return "****" + token[len(token)-4:]
}

// Redacted returns a shallow copy of the config with the token replaced by its
// redacted form -- safe to print in diagnostics or serialize to a log.
func (c *Config) Redacted() Config {
	dup := *c
	dup.Token = RedactToken(c.Token)
	return dup
}

// String renders the config with the token redacted.
func (c *Config) String() string {
	r := c.Redacted()
	return fmt.Sprintf("Config{BaseURL:%s Addr:%s ModelRoot:%s DBPath:%s PollInterval:%s DownloadJitter:%s MaxFileSize:%d Token:%s}",
		r.BaseURL, r.Addr, r.ModelRoot, r.DBPath, c.DefaultPollInterval.D(), c.DownloadJitter.D(), c.MaxFileSizeBytes, r.Token)
}
