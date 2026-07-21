// Package config resolves civitai-manager's runtime configuration from, in
// order of precedence, command-line flags, environment variables, and a YAML
// config file under the user's XDG config directory.
//
// The CivitAI API token is a secret: it is NEVER logged. Any diagnostic
// rendering of the configuration routes the token through RedactToken.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultBaseURL is the public CivitAI API host.
	DefaultBaseURL = "https://civitai.com"
	// DefaultAddr is the web UI listen address.
	DefaultAddr = ":8787"
	// DefaultPollInterval is the per-subscription default polling cadence. It is
	// deliberately on the order of an hour: the API fronts its read routes with
	// a ~5-minute edge cache, so polling faster wastes requests without seeing
	// fresher data.
	DefaultPollInterval = time.Hour
	// MinPollInterval is the hard floor enforced on any subscription's poll
	// interval to stay a good API citizen (well above the 5-minute edge cache).
	MinPollInterval = 15 * time.Minute
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
}

// Flags carries the command-line overrides. Empty-string / nil fields mean "not
// set on the command line" and fall through to the env/file/default layers.
type Flags struct {
	Token     string
	ModelRoot string
	BaseURL   string
	Addr      string
	DBPath    string
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
		DBPath:              filepath.Join(dir, "civitai-manager.db"),
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
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.DefaultPollInterval.D() <= 0 {
		c.DefaultPollInterval = Duration(DefaultPollInterval)
	}
	return nil
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
	return fmt.Sprintf("Config{BaseURL:%s Addr:%s ModelRoot:%s DBPath:%s PollInterval:%s Token:%s}",
		r.BaseURL, r.Addr, r.ModelRoot, r.DBPath, c.DefaultPollInterval.D(), r.Token)
}
