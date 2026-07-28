package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveOutputsMaxBytes pins the three-way semantics of outputs_max_bytes:
// unset → the 20 GiB default, explicit "0" → unlimited, anything else → parsed by
// ParseSize like its max_file_size / max_preview_size siblings.
func TestResolveOutputsMaxBytes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvToken, "")

	// No config key and no flag → the 20 GiB default.
	cfg, err := Resolve(Flags{ConfigPath: filepath.Join(dir, "missing.yaml")})
	if err != nil {
		t.Fatalf("resolve (default): %v", err)
	}
	if cfg.OutputsCapBytes() != DefaultOutputsMaxBytes {
		t.Errorf("default cap = %d, want %d", cfg.OutputsCapBytes(), DefaultOutputsMaxBytes)
	}

	// An explicit "0" means unlimited (NOT "unset → default").
	zeroPath := writeConfig(t, t.TempDir(), "outputs_max_bytes: \"0\"\n")
	cfg, err = Resolve(Flags{ConfigPath: zeroPath})
	if err != nil {
		t.Fatalf("resolve (0): %v", err)
	}
	if cfg.OutputsCapBytes() != 0 {
		t.Errorf("cap for `outputs_max_bytes: \"0\"` = %d, want 0 (unlimited)", cfg.OutputsCapBytes())
	}

	// A bare (unquoted) 0 decodes too, and means the same thing.
	bareZero := writeConfig(t, t.TempDir(), "outputs_max_bytes: 0\n")
	cfg, err = Resolve(Flags{ConfigPath: bareZero})
	if err != nil {
		t.Fatalf("resolve (bare 0): %v", err)
	}
	if cfg.OutputsCapBytes() != 0 {
		t.Errorf("cap for a bare `outputs_max_bytes: 0` = %d, want 0", cfg.OutputsCapBytes())
	}
}

// TestResolveOutputsMaxBytesAcceptsSizeStrings is the F5 fix: the YAML key takes a
// human size string like every other size knob, not just a raw byte count.
func TestResolveOutputsMaxBytesAcceptsSizeStrings(t *testing.T) {
	t.Setenv(EnvToken, "")
	cases := map[string]int64{
		"outputs_max_bytes: \"20GB\"\n":   20 << 30,
		"outputs_max_bytes: \"500MB\"\n":  500 << 20,
		"outputs_max_bytes: \"2G\"\n":     2 << 30,
		"outputs_max_bytes: 1073741824\n": 1 << 30, // a bare integer still works
	}
	for body, want := range cases {
		path := writeConfig(t, t.TempDir(), body)
		cfg, err := Resolve(Flags{ConfigPath: path})
		if err != nil {
			t.Fatalf("resolve %q: %v", strings.TrimSpace(body), err)
		}
		if cfg.OutputsCapBytes() != want {
			t.Errorf("%q → cap %d, want %d", strings.TrimSpace(body), cfg.OutputsCapBytes(), want)
		}
	}
}

// TestResolveOutputsMaxBytesRejectsTinyCap is the footgun guard: the obvious unit
// mistake `outputs_max_bytes: 20` (meaning 20 GB) must fail loudly at load rather
// than silently evicting the whole gallery on the next capture.
func TestResolveOutputsMaxBytesRejectsTinyCap(t *testing.T) {
	t.Setenv(EnvToken, "")
	for _, body := range []string{
		"outputs_max_bytes: 20\n",
		"outputs_max_bytes: \"20\"\n",
		"outputs_max_bytes: \"1KB\"\n",
	} {
		path := writeConfig(t, t.TempDir(), body)
		_, err := Resolve(Flags{ConfigPath: path})
		if err == nil {
			t.Errorf("%q: expected a rejection, got nil error", strings.TrimSpace(body))
			continue
		}
		if !strings.Contains(err.Error(), "outputs_max_bytes") {
			t.Errorf("%q: error should name the key, got %v", strings.TrimSpace(body), err)
		}
	}

	// Exactly at the floor is allowed.
	path := writeConfig(t, t.TempDir(), "outputs_max_bytes: \"1MB\"\n")
	cfg, err := Resolve(Flags{ConfigPath: path})
	if err != nil {
		t.Fatalf("resolve (exactly 1MB): %v", err)
	}
	if cfg.OutputsCapBytes() != MinOutputsMaxBytes {
		t.Errorf("cap = %d, want %d", cfg.OutputsCapBytes(), MinOutputsMaxBytes)
	}
}

func TestResolveOutputsMaxBytesFlagOverride(t *testing.T) {
	t.Setenv(EnvToken, "")
	valPath := writeConfig(t, t.TempDir(), "outputs_max_bytes: \"1GB\"\n")

	// The flag overrides the file and accepts a human size string.
	cfg, err := Resolve(Flags{ConfigPath: valPath, OutputsMaxBytes: "2GB"})
	if err != nil {
		t.Fatalf("resolve (flag): %v", err)
	}
	if cfg.OutputsCapBytes() != 2<<30 {
		t.Errorf("flag cap = %d, want %d", cfg.OutputsCapBytes(), int64(2)<<30)
	}

	// The flag can also force unlimited over a configured cap.
	cfg, err = Resolve(Flags{ConfigPath: valPath, OutputsMaxBytes: "0"})
	if err != nil {
		t.Fatalf("resolve (flag 0): %v", err)
	}
	if cfg.OutputsCapBytes() != 0 {
		t.Errorf("flag `0` cap = %d, want 0 (unlimited)", cfg.OutputsCapBytes())
	}

	// A malformed flag is a clear error, not a silent fallback.
	if _, err := Resolve(Flags{ConfigPath: valPath, OutputsMaxBytes: "not-a-size"}); err == nil {
		t.Error("expected an error for a malformed --outputs-max-bytes")
	}
	// The floor applies to the flag too.
	if _, err := Resolve(Flags{ConfigPath: valPath, OutputsMaxBytes: "20"}); err == nil {
		t.Error("expected a rejection for --outputs-max-bytes=20 (20 bytes)")
	}
}
