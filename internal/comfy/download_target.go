package comfy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrDestExists is returned by WriteModelStream when the destination file already
// exists. The "Download & run" orchestrator treats this as "already installed —
// skip the download and just run": the referenced file is present, so the graph
// reference already resolves.
var ErrDestExists = errors.New("destination file already exists")

// ErrSizeCapExceeded is returned when a download's advertised or actual size
// exceeds the enforced cap.
var ErrSizeCapExceeded = errors.New("download exceeds the maximum allowed size")

// defaultModelSizeCap bounds a single model download when no explicit cap is
// configured (config MaxFileSize unset). It is generous enough for a large
// checkpoint but still prevents an unbounded / adversarial download from filling
// the disk.
const defaultModelSizeCap = int64(25) << 30 // 25 GiB

// diskFreeMargin is the slack left above a download's advertised size in the
// free-disk pre-check, so a download does not fill a volume to the last byte.
const diskFreeMargin = int64(64) << 20 // 64 MiB

// typeSubdirs maps a CivitAI model `type` (case-insensitive) to the conventional
// ComfyUI models/ subdirectory a file of that type is loaded from. Only types
// with an unambiguous ComfyUI home are listed; an unlisted type yields (","",
// false) so the caller REFUSES the download rather than guessing a directory.
var typeSubdirs = map[string]string{
	"checkpoint":       "checkpoints",
	"lora":             "loras",
	"locon":            "loras",
	"lycoris":          "loras",
	"vae":              "vae",
	"controlnet":       "controlnet",
	"textualinversion": "embeddings",
	"upscaler":         "upscale_models",
	"hypernetwork":     "hypernetworks",
}

// TypeSubdir maps a CivitAI model type to the ComfyUI models/ subfolder it
// belongs in. The second return is false for an unknown/unmappable type — the
// caller must then refuse the download (never guess a destination directory).
func TypeSubdir(civitaiType string) (string, bool) {
	sub, ok := typeSubdirs[strings.ToLower(strings.TrimSpace(civitaiType))]
	return sub, ok
}

// SafeModelDest computes the absolute path at which a model referenced as refName
// should be saved under root/subdir. It uses ONLY the basename of refName —
// stripping every directory component and normalizing both `/` and `\`
// separators — so a hostile reference such as "../../etc/passwd", "/abs/evil", or
// "seg-a/model.safetensors" collapses to its final path element and can NEVER
// escape root/subdir. It errors on an empty root/subdir or a reference whose
// basename is empty, ".", or "..".
func SafeModelDest(root, subdir, refName string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("empty comfy model root")
	}
	subdir = strings.TrimSpace(subdir)
	if subdir == "" {
		return "", errors.New("empty model subdirectory")
	}
	// Normalize backslashes to forward slashes so a Windows-style traversal is
	// collapsed on every platform, then take the slash-based basename.
	base := path.Base(strings.ReplaceAll(strings.TrimSpace(refName), "\\", "/"))
	if base == "" || base == "." || base == ".." || base == "/" {
		return "", fmt.Errorf("invalid model reference %q", refName)
	}
	dir := filepath.Join(root, subdir)
	dest := filepath.Join(dir, base)
	// Defense in depth: the joined result must be dir + exactly one path element.
	rel, err := filepath.Rel(dir, dest)
	if err != nil || rel != base || strings.ContainsRune(rel, filepath.Separator) {
		return "", fmt.Errorf("refusing to write outside %s", dir)
	}
	return dest, nil
}

// WriteModelStream streams src into destPath ATOMICALLY: it writes to a temp file
// in the SAME directory (so the finalizing rename is atomic on one filesystem),
// fsyncs, then renames into place. Behavior:
//
//   - Refuses to overwrite an existing destPath (returns ErrDestExists) — the
//     caller skips the download and runs with the file already there.
//   - Enforces a size cap: maxBytes<=0 uses defaultModelSizeCap. contentLength
//     (the advertised size, <=0 if unknown) is rejected up front when it exceeds
//     the cap; the streamed copy is INDEPENDENTLY bounded at cap+1 so a lying or
//     absent Content-Length still cannot exceed the cap.
//   - Best-effort free-disk pre-check when contentLength is known.
//   - Removes the temp file on ANY error path.
//
// It returns the number of bytes written.
func WriteModelStream(destPath string, src io.Reader, contentLength, maxBytes int64) (int64, error) {
	if maxBytes <= 0 {
		maxBytes = defaultModelSizeCap
	}
	if contentLength > maxBytes {
		return 0, fmt.Errorf("%w (%d bytes > cap %d)", ErrSizeCapExceeded, contentLength, maxBytes)
	}
	// Existing-file guard (defense in depth; the orchestrator also pre-checks so it
	// can skip the network fetch entirely).
	if _, err := os.Stat(destPath); err == nil {
		return 0, ErrDestExists
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("create model subdir: %w", err)
	}
	if contentLength > 0 {
		if free, ok := freeDiskBytes(dir); ok && free < contentLength+diskFreeMargin {
			return 0, fmt.Errorf("insufficient free disk space in %s (need ~%d bytes, have %d)", dir, contentLength, free)
		}
	}

	tmp, err := os.CreateTemp(dir, ".cm-download-*")
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		_ = tmp.Close() // no-op if already closed below
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	// Bound the copy at cap+1 so an over-cap body (understated/absent
	// Content-Length) is detected instead of written unbounded. Guard the +1
	// against int64 overflow (a maxBytes near MaxInt64 would wrap negative and make
	// LimitReader read nothing → a silent empty file); at that scale the +1
	// detection is moot, so fall back to maxBytes itself.
	limit := maxBytes + 1
	if limit <= 0 {
		limit = maxBytes
	}
	written, err := io.Copy(tmp, io.LimitReader(src, limit))
	if err != nil {
		return written, fmt.Errorf("stream download: %w", err)
	}
	if written > maxBytes {
		return written, fmt.Errorf("%w (>%d bytes)", ErrSizeCapExceeded, maxBytes)
	}
	if err := tmp.Sync(); err != nil {
		return written, fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return written, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, destPath); err != nil {
		return written, fmt.Errorf("finalize download: %w", err)
	}
	success = true
	return written, nil
}
