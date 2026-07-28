package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputRelPathSchemeAndCollisionFree(t *testing.T) {
	rp0, err := outputRelPath("promptABC", 0, "ComfyUI_00012_.png")
	if err != nil {
		t.Fatalf("rel path: %v", err)
	}
	if rp0 != "promptABC/0-ComfyUI_00012_.png" {
		t.Errorf("rel path = %q, want promptABC/0-ComfyUI_00012_.png", rp0)
	}
	// Two images from the same run get distinct paths (idx prefix).
	rp1, _ := outputRelPath("promptABC", 1, "ComfyUI_00012_.png")
	if rp0 == rp1 {
		t.Errorf("collision: idx 0 and 1 produced the same path %q", rp0)
	}
	// A different run (different prompt id) is under a different subdir.
	rpOther, _ := outputRelPath("promptXYZ", 0, "ComfyUI_00012_.png")
	if strings.HasPrefix(rpOther, "promptABC/") {
		t.Errorf("run isolation broken: %q", rpOther)
	}
}

func TestOutputRelPathSanitizesHostileNames(t *testing.T) {
	// A hostile comfy filename with traversal collapses to its basename.
	rp, err := outputRelPath("p", 0, "../../etc/passwd")
	if err != nil {
		t.Fatalf("rel path: %v", err)
	}
	if rp != "p/0-passwd" {
		t.Errorf("rel path = %q, want p/0-passwd", rp)
	}
	// A hostile prompt id likewise collapses.
	rp2, err := outputRelPath("../../evil", 0, "a.png")
	if err != nil {
		t.Fatalf("rel path: %v", err)
	}
	if rp2 != "evil/0-a.png" {
		t.Errorf("rel path = %q, want evil/0-a.png", rp2)
	}
	// Empty / dot-only segments are rejected.
	for _, bad := range []string{"", ".", "..", "/"} {
		if _, err := outputRelPath("p", 0, bad); err == nil {
			t.Errorf("filename %q should be rejected", bad)
		}
		if _, err := outputRelPath(bad, 0, "a.png"); err == nil {
			t.Errorf("prompt id %q should be rejected", bad)
		}
	}
}

func TestSafeOutputPathContainment(t *testing.T) {
	root := t.TempDir()
	// A normal relative path resolves inside root.
	got, err := safeOutputPath(root, "p/0-a.png")
	if err != nil {
		t.Fatalf("safe path: %v", err)
	}
	if !strings.HasPrefix(got, root+string(filepath.Separator)) {
		t.Errorf("resolved path %q not inside root %q", got, root)
	}
	// Traversal, absolute, and escape are all rejected.
	for _, bad := range []string{"../escape.png", "../../etc/passwd", "/etc/passwd", "p/../../escape"} {
		if _, err := safeOutputPath(root, bad); err == nil {
			t.Errorf("rel_path %q should be rejected as escaping root", bad)
		}
	}
	// Empty root / empty rel_path are rejected.
	if _, err := safeOutputPath("", "a.png"); err == nil {
		t.Error("empty root should be rejected")
	}
	if _, err := safeOutputPath(root, ""); err == nil {
		t.Error("empty rel_path should be rejected")
	}
}

func TestWriteOutputImageAtomicAndCapped(t *testing.T) {
	root := t.TempDir()
	data := []byte("PNGDATA")
	n, err := writeOutputImage(root, "p/0-a.png", data, maxOutputImageBytes)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != int64(len(data)) {
		t.Errorf("wrote %d bytes, want %d", n, len(data))
	}
	// The file landed at the expected location with the right bytes.
	got, err := os.ReadFile(filepath.Join(root, "p", "0-a.png"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content = %q, want %q", got, data)
	}
	// No leftover temp files in the dir (atomic write cleaned up).
	entries, _ := os.ReadDir(filepath.Join(root, "p"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cm-download-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestWriteOutputImageSizeGuard(t *testing.T) {
	root := t.TempDir()
	if _, err := writeOutputImage(root, "p/0-big.png", []byte("toolarge"), 3); err == nil {
		t.Error("oversized write should be rejected by the size guard")
	}
	// The rejected write left nothing behind.
	if _, err := os.Stat(filepath.Join(root, "p", "0-big.png")); err == nil {
		t.Error("rejected write should not have created the file")
	}
}

func TestWriteOutputImageRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := writeOutputImage(root, "../escape.png", []byte("x"), maxOutputImageBytes); err == nil {
		t.Error("traversal rel_path should be rejected on write")
	}
}
