package config

import (
	"path/filepath"
	"testing"
	"time"
)

// TestResolveWebScanTimeout pins the FIRST two hops of a knob whose whole chain
// was faithfully carried and then read by nobody for 88 releases: YAML ->
// Config, and --web-scan-timeout -> Config (the flag overriding the file). The
// remaining hops are pinned by TestWebConfigCarriesTheWebScanTimeout
// (Config -> web.Config) and TestWebScanTimeoutBoundsTheScanJob (web.Config ->
// the scan job's context deadline).
func TestResolveWebScanTimeout(t *testing.T) {
	t.Setenv(EnvToken, "")

	// Nothing set anywhere -> the built-in default. This is the value the web
	// scan already enforced as a hard-coded const, so wiring the knob up changed
	// nothing for an unconfigured user.
	cfg, err := Resolve(Flags{ConfigPath: filepath.Join(t.TempDir(), "missing.yaml")})
	if err != nil {
		t.Fatalf("resolve (default): %v", err)
	}
	if got := cfg.WebScanTimeout.D(); got != DefaultWebScanTimeout {
		t.Errorf("unset web_scan_timeout = %v, want the default %v", got, DefaultWebScanTimeout)
	}
	if DefaultWebScanTimeout != 6*time.Hour {
		t.Errorf("DefaultWebScanTimeout = %v, want 6h (as documented in docs/configuration.md and docs/cli.md)", DefaultWebScanTimeout)
	}

	// The YAML key is honoured verbatim.
	yamlPath := writeConfig(t, t.TempDir(), "web_scan_timeout: \"45m\"\n")
	cfg, err = Resolve(Flags{ConfigPath: yamlPath})
	if err != nil {
		t.Fatalf("resolve (yaml): %v", err)
	}
	if got := cfg.WebScanTimeout.D(); got != 45*time.Minute {
		t.Errorf("web_scan_timeout: \"45m\" resolved to %v, want 45m", got)
	}

	// The flag overrides the file.
	cfg, err = Resolve(Flags{ConfigPath: yamlPath, WebScanTimeout: "90s"})
	if err != nil {
		t.Fatalf("resolve (flag): %v", err)
	}
	if got := cfg.WebScanTimeout.D(); got != 90*time.Second {
		t.Errorf("--web-scan-timeout 90s over a 45m file resolved to %v, want 90s", got)
	}

	// A non-positive value is meaningless for a bound, so it means the DEFAULT —
	// never "no bound" (a stuck job would leak forever) and never "instant".
	for _, raw := range []string{"0s", "-5m"} {
		p := writeConfig(t, t.TempDir(), "web_scan_timeout: \""+raw+"\"\n")
		cfg, err = Resolve(Flags{ConfigPath: p})
		if err != nil {
			t.Fatalf("resolve (%s): %v", raw, err)
		}
		if got := cfg.WebScanTimeout.D(); got != DefaultWebScanTimeout {
			t.Errorf("web_scan_timeout: %q resolved to %v, want the default %v", raw, got, DefaultWebScanTimeout)
		}
	}

	// A garbage flag fails LOUDLY rather than silently falling back.
	if _, err := Resolve(Flags{ConfigPath: yamlPath, WebScanTimeout: "not-a-duration"}); err == nil {
		t.Error("an unparseable --web-scan-timeout should be a hard error, not a silent default")
	}
}
