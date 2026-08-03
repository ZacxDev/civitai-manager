package web

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/comfyext"
)

// comfyModelPathSettingKey is the settings row holding a comfy_model_path the
// user set from the UI (the run panel's "set it up" step).
//
// 🔴 STORAGE DECISION — a settings row, NOT the YAML config file.
//
// The alternative considered was writing comfy_model_path into
// ~/.config/civitai-manager/config.yaml. It was rejected on three counts, in
// order of weight:
//
//  1. The operator has NO config.yaml at all. Creating one is a surprising,
//     non-obvious side effect on their machine, and nothing in this app writes
//     user config today — the file is read-only input to config.Load.
//  2. Config is resolved ONCE at process start (config.Load → cli → web.Config),
//     so a write would not take effect until a restart. A setting the user just
//     typed that visibly does nothing until they restart the server is worse
//     than no setting.
//  3. A settings row is reversible in-app and is the pattern this codebase
//     already uses for exactly this shape of state: `maturity_range`, the cloud
//     toggle, and — the closest precedent — `scan_dirs`, which already persists
//     USER-CHOSEN FILESYSTEM PATHS in the database rather than in YAML.
//
// The config-file-wins precedence below is lifted verbatim from Config.ComfyCloud's
// documented rule ("nil means the config FILE said nothing, so the DB-stored web
// toggle governs"): an explicit comfy_model_path in YAML or on the command line
// stays authoritative and the UI must never silently override it.
const comfyModelPathSettingKey = "comfy_model_path"

// comfyModelPath is the EFFECTIVE ComfyUI models/ root: the configured value when
// the config file or a flag set one, else the value the user saved from the UI.
//
// 🔴 Every read of the models root goes through here — there is deliberately no
// remaining `s.cfg.ComfyModelPath` read outside this file. A predicate duplicated
// across call sites regenerates the same bug at every site, and this one decides
// where real gigabytes get written: a site still reading cfg directly would keep
// reporting "not configured" after the user configured it, silently disabling the
// install path for them while its neighbours worked.
//
// It does NOT validate. Validation happens once, at the point of WRITE
// (handleSetComfyModelPath), because an unvalidated value never reaches the row;
// comfyDownloadEligible re-checks existence per poll exactly as it did before.
func (s *Server) comfyModelPath() string {
	if p := strings.TrimSpace(s.cfg.ComfyModelPath); p != "" {
		return p
	}
	v, err := s.store.GetSettingDefault(comfyModelPathSettingKey, "")
	if err != nil {
		// A settings read failure must not look like "the user configured a path
		// somewhere else". Fail CLOSED to "unset": the CTA renders disabled with the
		// setup step, which is recoverable, rather than routing a download at a path
		// we could not read.
		s.log.Warn("read comfy_model_path setting", "err", err)
		return ""
	}
	return strings.TrimSpace(v)
}

// comfyRoot is the EFFECTIVE ComfyUI install root (the folder holding
// custom_nodes/): the configured value when set, else derived from the effective
// models path's parent.
//
// The derivation is the SAME rule config.Load applies (comfyext.DeriveRoot, which
// only accepts a parent that actually looks like a ComfyUI install), so a root set
// up through the UI behaves identically to one set up in YAML — including for the
// manual `git clone` command, which otherwise prints a /path/to/ComfyUI
// placeholder the user has to hand-edit.
func (s *Server) comfyRoot() string {
	if r := strings.TrimSpace(s.cfg.ComfyRoot); r != "" {
		return r
	}
	return comfyext.DeriveRoot(s.comfyModelPath())
}

// validateComfyModelPath checks a user-supplied models root before it is
// persisted, and returns a plain-language reason when it is unusable.
//
// 🔴 It is the ONLY validation gate for the settings-stored value. config.Load
// validates the YAML one at startup (ValidateWritableDir); a value arriving from
// the UI has never been through that, so it is checked HERE and a bad one is
// never written. Failing at write time means the stored value is always one that
// was usable at least once, and the per-poll os.Stat in comfyDownloadEligible
// covers it going away later.
//
// The path must be ABSOLUTE: a relative path would resolve against the server
// process's working directory, which the user cannot see and which differs
// between a systemd unit and a shell. That is a silent wrong-destination bug, so
// it is refused rather than cleaned up.
func validateComfyModelPath(raw string) (string, string) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", "Enter the full path to ComfyUI's models folder."
	}
	if strings.ContainsRune(p, 0) {
		return "", "That path contains an invalid character."
	}
	if !filepath.IsAbs(p) {
		return "", "Enter an absolute path (one starting with /), not a relative one."
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	if err != nil {
		return "", "There is nothing at " + p + " that this server can read."
	}
	if !fi.IsDir() {
		return "", p + " is a file, not a folder. Point this at ComfyUI's models folder."
	}
	if err := writableDirProbe(p); err != nil {
		return "", "This server cannot write into " + p + ". Check its permissions."
	}
	return p, ""
}

// writableDirProbe confirms the directory is writable by actually creating and
// removing a temp file in it.
//
// A stat-only check is not enough HERE, and that asymmetry with
// comfyDownloadEligible is deliberate: this runs ONCE, on an explicit user
// action, where being wrong means the user is told "saved" and then every future
// download fails with a permission error they cannot connect to this step. The
// per-poll path stays stat-only because a write probe there would churn a temp
// file in the models directory every 1-2 seconds (see comfyDownloadEligible).
func writableDirProbe(dir string) error {
	f, err := os.CreateTemp(dir, ".civitai-manager-write-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}
