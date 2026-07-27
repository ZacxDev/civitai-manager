package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
)

// LocalModelMeta is the enriched metadata for one installed model file: its
// CivitAI linkage (from local_files) plus the name / base-model / preview image
// decoded from the cached model-detail JSON (model_cache.raw). It powers a rich
// "installed model" substitute card WITHOUT any network call. NSFWLevel carries the
// numeric CivitAI level of the preview image so the caller can blur/omit it per the
// current NSFW mode; NSFWLevelKnown is false when the preview had no parseable level
// (the caller then treats it as unknown → fail-closed).
type LocalModelMeta struct {
	Basename       string
	ModelID        int
	VersionID      int
	Name           string
	BaseModel      string
	ImageURL       string
	NSFWLevel      int
	NSFWLevelKnown bool
}

// LocalModelMetaByBasenames returns, keyed by LOWERCASED basename, the enriched
// metadata for each requested basename that maps to EXACTLY ONE CivitAI-linked
// installed file with a cached model-detail snapshot. It is read-only.
//
// Basenames are matched against local_files (a workflow candidate carries only a
// bare — possibly subfolder-prefixed — filename). A basename present under multiple
// files that disagree on their model linkage is AMBIGUOUS and omitted (the caller
// renders a minimal card rather than guessing). A basename with no CivitAI linkage,
// or whose model has no cached model_cache row, is likewise absent from the result.
// An empty input returns an empty (non-nil) map.
func (s *Store) LocalModelMetaByBasenames(names []string) (map[string]LocalModelMeta, error) {
	out := map[string]LocalModelMeta{}
	want := map[string]bool{}
	for _, n := range names {
		if b := lowerBasename(n); b != "" {
			want[b] = true
		}
	}
	if len(want) == 0 {
		return out, nil
	}

	// Load model-linked files once; the exact basename match happens in Go.
	rows, err := s.db.Query(`SELECT path, model_id, version_id FROM local_files WHERE model_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type link struct {
		modelID   int
		versionID int
		ambiguous bool
	}
	links := map[string]*link{}
	for rows.Next() {
		var (
			path      string
			modelID   int
			versionID sql.NullInt64
		)
		if err := rows.Scan(&path, &modelID, &versionID); err != nil {
			return nil, err
		}
		b := strings.ToLower(filepath.Base(path))
		if !want[b] {
			continue
		}
		vid := 0
		if versionID.Valid {
			vid = int(versionID.Int64)
		}
		if cur, ok := links[b]; ok {
			if cur.modelID != modelID {
				cur.ambiguous = true // collision on differing model → drop
			}
			continue
		}
		links[b] = &link{modelID: modelID, versionID: vid}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Decode each needed model_cache snapshot once (memoized by model id).
	cache := map[int]*ModelCacheEntry{}
	for b, l := range links {
		if l.ambiguous {
			continue
		}
		ent, ok := cache[l.modelID]
		if !ok {
			ent, err = s.GetModelCache(l.modelID)
			if err != nil {
				return nil, err
			}
			cache[l.modelID] = ent
		}
		if ent == nil {
			continue // no cached model-detail → caller renders a minimal card
		}
		meta := LocalModelMeta{Basename: b, ModelID: l.modelID, VersionID: l.versionID, Name: ent.Name}
		meta.BaseModel, meta.ImageURL, meta.NSFWLevel, meta.NSFWLevelKnown =
			decodeModelCacheMeta(ent.Raw, l.versionID)
		out[b] = meta
	}
	return out, nil
}

// lowerBasename returns the lowercased basename of a (possibly subfolder-prefixed,
// possibly backslash-separated) filename.
func lowerBasename(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	if name == "" {
		return ""
	}
	return strings.ToLower(filepath.Base(name))
}

// decodeModelCacheMeta extracts the base model, a preview image URL, and that
// image's numeric nsfwLevel from a cached model-detail body (the GetModel JSON).
// It prefers the version linked to the local file (versionID); if that version has
// no usable image it falls back to the first image across any version. baseModel is
// always taken from the linked/primary version.
func decodeModelCacheMeta(raw []byte, versionID int) (baseModel, imageURL string, nsfwLevel int, nsfwKnown bool) {
	if len(raw) == 0 {
		return "", "", 0, false
	}
	var body struct {
		ModelVersions []struct {
			ID        int    `json:"id"`
			BaseModel string `json:"baseModel"`
			Images    []struct {
				URL       string          `json:"url"`
				NSFWLevel json.RawMessage `json:"nsfwLevel"`
			} `json:"images"`
		} `json:"modelVersions"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.ModelVersions) == 0 {
		return "", "", 0, false
	}
	// Locate the linked version (else positional [0], the primary).
	idx := 0
	if versionID > 0 {
		for i, v := range body.ModelVersions {
			if v.ID == versionID {
				idx = i
				break
			}
		}
	}
	baseModel = body.ModelVersions[idx].BaseModel

	// Preview image: prefer the linked version, then any version, in order.
	order := append([]int{idx}, otherIndices(len(body.ModelVersions), idx)...)
	for _, vi := range order {
		for _, im := range body.ModelVersions[vi].Images {
			if strings.TrimSpace(im.URL) == "" {
				continue
			}
			imageURL = im.URL
			if lvl, ok := decodeIntRaw(im.NSFWLevel); ok {
				nsfwLevel, nsfwKnown = lvl, true
			}
			return baseModel, imageURL, nsfwLevel, nsfwKnown
		}
	}
	return baseModel, imageURL, nsfwLevel, nsfwKnown
}

// otherIndices returns [0..n) with `skip` removed, preserving order.
func otherIndices(n, skip int) []int {
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if i != skip {
			out = append(out, i)
		}
	}
	return out
}

// decodeIntRaw decodes a raw JSON number to an int. An absent/null/non-integer
// value yields (0, false).
func decodeIntRaw(raw json.RawMessage) (int, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}
