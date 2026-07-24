package library

import (
	"context"
	"path/filepath"
	"testing"
)

// TestDetectComfyUIWorkflowDirs asserts a ComfyUI install laid out with a
// user/default/workflows dir is detected WITH that dir populated in WorkflowDirs
// (and the output dir surfaced), while the model scanner still treats it as a leaf.
func TestDetectComfyUIWorkflowDirs(t *testing.T) {
	root := t.TempDir()
	comfy := filepath.Join(root, "ComfyUI")
	mkdirAll(t, filepath.Join(comfy, "models", "checkpoints"))
	mkdirAll(t, filepath.Join(comfy, "user", "default", "workflows"))
	mkdirAll(t, filepath.Join(comfy, "output"))
	writeFile(t, filepath.Join(comfy, "main.py"), "x\n")

	got := discover(t, context.Background(), root, DiscoverOptions{})
	var found *Install
	for i := range got {
		if got[i].Kind == KindComfyUI {
			found = &got[i]
		}
	}
	if found == nil {
		t.Fatal("ComfyUI install not detected")
	}
	wantWF := filepath.Join(comfy, "user", "default", "workflows")
	if len(found.WorkflowDirs) != 1 || found.WorkflowDirs[0] != wantWF {
		t.Errorf("WorkflowDirs = %v, want [%s]", found.WorkflowDirs, wantWF)
	}
	if found.OutputDir != filepath.Join(comfy, "output") {
		t.Errorf("OutputDir = %q", found.OutputDir)
	}

	// WorkflowScanDirs collects it.
	dirs := WorkflowScanDirs(got)
	if len(dirs) != 1 || dirs[0] != wantWF {
		t.Errorf("WorkflowScanDirs = %v, want [%s]", dirs, wantWF)
	}
}

// TestDetectComfyUINoWorkflowDir: an install without any workflows dir yields an
// empty WorkflowDirs and contributes nothing to WorkflowScanDirs.
func TestDetectComfyUINoWorkflowDir(t *testing.T) {
	root := t.TempDir()
	comfy := filepath.Join(root, "ComfyUI")
	mkdirAll(t, filepath.Join(comfy, "models", "loras"))
	writeFile(t, filepath.Join(comfy, "main.py"), "x\n")

	got := discover(t, context.Background(), root, DiscoverOptions{})
	for _, in := range got {
		if in.Kind == KindComfyUI && len(in.WorkflowDirs) != 0 {
			t.Errorf("expected no WorkflowDirs, got %v", in.WorkflowDirs)
		}
	}
	if dirs := WorkflowScanDirs(got); len(dirs) != 0 {
		t.Errorf("WorkflowScanDirs = %v, want empty", dirs)
	}
}

// TestComfyLegacyWorkflowsDir: the legacy top-level "workflows" dir is also
// picked up when present.
func TestComfyLegacyWorkflowsDir(t *testing.T) {
	root := t.TempDir()
	comfy := filepath.Join(root, "ComfyUI")
	mkdirAll(t, filepath.Join(comfy, "models", "checkpoints"))
	mkdirAll(t, filepath.Join(comfy, "workflows"))
	writeFile(t, filepath.Join(comfy, "main.py"), "x\n")

	got := discover(t, context.Background(), root, DiscoverOptions{})
	dirs := WorkflowScanDirs(got)
	want := filepath.Join(comfy, "workflows")
	if len(dirs) != 1 || dirs[0] != want {
		t.Errorf("WorkflowScanDirs = %v, want [%s]", dirs, want)
	}
}
