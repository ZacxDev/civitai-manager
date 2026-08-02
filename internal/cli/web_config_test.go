package cli

import (
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/config"
)

// TestWebConfigCarriesTheWebScanTimeout pins the THIRD hop of the
// web_scan_timeout chain: config.Config -> web.Config, the assignment serveRun
// makes when it constructs the web server. That hop was correct all along —
// which is precisely why the defect survived: every individual link looked
// right, and the value simply died on the far side. Pinning it here means a
// future refactor of webConfigFor cannot quietly drop the knob and leave the
// web-side guard passing against a value nobody supplied.
//
// (Hop 1-2: config.TestResolveWebScanTimeout. Hop 4: the web package's
// TestWebScanTimeoutBoundsTheScanJob.)
func TestWebConfigCarriesTheWebScanTimeout(t *testing.T) {
	cfg := &config.Config{
		BaseURL:        "https://civitai.com",
		Addr:           "127.0.0.1:8787",
		WebScanTimeout: config.Duration(45 * time.Minute),
		// Set alongside its sibling so a copy/paste that crosses the two wires is
		// visible here rather than in production.
		WebScanMaxFiles: 1234,
	}
	wc := webConfigFor(cfg)
	if wc.WebScanTimeout != 45*time.Minute {
		t.Errorf("web.Config.WebScanTimeout = %v, want 45m (the resolved config value)", wc.WebScanTimeout)
	}
	if wc.WebScanMaxFiles != 1234 {
		t.Errorf("web.Config.WebScanMaxFiles = %d, want 1234", wc.WebScanMaxFiles)
	}

	// An unset value is passed through as zero — the web server, not this hop,
	// owns the fallback (Server.webScanTimeout). Substituting a default here
	// would give the knob two owners and two places to drift.
	if got := webConfigFor(&config.Config{}).WebScanTimeout; got != 0 {
		t.Errorf("an unset config timeout should cross as 0 and let the web server apply its default, got %v", got)
	}
}
