package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestComfyModelPathUnsetDisabled asserts an absent comfy_model_path resolves to
// "" (the download flow is disabled) with no error.
func TestComfyModelPathUnsetDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Resolve(Flags{ConfigPath: filepath.Join(dir, "missing.yaml")})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.ComfyModelPath != "" {
		t.Errorf("ComfyModelPath = %q, want empty (flow disabled)", cfg.ComfyModelPath)
	}
}

// TestComfyModelPathValidDir asserts a set comfy_model_path pointing at an
// existing writable dir resolves cleanly.
func TestComfyModelPathValidDir(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	if err := os.MkdirAll(models, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeConfig(t, dir, "comfy_model_path: "+models+"\n")
	cfg, err := Resolve(Flags{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.ComfyModelPath != models {
		t.Errorf("ComfyModelPath = %q, want %q", cfg.ComfyModelPath, models)
	}
}

// TestComfyModelPathMissingDirErrors asserts a set-but-nonexistent path is a hard
// config error (surfaced at startup, not silently disabled).
func TestComfyModelPathMissingDirErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")
	cfgPath := writeConfig(t, dir, "comfy_model_path: "+missing+"\n")
	if _, err := Resolve(Flags{ConfigPath: cfgPath}); err == nil {
		t.Fatal("expected an error for a nonexistent comfy_model_path, got nil")
	}
}

// TestComfyModelPathFileNotDirErrors asserts a path pointing at a FILE errors.
func TestComfyModelPathFileNotDirErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeConfig(t, dir, "comfy_model_path: "+file+"\n")
	if _, err := Resolve(Flags{ConfigPath: cfgPath}); err == nil {
		t.Fatal("expected an error for a comfy_model_path that is a file, got nil")
	}
}

// TestComfyModelPathNotWritableErrors asserts a read-only directory errors.
func TestComfyModelPathNotWritableErrors(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	dir := t.TempDir()
	ro := filepath.Join(dir, "readonly")
	if err := os.MkdirAll(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	cfgPath := writeConfig(t, dir, "comfy_model_path: "+ro+"\n")
	if _, err := Resolve(Flags{ConfigPath: cfgPath}); err == nil {
		t.Fatal("expected an error for a non-writable comfy_model_path, got nil")
	}
}

// TestComfyModelPathFlagOverridesFile asserts the --comfy-model-path flag wins
// over the config file.
func TestComfyModelPathFlagOverridesFile(t *testing.T) {
	dir := t.TempDir()
	fileDir := filepath.Join(dir, "fromfile")
	flagDir := filepath.Join(dir, "fromflag")
	for _, d := range []string{fileDir, flagDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := writeConfig(t, dir, "comfy_model_path: "+fileDir+"\n")
	cfg, err := Resolve(Flags{ConfigPath: cfgPath, ComfyModelPath: flagDir})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.ComfyModelPath != flagDir {
		t.Errorf("ComfyModelPath = %q, want the flag value %q", cfg.ComfyModelPath, flagDir)
	}
}
