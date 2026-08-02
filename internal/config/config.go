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

	"github.com/ZacxDev/civitai-manager/internal/comfyext"
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
	//
	// 🔴 It is a RUNAWAY BACKSTOP, not the normal termination path — hence 6h,
	// not minutes. Hashing a multi-GB library on a slow/spun-down drive
	// legitimately takes hours, and the scan is meant to run until it exhausts
	// the tree, the USER stops it, or the server shuts down. The value is the
	// web scan's long-standing EFFECTIVE bound: from v0.1.13 until this constant
	// became live it was hard-coded in internal/web as `scanJobBudget = 6h`,
	// while this (then 2m) constant was plumbed all the way to the web server
	// and read by nobody. Wiring it up at the 2m it used to advertise would have
	// started killing scans that succeed today, so the default moved to the
	// truth instead. Lower it (`web_scan_timeout` / `--web-scan-timeout`) if you
	// want a runaway "Scan now" capped sooner.
	DefaultWebScanTimeout = 6 * time.Hour
	// DefaultWebScanMaxFiles caps how many model-extension files a web-triggered
	// scan will walk before aborting with "scan too large; narrow the path". It
	// bounds the arbitrary-path walk primitive the web endpoint exposes.
	DefaultWebScanMaxFiles = 50000
	// EnvToken is the environment variable holding the API token.
	EnvToken = "CIVITAI_TOKEN"
	// EnvComfyToken is the environment variable holding an optional ComfyUI login
	// token (for installs fronted by ComfyUI-Login). Like the CivitAI token it is a
	// secret and is never logged.
	EnvComfyToken = "COMFY_TOKEN"
	// EnvHFToken is the environment variable holding an OPTIONAL HuggingFace token,
	// used by the HuggingFace fallback resolver to raise rate limits and (when the
	// user has accepted a gated model's terms) download gated repos. It is a secret:
	// it is NEVER logged and is sent ONLY to HuggingFace hosts (never to civitai.com,
	// never to the CDN redirect target). The fallback works fully anonymously without
	// it for the public models it targets.
	EnvHFToken = "HF_TOKEN"
	// DefaultComfyURL is the default local ComfyUI server address. ComfyUI listens
	// on loopback with no auth by default.
	DefaultComfyURL = "http://127.0.0.1:8188"
	// DefaultOutputsMaxBytes is the default TOTAL size cap of the output-gallery
	// tree under OutputsDir: 20 GiB. Captured generations accumulate forever
	// otherwise, so the gallery ships bounded by default; once the total exceeds
	// the cap, the OLDEST generations are evicted (rows + files) until it is back
	// under. An explicit `outputs_max_bytes: "0"` disables the cap entirely.
	DefaultOutputsMaxBytes = int64(20) << 30
	// MinOutputsMaxBytes is the floor for a POSITIVE outputs_max_bytes. A cap of a
	// handful of bytes would evict essentially the entire gallery on the very next
	// capture, so anything under 1 MiB is refused at load as an almost-certain unit
	// mistake (`outputs_max_bytes: 20` intending 20 GB). Use "0" for unlimited.
	MinOutputsMaxBytes = int64(1) << 20

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
	// NoPreview, when true, skips writing the <base>.preview.png sidecar entirely.
	// Default false preserves the historical behavior (write the full-resolution
	// preview). Opt-in for users who do not want large image sidecars on disk.
	NoPreview bool `yaml:"no_preview"`
	// MaxPreviewSize caps the size of the preview image sidecar, as a human string
	// ("2MB") or a plain byte count. A fetched preview larger than this is skipped
	// (the model file is still downloaded). Empty / "0" means no cap (write any
	// size). Parsed into MaxPreviewSizeBytes.
	MaxPreviewSize string `yaml:"max_preview_size"`
	// ComfyURL is the local ComfyUI server base URL used for workflow runs and
	// preflight (/object_info). It is CONFIG-ONLY — never a per-request parameter —
	// so a run endpoint can never be pointed at an arbitrary target. Empty falls back
	// to DefaultComfyURL.
	ComfyURL string `yaml:"comfy_url"`
	// ComfyToken is an OPTIONAL bearer token for a ComfyUI fronted by a login node.
	// It is a secret: mirror Token's handling — NEVER log it; RedactToken it in any
	// diagnostic output.
	ComfyToken string `yaml:"comfy_token"`
	// OutputsDir is the civitai-manager-owned directory into which successful
	// workflow-run output images are COPIED (the durable output gallery). It is
	// app-owned application data, the same class as the DB — NOT model weights, so
	// it defaults NEXT TO the DB (<dir(DBPath)>/outputs) rather than under
	// ModelRoot. Empty resolves to that default. It is created on first write (no
	// pre-existence requirement), and ~ is expanded on load.
	OutputsDir string `yaml:"outputs_dir"`
	// OutputsMaxBytes caps the TOTAL bytes stored under OutputsDir. Like its
	// siblings max_file_size / max_preview_size it is a human SIZE STRING ("20GB",
	// "500MB") or a plain byte count, parsed by ParseSize into
	// OutputsMaxBytesResolved — read the cap through OutputsCapBytes(), never this
	// field.
	//
	// EMPTY (the key is absent) means "not configured" and resolves to
	// DefaultOutputsMaxBytes (20 GiB). An explicit "0" means UNLIMITED (no eviction
	// ever). A POSITIVE value below MinOutputsMaxBytes is REJECTED at load: a
	// cap of a few bytes would evict essentially the whole gallery on the next
	// capture, and is far more likely a unit mistake (`outputs_max_bytes: 20`
	// meaning 20 GB) than an intent.
	//
	// When the cap is exceeded after a capture, the OLDEST generations are deleted
	// (DB rows + their files, no undo) until the total is back under it; eviction is
	// best-effort and never affects a run's outcome.
	OutputsMaxBytes SizeString `yaml:"outputs_max_bytes"`
	// ComfyModelPath is the local filesystem root of ComfyUI's `models` directory
	// (the folder holding checkpoints/, loras/, vae/, …). It is SEPARATE from
	// ModelRoot (this app's own download layout) and is used ONLY by the
	// "Download & run" preflight action: it writes a missing model into the correct
	// per-type subfolder under this root so a local ComfyUI run can find it. Optional
	// — when empty the download flow is disabled (the missing-models panel degrades
	// to the CivitAI-link-only view). When SET it must be an existing writable
	// directory (validated on load), or config resolution fails with a clear error.
	ComfyModelPath string `yaml:"comfy_model_path"`
	// ComfyRoot is the local filesystem root of the ComfyUI INSTALL itself (the
	// folder holding main.py, custom_nodes/, models/, …) — the parent of
	// ComfyModelPath in a stock layout. It is used ONLY by the explicit, user-
	// triggered "install the ComfyUI helper extension" action, which writes
	// <ComfyRoot>/custom_nodes/civitai-manager/.
	//
	// Optional. When EMPTY it is derived from ComfyModelPath's parent, but only
	// when that parent actually looks like a ComfyUI install (has custom_nodes/);
	// otherwise it stays empty and the install action is unavailable. When SET it
	// must be an existing directory (validated on load, like ComfyModelPath); the
	// stronger "is this really a ComfyUI install, and is it writable" check runs at
	// install time so its error is actionable in the UI instead of at startup.
	ComfyRoot string `yaml:"comfy_root"`
	// HFToken is an OPTIONAL HuggingFace token for the HuggingFace fallback resolver.
	// Secret — mirror Token's handling: NEVER log it; RedactToken it in any diagnostic
	// output. Empty means anonymous (the default), which resolves the public aux-model
	// repos the fallback targets. It is sent ONLY to HuggingFace hosts.
	HFToken string `yaml:"hf_token"`
	// HFFallback enables the HuggingFace fallback: when CivitAI resolution finds no
	// match for a missing model filename, the filename is sent to huggingface.co to
	// look for a fallback download. This is a NEW external egress (mirrors the
	// match_remote opt-out spirit); it defaults ON but is clearly disclosed and can be
	// turned off with `hf_fallback: false`. When off, the resolve flow is CivitAI-only.
	HFFallback *bool `yaml:"hf_fallback"`
	// ResolveNodePacks enables the ONLINE half of custom-node attribution: when a
	// workflow run reports missing ComfyUI node class_types that a locally-present
	// ComfyUI-Manager could not place, those CLASS NAMES are sent to api.comfy.org
	// (the Comfy Registry) and raw.githubusercontent.com (ComfyUI-Manager's static
	// extension-node-map index) to learn which node pack provides them. This is a
	// NEW external egress (mirrors the match_remote opt-out spirit); it defaults ON
	// but is clearly disclosed and can be turned off with `resolve_node_packs:
	// false`. When off, attribution runs ONLY against a local ComfyUI-Manager
	// (loopback, NOT affected by this key) and anything Manager cannot place is
	// reported as unattributed.
	ResolveNodePacks *bool `yaml:"resolve_node_packs"`
	// ComfyCloud enables the "Run on CivitAI Cloud" feature: submitting a workflow
	// to the CivitAI orchestration API (which sends the graph + resource list to
	// civitai.com AND spends Buzz from the account behind Token). Default OFF —
	// the cloud UI is only shown/enabled when this is true, so the egress+spend is
	// strictly opt-in. It reuses the existing Token for auth (NO new secret): the
	// orchestration client is built from Token and nothing else (see
	// internal/comfy/cloud.go's Authorization: Bearer header).
	//
	// It is a *bool — mirroring HFFallback/ResolveNodePacks — precisely so the web
	// UI can tell "the user wrote comfy_cloud: false in the config file" apart from
	// "the key is absent". An EXPLICIT config value (either way) WINS over the
	// DB-stored web toggle and is shown read-only in the UI; only when this is nil
	// does the DB setting govern. Two sources silently disagreeing is worse than
	// either alone, so the precedence is explicit here and in the UI copy.
	ComfyCloud *bool `yaml:"comfy_cloud"`

	// MaxFileSizeBytes is the resolved byte value of MaxFileSize (0 = unlimited).
	MaxFileSizeBytes int64 `yaml:"-"`
	// MaxPreviewSizeBytes is the resolved byte value of MaxPreviewSize (0 = no cap).
	MaxPreviewSizeBytes int64 `yaml:"-"`
	// OutputsMaxBytesResolved is the resolved byte value of OutputsMaxBytes: the
	// 20 GiB default when unset, 0 when explicitly unlimited. Set by normalize; read
	// it through OutputsCapBytes().
	OutputsMaxBytesResolved int64 `yaml:"-"`
}

// SizeString is a human file-size string ("20GB", "500MB", "1048576") that also
// tolerates a BARE YAML INTEGER (`outputs_max_bytes: 20`), which would otherwise
// fail to decode into a string field. Decoding never interprets the value — it is
// parsed later by ParseSize — so an out-of-range or nonsensical value still gets a
// clear, unit-aware validation error rather than a YAML type error.
type SizeString string

// UnmarshalYAML accepts either a quoted string or a bare scalar number.
func (s *SizeString) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		var n float64
		if err2 := value.Decode(&n); err2 != nil {
			return err // report the string error — the key is documented as a string
		}
		raw = strconv.FormatFloat(n, 'f', -1, 64)
	}
	*s = SizeString(strings.TrimSpace(raw))
	return nil
}

// String returns the raw configured text.
func (s SizeString) String() string { return string(s) }

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
	// NoPreview, when true, turns OFF preview-sidecar writing. It is opt-in: the
	// flag can only enable the skip, never override a config `no_preview: true`
	// back to false (there is no need to force-write via a flag).
	NoPreview bool
	// MaxPreviewSize overrides the preview-size cap (a human size string or byte
	// count). Empty means "not set on the command line".
	MaxPreviewSize string
	// ComfyModelPath overrides the ComfyUI models-dir root used by the
	// "Download & run" flow. Empty means "not set on the command line".
	ComfyModelPath string
	// ComfyRoot overrides the ComfyUI install root used by the "install the
	// ComfyUI helper extension" action. Empty means "not set on the command line"
	// (falls through to file, then to the derived comfy_model_path parent).
	ComfyRoot string
	// OutputsDir overrides the output-gallery storage directory. Empty means "not
	// set on the command line" (falls through to file/default).
	OutputsDir string
	// OutputsMaxBytes overrides the output-gallery total disk cap. It is a human
	// size string ("20GB", "500MB") or a byte count; "0" means unlimited. Empty
	// means "not set on the command line" (falls through to file/default).
	OutputsMaxBytes string
	// HFToken overrides the optional HuggingFace token (secret; never logged). Empty
	// means "not set on the command line".
	HFToken string
	// ConfigPath overrides the config-file location (default: the XDG path).
	ConfigPath string
}

// HFFallbackEnabled reports whether the HuggingFace fallback is on. It defaults ON
// (an unset hf_fallback key); only an explicit `hf_fallback: false` disables it.
func (c *Config) HFFallbackEnabled() bool {
	return c.HFFallback == nil || *c.HFFallback
}

// ResolveNodePacksEnabled reports whether the online custom-node attribution
// lookups are on. It defaults ON (an unset resolve_node_packs key); only an
// explicit `resolve_node_packs: false` disables them.
func (c *Config) ResolveNodePacksEnabled() bool {
	return c.ResolveNodePacks == nil || *c.ResolveNodePacks
}

// ComfyCloudConfigured reports whether the config FILE explicitly set
// `comfy_cloud` (either true or false). When it did, that value is authoritative
// and the web toggle must be read-only; when it did not, the DB-stored web
// setting governs.
func (c *Config) ComfyCloudConfigured() bool {
	return c.ComfyCloud != nil
}

// ComfyCloudEnabled reports the CONFIG-LAYER value of `comfy_cloud`. It defaults
// OFF (an unset key) — cloud runs send data to civitai.com and spend Buzz, so
// they stay strictly opt-in. Callers that also honor the DB toggle must consult
// ComfyCloudConfigured first; this is only the config layer's answer.
func (c *Config) ComfyCloudEnabled() bool {
	return c.ComfyCloud != nil && *c.ComfyCloud
}

// OutputsCapBytes returns the resolved output-gallery total disk cap in bytes: the
// 20 GiB default when `outputs_max_bytes` is unset, or 0 for an explicit "0",
// which callers MUST treat as UNLIMITED (no eviction). It reads the value
// normalize resolved, so it is only meaningful on a Config returned by Resolve.
func (c *Config) OutputsCapBytes() int64 {
	if c.OutputsMaxBytesResolved < 0 {
		return 0
	}
	return c.OutputsMaxBytesResolved
}

// isUnitlessSize reports whether a size string carries NO unit suffix (only
// digits, an optional decimal point, and surrounding space) — i.e. the user very
// likely forgot the unit. It drives the wording of the too-small-cap hint.
func isUnitlessSize(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// resolveOutputsMaxBytes parses OutputsMaxBytes into OutputsMaxBytesResolved,
// applying the unset→default rule and rejecting an implausibly small positive cap.
func (c *Config) resolveOutputsMaxBytes() error {
	raw := strings.TrimSpace(string(c.OutputsMaxBytes))
	if raw == "" {
		c.OutputsMaxBytesResolved = DefaultOutputsMaxBytes
		return nil
	}
	n, err := ParseSize(raw)
	if err != nil {
		return fmt.Errorf("invalid outputs_max_bytes %q: %w", raw, err)
	}
	if n > 0 && n < MinOutputsMaxBytes {
		// The hint must read correctly for BOTH shapes. A UNITLESS value is almost
		// always a forgotten unit, so suggest the two plausible spellings; a value
		// that already carries a unit is simply too small, so suggesting "512KBGB"
		// would be gibberish — point at the minimum instead.
		hint := `use at least "1MB", or "0" for unlimited`
		if isUnitlessSize(raw) {
			hint = fmt.Sprintf("did you mean %q or %q? (use \"0\" for unlimited)", raw+"GB", raw+"MB")
		}
		return fmt.Errorf("outputs_max_bytes %q resolves to %d bytes, below the %d-byte (1 MiB) minimum — "+
			"a cap this small would delete almost the whole output gallery on the next run; %s",
			raw, n, MinOutputsMaxBytes, hint)
	}
	c.OutputsMaxBytesResolved = n
	return nil
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
		ComfyURL:            DefaultComfyURL,
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

	// Env layer: the CivitAI token and the optional ComfyUI token are
	// env-configurable (both secrets).
	if env := os.Getenv(EnvToken); env != "" {
		cfg.Token = env
	}
	if env := os.Getenv(EnvComfyToken); env != "" {
		cfg.ComfyToken = env
	}
	if env := os.Getenv(EnvHFToken); env != "" {
		cfg.HFToken = env
	}

	// Flag layer (highest precedence).
	if flags.Token != "" {
		cfg.Token = flags.Token
	}
	if flags.HFToken != "" {
		cfg.HFToken = flags.HFToken
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
	// NoPreview is opt-in: a set flag only turns the skip ON (it never overrides a
	// config `no_preview: true` back to false).
	if flags.NoPreview {
		cfg.NoPreview = true
	}
	if flags.MaxPreviewSize != "" {
		cfg.MaxPreviewSize = flags.MaxPreviewSize
	}
	if flags.ComfyModelPath != "" {
		cfg.ComfyModelPath = flags.ComfyModelPath
	}
	if flags.ComfyRoot != "" {
		cfg.ComfyRoot = flags.ComfyRoot
	}
	if flags.OutputsDir != "" {
		cfg.OutputsDir = flags.OutputsDir
	}
	if strings.TrimSpace(flags.OutputsMaxBytes) != "" {
		// An explicit flag always wins, including "0" (unlimited). It is parsed +
		// floor-checked by normalize, exactly like the config-file value.
		cfg.OutputsMaxBytes = SizeString(strings.TrimSpace(flags.OutputsMaxBytes))
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
	// OutputsDir is app-owned data co-located with the DB. Expand ~ when set;
	// default to <dir(DBPath)>/outputs. It is NOT validated for pre-existence here
	// (the capture path creates it on first write), unlike ComfyModelPath.
	c.OutputsDir = strings.TrimSpace(c.OutputsDir)
	if c.OutputsDir == "" {
		c.OutputsDir = filepath.Join(filepath.Dir(c.DBPath), "outputs")
	} else if c.OutputsDir, err = expandHome(c.OutputsDir); err != nil {
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
	c.ComfyURL = strings.TrimRight(strings.TrimSpace(c.ComfyURL), "/")
	if c.ComfyURL == "" {
		c.ComfyURL = DefaultComfyURL
	}
	// ComfyModelPath is optional; when set it must expand and point at an existing
	// WRITABLE directory (the "Download & run" flow writes model files into its
	// per-type subfolders). A bad value is a hard config error rather than a silent
	// disable, so a typo surfaces at startup instead of at first download.
	c.ComfyModelPath = strings.TrimSpace(c.ComfyModelPath)
	if c.ComfyModelPath != "" {
		if c.ComfyModelPath, err = expandHome(c.ComfyModelPath); err != nil {
			return err
		}
		if err := ValidateWritableDir(c.ComfyModelPath); err != nil {
			return fmt.Errorf("invalid comfy_model_path %q: %w", c.ComfyModelPath, err)
		}
	}
	// ComfyRoot is optional. An EXPLICIT value must expand to an existing directory
	// (a typo should surface at startup, mirroring comfy_model_path); an UNSET one
	// falls back to the comfy_model_path parent, and only when that parent really
	// looks like a ComfyUI install — a wrong guess must never become the target of
	// a write.
	c.ComfyRoot = strings.TrimSpace(c.ComfyRoot)
	if c.ComfyRoot != "" {
		if c.ComfyRoot, err = expandHome(c.ComfyRoot); err != nil {
			return err
		}
		info, statErr := os.Stat(c.ComfyRoot)
		if statErr != nil {
			return fmt.Errorf("invalid comfy_root %q: %w", c.ComfyRoot, statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("invalid comfy_root %q: not a directory", c.ComfyRoot)
		}
	} else {
		c.ComfyRoot = comfyext.DeriveRoot(c.ComfyModelPath)
	}
	// Resolve symlinks so the path the UI SHOWS (and the install writes to) is the
	// real location. Without this, "installed into /a/comfyui/custom_nodes/…" can
	// name a symlink while the running ComfyUI reports a different path, which
	// makes a failed install impossible to diagnose. Best-effort: a resolution
	// failure keeps the configured value rather than rejecting a usable config.
	if c.ComfyRoot != "" {
		if resolved, rerr := filepath.EvalSymlinks(c.ComfyRoot); rerr == nil {
			c.ComfyRoot = resolved
		}
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
	previewBytes, err := ParseSize(c.MaxPreviewSize)
	if err != nil {
		return fmt.Errorf("invalid max_preview_size %q: %w", c.MaxPreviewSize, err)
	}
	c.MaxPreviewSizeBytes = previewBytes
	if err := c.resolveOutputsMaxBytes(); err != nil {
		return err
	}
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

// ValidateWritableDir reports whether path is an existing directory this process
// can write to. It confirms writability by creating and removing a probe file
// (the only portable check — file-mode bits do not account for ACLs, ownership,
// or a read-only mount). Returns a descriptive error otherwise.
func ValidateWritableDir(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("directory does not exist")
		}
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("not a directory")
	}
	probe, err := os.CreateTemp(path, ".cm-writecheck-*")
	if err != nil {
		return fmt.Errorf("directory is not writable: %w", err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
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
	dup.ComfyToken = RedactToken(c.ComfyToken)
	dup.HFToken = RedactToken(c.HFToken)
	return dup
}

// String renders the config with the token redacted.
func (c *Config) String() string {
	r := c.Redacted()
	return fmt.Sprintf("Config{BaseURL:%s Addr:%s ModelRoot:%s DBPath:%s PollInterval:%s DownloadJitter:%s MaxFileSize:%d ComfyURL:%s ComfyModelPath:%s ComfyCloud:%t HFFallback:%t ResolveNodePacks:%t Token:%s ComfyToken:%s HFToken:%s}",
		r.BaseURL, r.Addr, r.ModelRoot, r.DBPath, c.DefaultPollInterval.D(), c.DownloadJitter.D(), c.MaxFileSizeBytes, r.ComfyURL, r.ComfyModelPath, c.ComfyCloudEnabled(), c.HFFallbackEnabled(), c.ResolveNodePacksEnabled(), r.Token, r.ComfyToken, r.HFToken)
}
