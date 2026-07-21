package civitai

import (
	"path/filepath"
	"strings"
)

// SanitizeComponent makes a string safe to use as a single path component:
// it strips path separators and other characters that are awkward or unsafe in
// file names across platforms, collapses whitespace, and trims dots/spaces from
// the ends. An empty or all-invalid input becomes "unknown".
func SanitizeComponent(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			b.WriteByte('_')
		case '\n', '\t', '\r':
			b.WriteByte(' ')
		default:
			if r < 0x20 {
				continue
			}
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	// collapse runs of spaces
	out = strings.Join(strings.Fields(out), " ")
	if out == "" {
		return "unknown"
	}
	// bound length so a pathological name can't blow the path limit
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

// DestPath builds the on-disk destination for a downloaded file:
//
//	<root>/<type>/<creator>/<model>/<versionName><ext>
//
// Every dynamic component is sanitized. The base name is the version name and
// the extension is taken from the API file name (so it is always correct even
// when the version name has none); an extension-less or empty file name falls
// back to the sanitized file name itself, then to ".bin".
func DestPath(root, modelType, creator, modelName, versionName, fileName string) string {
	base := SanitizeComponent(orUnknown(versionName))
	ext := filepath.Ext(fileName)
	name := base + ext
	switch {
	case base == "unknown":
		// No usable version name: prefer the real file name.
		if fn := SanitizeComponent(fileName); fn != "unknown" {
			name = fn
		}
	case ext == "":
		// A version name but no extension to borrow: prefer the real file name.
		if fn := SanitizeComponent(fileName); fn != "unknown" {
			name = fn
		} else {
			name = base
		}
	}
	return filepath.Join(
		root,
		SanitizeComponent(orUnknown(modelType)),
		SanitizeComponent(orUnknown(creator)),
		SanitizeComponent(orUnknown(modelName)),
		name,
	)
}

// SidecarBase returns the base path (destination path with its final extension
// removed) used to derive sidecar file names like "<base>.civitai.info" and
// "<base>.preview.png".
func SidecarBase(destPath string) string {
	ext := filepath.Ext(destPath)
	return strings.TrimSuffix(destPath, ext)
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
