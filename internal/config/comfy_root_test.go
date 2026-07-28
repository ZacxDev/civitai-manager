package config

import (
	"os"
	"path/filepath"
	"testing"
)

// comfyInstall builds a directory that looks like a ComfyUI install (custom_nodes/
// plus a main.py fingerprint) with its models/ dir, and returns (root, models).
func comfyInstall(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	for _, sub := range []string{"custom_nodes", "models"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "main.py"), []byte("# comfyui\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "models")
}

// TestComfyRootUnsetAndNoModelPath asserts comfy_root stays empty (the extension
// install action is unavailable) when neither key is set.
func TestComfyRootUnsetAndNoModelPath(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Resolve(Flags{ConfigPath: filepath.Join(dir, "missing.yaml")})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.ComfyRoot != "" {
		t.Errorf("ComfyRoot = %q, want empty", cfg.ComfyRoot)
	}
}

// TestComfyRootDerivedFromModelPath asserts an unset comfy_root defaults to the
// comfy_model_path PARENT when that parent looks like a ComfyUI install.
func TestComfyRootDerivedFromModelPath(t *testing.T) {
	root, models := comfyInstall(t)
	cfgPath := writeConfig(t, t.TempDir(), "comfy_model_path: "+models+"\n")
	cfg, err := Resolve(Flags{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.ComfyRoot != root {
		t.Errorf("ComfyRoot = %q, want the derived %q", cfg.ComfyRoot, root)
	}
}

// TestComfyRootNotDerivedFromUnrelatedModelPath asserts the derivation REFUSES a
// models dir whose parent is not a ComfyUI install — a wrong guess must never
// become the target of a write.
func TestComfyRootNotDerivedFromUnrelatedModelPath(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	if err := os.MkdirAll(models, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeConfig(t, t.TempDir(), "comfy_model_path: "+models+"\n")
	cfg, err := Resolve(Flags{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.ComfyRoot != "" {
		t.Errorf("ComfyRoot = %q, want empty (parent is not a ComfyUI install)", cfg.ComfyRoot)
	}
}

// TestComfyRootExplicitWins asserts an explicit comfy_root overrides derivation.
func TestComfyRootExplicitWins(t *testing.T) {
	root, models := comfyInstall(t)
	other, _ := comfyInstall(t)
	cfgPath := writeConfig(t, t.TempDir(),
		"comfy_model_path: "+models+"\ncomfy_root: "+other+"\n")
	cfg, err := Resolve(Flags{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.ComfyRoot == root {
		t.Fatal("explicit comfy_root must not be replaced by the derived value")
	}
	if cfg.ComfyRoot != other {
		t.Errorf("ComfyRoot = %q, want %q", cfg.ComfyRoot, other)
	}
}

// TestComfyRootFlagOverridesFile asserts --comfy-root wins over the config file.
func TestComfyRootFlagOverridesFile(t *testing.T) {
	fileRoot, _ := comfyInstall(t)
	flagRoot, _ := comfyInstall(t)
	cfgPath := writeConfig(t, t.TempDir(), "comfy_root: "+fileRoot+"\n")
	cfg, err := Resolve(Flags{ConfigPath: cfgPath, ComfyRoot: flagRoot})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.ComfyRoot != flagRoot {
		t.Errorf("ComfyRoot = %q, want the flag value %q", cfg.ComfyRoot, flagRoot)
	}
}

// TestComfyRootMissingOrFileErrors asserts an explicit but nonexistent comfy_root
// (or one pointing at a file) is a hard config error — a typo surfaces at startup.
func TestComfyRootMissingOrFileErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	cfgPath := writeConfig(t, dir, "comfy_root: "+missing+"\n")
	if _, err := Resolve(Flags{ConfigPath: cfgPath}); err == nil {
		t.Error("expected an error for a nonexistent comfy_root")
	}

	dir2 := t.TempDir()
	file := filepath.Join(dir2, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath2 := writeConfig(t, dir2, "comfy_root: "+file+"\n")
	if _, err := Resolve(Flags{ConfigPath: cfgPath2}); err == nil {
		t.Error("expected an error for a comfy_root that is a file")
	}
}

// TestComfyRootNotDerivedFromContainingFolder pins the mix-up the audit found on
// this machine: /…/fast/comfyui/ HAS a custom_nodes/ while the real install is one
// level deeper at /…/fast/comfyui/ComfyUI/. If comfy_model_path points at the
// install root itself (instead of its models/ dir), the derived parent is the
// CONTAINING folder — which must NOT be accepted, or the helper is written where
// the running ComfyUI never looks, with no diagnostic at all.
func TestComfyRootNotDerivedFromContainingFolder(t *testing.T) {
	outer := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outer, "custom_nodes"), 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "ComfyUI")
	for _, sub := range []string{"custom_nodes", "models"} {
		if err := os.MkdirAll(filepath.Join(inner, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(inner, "main.py"), []byte("# comfyui\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// comfy_model_path pointing at the INSTALL ROOT (the easy mistake) → the parent
	// is the containing folder, which has custom_nodes/ but is not a ComfyUI install.
	cfgPath := writeConfig(t, t.TempDir(), "comfy_model_path: "+inner+"\n")
	cfg, err := Resolve(Flags{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.ComfyRoot != "" {
		t.Errorf("ComfyRoot = %q, want empty (the parent is the folder CONTAINING the install)", cfg.ComfyRoot)
	}

	// The correct configuration still derives correctly.
	cfgPath2 := writeConfig(t, t.TempDir(), "comfy_model_path: "+filepath.Join(inner, "models")+"\n")
	cfg2, err := Resolve(Flags{ConfigPath: cfgPath2})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg2.ComfyRoot != inner {
		t.Errorf("ComfyRoot = %q, want %q", cfg2.ComfyRoot, inner)
	}
}
