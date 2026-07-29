package uxaudit

import (
	"fmt"
	"os"
	"os/exec"
)

// ResolveChromium finds a Chromium/Chrome executable host-agnostically, mirroring
// auditloop: an explicit AUDITLOOP_CHROMIUM (or CHROMIUM_PATH) wins, else the
// common binary names are looked up on PATH. It never downloads a browser (no
// `playwright install`) — on NixOS the nix-provided chromium is used.
//
// It returns an empty string (not an error) when nothing is found, so the
// chromium-gated walk test can skip cleanly with a clear message.
func ResolveChromium() string {
	for _, env := range []string{"AUDITLOOP_CHROMIUM", "CHROMIUM_PATH", "UXAUDIT_CHROMIUM"} {
		if p := os.Getenv(env); p != "" {
			return p
		}
	}
	for _, name := range []string{"chromium", "chromium-browser", "chrome", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// MustResolveChromium is ResolveChromium but returns an error describing the
// searched locations when nothing is found.
func MustResolveChromium() (string, error) {
	if p := ResolveChromium(); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("no chromium found: set AUDITLOOP_CHROMIUM or put chromium/chromium-browser/chrome on PATH (never `playwright install`)")
}
