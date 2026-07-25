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

// A marked ComfyUI install ROOT must resolve to its user/default/workflows dir
// SPECIFICALLY (not the whole tree), so bundled examples/templates aren't swept in.
func TestWorkflowDirsForMarkedComfyRoot(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "user", "default", "workflows")
	mkdirAll(t, wf)
	mkdirAll(t, filepath.Join(root, "custom_nodes", "somenode", "example_workflows"))
	mkdirAll(t, filepath.Join(root, "models", "checkpoints"))

	got := WorkflowDirsForMarked(root)
	if len(got) != 1 || got[0] != wf {
		t.Errorf("WorkflowDirsForMarked(root) = %v, want [%s] (not the whole tree)", got, wf)
	}
}

// A marked arbitrary directory (no ComfyUI layout) is scanned as-is.
func TestWorkflowDirsForMarkedArbitrary(t *testing.T) {
	dir := t.TempDir()
	got := WorkflowDirsForMarked(dir)
	if len(got) != 1 || got[0] != dir {
		t.Errorf("WorkflowDirsForMarked(arbitrary) = %v, want [%s]", got, dir)
	}
}
