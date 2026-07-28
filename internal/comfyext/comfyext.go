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
//   - the target root must be a real ComfyUI install (custom_nodes/ AND a ComfyUI
//     fingerprint — see rootMarkerFiles);
//   - it NEVER clobbers a directory it did not write (a marker file identifies
//     our own installs; anything else is refused);
//   - every written path is contained under the target directory;
//   - the write is staged OUTSIDE custom_nodes/ then renamed in, and an existing
//     install is moved aside rather than deleted, so neither a crash nor a failed
//     rename can leave an importable orphan or an empty hole (see Install);
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

	// stagingPrefix names the temporary directory an install is built in before it
	// is renamed into place.
	//
	// It MUST live directly under the ComfyUI ROOT, never under custom_nodes/: a
	// staging dir holds a COMPLETE __init__.py, and ComfyUI's loader does NOT skip
	// dot-directories under custom_nodes (it only skips __pycache__, non-.py files
	// and *.disabled). A staging dir orphaned by a kill/crash would therefore be
	// imported as a SECOND copy of this extension at the next start, and the second
	// copy's route registration makes aiohttp raise
	// "Added route will never be executed" — which takes the WHOLE ComfyUI down,
	// across every custom node. The root is on the same filesystem as
	// custom_nodes/, so the final rename stays atomic.
	stagingPrefix = ".civitai-manager-staging-"

	// backupPrefix names the temporary directory an existing install is renamed
	// ASIDE to during an upgrade, so a failure at any point can restore it. It also
	// lives under the root, for the same reason as stagingPrefix.
	backupPrefix = ".civitai-manager-backup-"

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

// rootMarkerFiles are files that, alongside custom_nodes/, identify a directory
// as the ComfyUI install ITSELF rather than merely a directory that happens to
// contain a custom_nodes/ folder.
//
// custom_nodes/ ALONE is not enough, and that is not hypothetical: on the
// development machine /…/fast/comfyui/ has a custom_nodes/ directory while the
// real install lives one level deeper at /…/fast/comfyui/ComfyUI/. Installing
// into the shallower directory writes a helper the running ComfyUI never loads —
// with no diagnostic at all, just "it didn't work".
var rootMarkerFiles = []string{"main.py", "folder_paths.py", "nodes.py"}

// rootMarkerDirs are directories that serve the same purpose as rootMarkerFiles.
var rootMarkerDirs = []string{"comfy"}

// rootFingerprint reports whether root carries at least one unambiguous ComfyUI
// install marker beside custom_nodes/.
func rootFingerprint(root string) bool {
	for _, name := range rootMarkerFiles {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	for _, name := range rootMarkerDirs {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// ValidateRoot reports whether root is a ComfyUI install we can install into: an
// existing absolute directory that has BOTH a writable custom_nodes/ directory
// AND at least one ComfyUI fingerprint (main.py / folder_paths.py / nodes.py /
// comfy/). Both halves are required — see rootMarkerFiles for why custom_nodes/
// alone silently installs into the wrong place.
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
	if !rootFingerprint(root) {
		return fmt.Errorf("%q has a %s/ directory but none of %s — it is probably the folder CONTAINING your ComfyUI install rather than the install itself; point comfy_root at the directory holding main.py",
			root, CustomNodesDir, strings.Join(append(append([]string{}, rootMarkerFiles...), "comfy/"), ", "))
	}
	// A probe write is the only honest writability check (permission bits lie
	// under ACLs / read-only mounts). It is removed immediately. It probes the
	// ROOT, which is where staging happens, and custom_nodes/, which is where the
	// final directory lands.
	for _, dir := range []string{root, cn} {
		probe, err := os.CreateTemp(dir, ".civitai-manager-probe-")
		if err != nil {
			return fmt.Errorf("%s is not writable: %w", dir, err)
		}
		name := probe.Name()
		_ = probe.Close()
		_ = os.Remove(name)
	}
	return nil
}

// LooksLikeRoot reports whether root is a plausible ComfyUI install: it has
// custom_nodes/ AND a ComfyUI fingerprint. It is the cheap, side-effect-free
// predicate used to decide whether a DERIVED root (the parent of
// comfy_model_path) is plausible — never to authorize a write.
func LooksLikeRoot(root string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, CustomNodesDir))
	if err != nil || !info.IsDir() {
		return false
	}
	return rootFingerprint(root)
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

// renameFn is os.Rename, indirected so a test can inject a failure at a chosen
// step and prove the swap leaves either the OLD install or the NEW one in place —
// never nothing.
var renameFn = os.Rename

// afterStageHook, when non-nil, runs after the new tree is fully staged and
// BEFORE anything is moved. Tests use it to assert the invariant at the exact
// vulnerable moment: nothing importable may exist under custom_nodes/ yet.
var afterStageHook func(stage string)

// Install writes (or updates in place) the helper under root/custom_nodes/.
//
// It refuses a root that is not a ComfyUI install and refuses to touch an
// existing directory that is not ours. An existing OUR-marker install is
// replaced, which is how an upgrade works.
//
// The swap is crash-conscious:
//
//   - the new tree is staged under the ROOT, never under custom_nodes/, so an
//     orphaned staging directory can never be imported by ComfyUI as a second
//     copy of this extension (which would abort ComfyUI's startup entirely);
//   - an existing install is renamed ASIDE rather than deleted, then the new tree
//     is renamed in, and only then is the old one deleted — so an interruption
//     leaves either the old install or the new one, never an empty hole;
//   - stale staging/backup directories from a previous killed run are swept first.
func Install(root string) (Status, error) {
	if err := ValidateRoot(root); err != nil {
		return Status{}, err
	}
	st := Inspect(root)
	if st.Foreign {
		return st, fmt.Errorf("refusing to overwrite %s: it already exists and was not installed by civitai-manager (no %s marker) — move or delete it yourself first", st.Dir, MarkerName)
	}

	// A previous run killed mid-install may have orphaned temp dirs. Sweep them
	// (including any left under custom_nodes/ by an older civitai-manager, which
	// staged there) BEFORE writing, so they can never be imported.
	SweepStale(root)

	stage, err := os.MkdirTemp(root, stagingPrefix)
	if err != nil {
		return st, fmt.Errorf("stage the extension: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
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
	if afterStageHook != nil {
		afterStageHook(stage)
	}

	// Move an existing install (ours — Foreign was refused above) aside instead of
	// deleting it, so a failure below can put it back.
	backup := ""
	if st.Installed {
		tmp, err := os.MkdirTemp(root, backupPrefix)
		if err != nil {
			return st, fmt.Errorf("back up the previous install: %w", err)
		}
		// MkdirTemp created the directory; rename needs the destination to not
		// exist, so drop it and reuse the (still unique) name.
		if err := os.Remove(tmp); err != nil {
			return st, fmt.Errorf("back up the previous install: %w", err)
		}
		if err := renameFn(st.Dir, tmp); err != nil {
			return st, fmt.Errorf("back up the previous install: %w", err)
		}
		backup = tmp
	}

	if err := renameFn(stage, st.Dir); err != nil {
		// Put the previous install back — an interrupted upgrade must never leave
		// the user with nothing installed.
		if backup != "" {
			if rerr := renameFn(backup, st.Dir); rerr != nil {
				return st, fmt.Errorf("install the extension: %w (and the previous install could not be restored from %s: %v)", err, backup, rerr)
			}
		}
		return st, fmt.Errorf("install the extension: %w", err)
	}
	committed = true
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return Inspect(root), nil
}

// SweepStale removes civitai-manager staging/backup leftovers from a killed
// install. It looks under BOTH the root (where staging lives now) and
// custom_nodes/ (where an older civitai-manager staged, and where an orphan is
// actively dangerous — ComfyUI imports dot-directories there). It is best-effort
// and never reports an error: a leftover it cannot remove must not block a repair
// install.
func SweepStale(root string) {
	if strings.TrimSpace(root) == "" {
		return
	}
	for _, dir := range []string{root, filepath.Join(root, CustomNodesDir)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, stagingPrefix) || strings.HasPrefix(name, backupPrefix) {
				_ = os.RemoveAll(filepath.Join(dir, name))
			}
		}
	}
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
