package config

import (
	"path/filepath"
	"testing"
)

// TestOutputsCapBytesDefault pins the three-way semantics of outputs_max_bytes:
// unset → 20 GiB default, explicit 0 → unlimited, negative → unlimited.
func TestOutputsCapBytes(t *testing.T) {
	var zero int64
	var neg int64 = -1
	var set int64 = 1 << 20

	cases := []struct {
		name string
		in   *int64
		want int64
	}{
		{"unset defaults to 20 GiB", nil, DefaultOutputsMaxBytes},
		{"explicit 0 is unlimited", &zero, 0},
		{"negative is unlimited", &neg, 0},
		{"explicit value is honoured", &set, 1 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{OutputsMaxBytes: tc.in}
			if got := c.OutputsCapBytes(); got != tc.want {
				t.Errorf("OutputsCapBytes() = %d, want %d", got, tc.want)
			}
		})
	}
}

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

	// An explicit 0 in the config file means unlimited (NOT "unset → default").
	zeroPath := writeConfig(t, t.TempDir(), "outputs_max_bytes: 0\n")
	cfg, err = Resolve(Flags{ConfigPath: zeroPath})
	if err != nil {
		t.Fatalf("resolve (0): %v", err)
	}
	if cfg.OutputsCapBytes() != 0 {
		t.Errorf("cap for `outputs_max_bytes: 0` = %d, want 0 (unlimited)", cfg.OutputsCapBytes())
	}

	// A config value is honoured.
	valPath := writeConfig(t, t.TempDir(), "outputs_max_bytes: 1048576\n")
	cfg, err = Resolve(Flags{ConfigPath: valPath})
	if err != nil {
		t.Fatalf("resolve (value): %v", err)
	}
	if cfg.OutputsCapBytes() != 1<<20 {
		t.Errorf("cap = %d, want 1048576", cfg.OutputsCapBytes())
	}

	// The flag overrides the file and accepts a human size string.
	cfg, err = Resolve(Flags{ConfigPath: valPath, OutputsMaxBytes: "2GB"})
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
}
