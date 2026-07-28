// Package comfyext installs, inspects and removes the small civitai-manager
// ComfyUI helper extension.
//
// WHY IT EXISTS: ComfyUI has no supported "open this workflow" URL parameter in
// ANY frontend version — the only workflow-opening params are template/source/
// mode (and a cloud-only share). So after civitai-manager saves a workflow into
// ComfyUI's user store, there is no honest way to deep-link the editor at it.
// This helper adds that capability: a feature-detection route, a route that
// broadcasts a websocket "open this workflow" event to already-open tabs, and a
// tiny frontend script that honours ?cm_open=<path>.
//
// The extension source is EMBEDDED in the civitai-manager binary (this project
// ships as a single binary), and installing it is an explicit user action — it
// writes into the user's ComfyUI install, so it is never done automatically.
//
// Safety rules this package enforces:
//   - the target root must look like a real ComfyUI install (custom_nodes/ exists);
//   - it NEVER clobbers a directory it did not write (a marker file identifies
//     our own installs; anything else is refused);
//   - every written path is contained under the target directory;
//   - the write is atomic-ish (staged in a sibling temp dir, then renamed);
//   - Uninstall removes ONLY a directory carrying our marker.
//
// ComfyUI registers custom-node HTTP routes at startup only, so installing or
// removing this extension requires ONE ComfyUI restart to take effect.
package comfyext

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// ExtensionVersion is the shipped helper version. It is written into the
	// install marker and reported by the helper's /civitai-manager/ping route, so
	// an out-of-date install is detectable and an in-place update is safe. Keep it
	// in sync with EXTENSION_VERSION in extension/__init__.py.
	ExtensionVersion = "1"

	// DirName is the directory the helper is installed as, under custom_nodes/.
	// ComfyUI derives BOTH the python module name and the static asset URL from
	// it, so the frontend script lands at /extensions/civitai-manager/…
	DirName = "civitai-manager"

	// CustomNodesDir is ComfyUI's custom-node directory, relative to the root. Its
	// presence is what makes a directory recognizable as a ComfyUI install.
	CustomNodesDir = "custom_nodes"

	// MarkerName is the ownership marker written into an installed directory. Its
	// presence (with a matching tool field) is the ONLY thing that authorizes
	// overwriting or removing the directory.
	MarkerName = ".civitai-manager-install.json"

	// ToolName identifies this tool in the install marker AND in the helper's
	// /civitai-manager/ping response, so feature detection cannot be satisfied by
	// some unrelated server answering that path.
	ToolName = "civitai-manager"

	// markerTool is the marker's tool field — a foreign directory that happens to
	// carry a same-named file is still refused unless this matches.
	markerTool = ToolName

	// embedRoot is the embedded source directory holding the extension tree.
	embedRoot = "extension"

	dirPerm  fs.FileMode = 0o755
	filePerm fs.FileMode = 0o644
)

// extFS holds the embedded ComfyUI extension source. The all: prefix is required
// because the tree contains __init__.py, which the default embed patterns skip
// (leading underscore).
//
//go:embed all:extension
var extFS embed.FS

// ErrNotInstalled reports that the helper is not present under the given root.
var ErrNotInstalled = errors.New("the civitai-manager ComfyUI helper is not installed")

// Marker is the JSON ownership marker written at the root of an installed
// extension directory.
type Marker struct {
	Tool        string    `json:"tool"`
	Version     string    `json:"extension_version"`
	InstalledAt time.Time `json:"installed_at"`
}

// Status is what Inspect reports about a ComfyUI root.
type Status struct {
	// Root is the ComfyUI install root that was inspected.
	Root string
	// Dir is the absolute path the helper occupies (whether or not it exists).
	Dir string
	// Installed is true when Dir exists AND carries our marker.
	Installed bool
	// Foreign is true when Dir exists but is NOT ours (a plain directory, a file,
	// or a directory whose marker is missing/unreadable/for another tool). A
	// foreign Dir is never written to or removed.
	Foreign bool
	// Version is the installed marker's version (empty unless Installed).
	Version string
	// Outdated is true when Installed and Version differs from ExtensionVersion.
	Outdated bool
}

// Dir returns the absolute directory the helper occupies under root.
func Dir(root string) string {
	return filepath.Join(root, CustomNodesDir, DirName)
}

// ValidateRoot reports whether root looks like a ComfyUI install we can install
// into: a non-empty existing directory containing a writable custom_nodes/
// directory. It is deliberately the check the task demands — presence of
// custom_nodes/ — rather than a fingerprint of ComfyUI internals, which vary
// across packaged installs.
func ValidateRoot(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return errors.New("no ComfyUI root configured (set comfy_root)")
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("ComfyUI root %q must be an absolute path", root)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("ComfyUI root %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("ComfyUI root %q is not a directory", root)
	}
	cn := filepath.Join(root, CustomNodesDir)
	cnInfo, err := os.Stat(cn)
	if err != nil || !cnInfo.IsDir() {
		return fmt.Errorf("%q does not look like a ComfyUI install (no %s/ directory)", root, CustomNodesDir)
	}
	// A probe write is the only honest writability check (permission bits lie
	// under ACLs / read-only mounts). It is removed immediately.
	probe, err := os.CreateTemp(cn, ".civitai-manager-probe-")
	if err != nil {
		return fmt.Errorf("%s is not writable: %w", cn, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// LooksLikeRoot reports whether root exists and contains custom_nodes/. It is the
// cheap, side-effect-free predicate used to decide whether a DERIVED root (the
// parent of comfy_model_path) is plausible — never to authorize a write.
func LooksLikeRoot(root string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, CustomNodesDir))
	return err == nil && info.IsDir()
}

// DeriveRoot guesses the ComfyUI root from a configured comfy_model_path
// (…/ComfyUI/models → …/ComfyUI). It returns "" unless the parent actually
// looks like a ComfyUI install, so a models dir living somewhere else never
// produces a bogus root.
func DeriveRoot(modelPath string) string {
	modelPath = strings.TrimSpace(modelPath)
	if modelPath == "" {
		return ""
	}
	parent := filepath.Dir(filepath.Clean(modelPath))
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return ""
	}
	if !LooksLikeRoot(parent) {
		return ""
	}
	return parent
}

// Inspect reports what is currently installed under root. It does not require a
// valid ComfyUI root (an unusable root simply reports "not installed"), and it
// never writes anything.
func Inspect(root string) Status {
	st := Status{Root: root, Dir: Dir(root)}
	if strings.TrimSpace(root) == "" {
		return st
	}
	info, err := os.Lstat(st.Dir)
	if err != nil {
		return st // absent (or unreadable) → not installed, not foreign
	}
	if !info.IsDir() {
		st.Foreign = true // a file/symlink squatting the name is never ours
		return st
	}
	m, err := readMarker(st.Dir)
	if err != nil || m.Tool != markerTool {
		st.Foreign = true
		return st
	}
	st.Installed = true
	st.Version = m.Version
	st.Outdated = m.Version != ExtensionVersion
	return st
}

// Install writes (or updates in place) the helper under root/custom_nodes/.
//
// It refuses a root that does not look like a ComfyUI install and refuses to
// touch an existing directory that is not ours. An existing OUR-marker install is
// replaced, which is how an upgrade works. The new tree is staged in a sibling
// temp directory and renamed into place, so a failed write never leaves a
// half-written extension behind.
func Install(root string) (Status, error) {
	if err := ValidateRoot(root); err != nil {
		return Status{}, err
	}
	st := Inspect(root)
	if st.Foreign {
		return st, fmt.Errorf("refusing to overwrite %s: it already exists and was not installed by civitai-manager (no %s marker) — move or delete it yourself first", st.Dir, MarkerName)
	}

	parent := filepath.Join(root, CustomNodesDir)
	stage, err := os.MkdirTemp(parent, ".civitai-manager-staging-")
	if err != nil {
		return st, fmt.Errorf("stage the extension: %w", err)
	}
	staged := false
	defer func() {
		if !staged {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, dirPerm); err != nil {
		return st, fmt.Errorf("stage the extension: %w", err)
	}
	if err := writeTree(stage); err != nil {
		return st, err
	}
	if err := writeMarker(stage); err != nil {
		return st, err
	}

	// Replace only a directory we already own (Foreign was refused above).
	if st.Installed {
		if err := os.RemoveAll(st.Dir); err != nil {
			return st, fmt.Errorf("replace the previous install: %w", err)
		}
	}
	if err := os.Rename(stage, st.Dir); err != nil {
		return st, fmt.Errorf("install the extension: %w", err)
	}
	staged = true
	return Inspect(root), nil
}

// Uninstall removes the helper directory — but ONLY when it carries our marker.
// A foreign directory is refused, and an absent one reports ErrNotInstalled.
func Uninstall(root string) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("no ComfyUI root configured (set comfy_root)")
	}
	st := Inspect(root)
	if st.Foreign {
		return fmt.Errorf("refusing to remove %s: it was not installed by civitai-manager (no %s marker)", st.Dir, MarkerName)
	}
	if !st.Installed {
		return ErrNotInstalled
	}
	if err := os.RemoveAll(st.Dir); err != nil {
		return fmt.Errorf("remove the extension: %w", err)
	}
	return nil
}

// writeTree copies the embedded extension source into dst. Every destination is
// containment-checked against dst even though the source is our own embedded
// tree — the check is the invariant, not a reaction to untrusted input.
func writeTree(dst string) error {
	return fs.WalkDir(extFS, embedRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := relUnder(embedRoot, p)
		if err != nil {
			return err
		}
		target, err := safeJoin(dst, rel)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, dirPerm)
		}
		data, err := fs.ReadFile(extFS, p)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", p, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, filePerm); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	})
}

// relUnder returns p relative to base using SLASH semantics (embed.FS paths are
// always slash-separated, on every OS).
func relUnder(base, p string) (string, error) {
	if p == base {
		return ".", nil
	}
	prefix := base + "/"
	if !strings.HasPrefix(p, prefix) {
		return "", fmt.Errorf("embedded path %q escapes %q", p, base)
	}
	return strings.TrimPrefix(p, prefix), nil
}

// safeJoin joins a slash-separated relative path onto dir and proves the result
// stays inside dir. Any absolute path, "..", or escape is rejected.
func safeJoin(dir, rel string) (string, error) {
	if rel == "." || rel == "" {
		return dir, nil
	}
	if path.IsAbs(rel) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative", rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path %q escapes the extension directory", rel)
		}
	}
	target := filepath.Join(dir, filepath.FromSlash(rel))
	cleanDir := filepath.Clean(dir)
	if target != cleanDir && !strings.HasPrefix(target, cleanDir+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes %q", rel, dir)
	}
	return target, nil
}

func writeMarker(dir string) error {
	m := Marker{Tool: markerTool, Version: ExtensionVersion, InstalledAt: time.Now().UTC()}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the install marker: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, MarkerName), append(data, '\n'), filePerm); err != nil {
		return fmt.Errorf("write the install marker: %w", err)
	}
	return nil
}

func readMarker(dir string) (Marker, error) {
	var m Marker
	data, err := os.ReadFile(filepath.Join(dir, MarkerName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	return m, nil
}
