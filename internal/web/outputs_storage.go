package web

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// maxOutputImageBytes bounds a single captured output image on disk. It matches
// the comfy /view read bound (64 MiB), so a legitimately-fetched image always
// fits while an adversarial/oversized body is refused.
const maxOutputImageBytes = int64(64) << 20

// safePathSegment collapses an untrusted string (a ComfyUI prompt id or output
// filename) to a SINGLE safe path segment: it normalizes both separators, takes
// the basename, and rejects an empty / "." / ".." result. A hostile value like
// "../../etc/passwd" collapses to "passwd"; "a/b/c.png" to "c.png". This is the
// same basename discipline as comfy.SafeModelDest.
func safePathSegment(s string) (string, error) {
	base := path.Base(strings.ReplaceAll(strings.TrimSpace(s), "\\", "/"))
	if base == "" || base == "." || base == ".." || base == "/" {
		return "", fmt.Errorf("invalid path segment %q", s)
	}
	return base, nil
}

// outputRelPath builds the root-relative storage path for one captured image:
//
//	<sanitized prompt_id>/<idx>-<sanitized basename>
//
// The prompt_id subdir (unique per run) + the idx prefix make the path
// collision-free across runs and across a run's own N images, WITHOUT needing the
// DB autoincrement id first. Both the prompt id and the comfy-supplied filename
// are sanitized to a single safe segment (the comfy server is untrusted).
func outputRelPath(promptID string, idx int, filename string) (string, error) {
	promptSeg, err := safePathSegment(promptID)
	if err != nil {
		return "", fmt.Errorf("prompt id: %w", err)
	}
	nameSeg, err := safePathSegment(filename)
	if err != nil {
		return "", fmt.Errorf("filename: %w", err)
	}
	return promptSeg + "/" + strconv.Itoa(idx) + "-" + nameSeg, nil
}

// safeOutputPath resolves a root-relative rel_path to an absolute path and ASSERTS
// it stays inside root (defense in depth — even a corrupted DB row must never read
// or write outside the outputs tree). It rejects an empty root/relPath, an
// absolute relPath, and any ".." escape.
func safeOutputPath(root, relPath string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("empty outputs root")
	}
	rel := strings.TrimSpace(relPath)
	if rel == "" {
		return "", errors.New("empty output rel_path")
	}
	// Normalize backslashes so a Windows-style traversal is collapsed on every
	// platform, then reject an absolute or ".."-escaping path lexically first.
	cleaned := filepath.Clean(strings.ReplaceAll(rel, "\\", "/"))
	if filepath.IsAbs(cleaned) || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("output path %q escapes root", relPath)
	}
	dest := filepath.Join(root, cleaned)
	// Belt-and-suspenders: the joined result must resolve back inside root.
	r, err := filepath.Rel(root, dest)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("output path %q escapes root", relPath)
	}
	return dest, nil
}

// writeOutputImage writes data to <root>/<relPath> atomically (temp-in-same-dir →
// fsync → rename), path-contained and size-capped. It reuses comfy.WriteModelStream
// for the atomic-write + size-cap + refuse-overwrite discipline; the unique
// per-run rel_path means the refuse-overwrite never trips in normal capture. It
// returns the number of bytes written.
func writeOutputImage(root, relPath string, data []byte, maxBytes int64) (int64, error) {
	dest, err := safeOutputPath(root, relPath)
	if err != nil {
		return 0, err
	}
	if maxBytes <= 0 {
		maxBytes = maxOutputImageBytes
	}
	if int64(len(data)) > maxBytes {
		return 0, fmt.Errorf("%w (%d bytes > cap %d)", comfy.ErrSizeCapExceeded, len(data), maxBytes)
	}
	return comfy.WriteModelStream(dest, bytes.NewReader(data), int64(len(data)), maxBytes)
}
