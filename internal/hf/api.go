package hf

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// repoSummary is the subset of a /api/models search item we use.
type repoSummary struct {
	ID        string `json:"id"` // "Bingsu/adetailer"
	Downloads int    `json:"downloads"`
	Likes     int    `json:"likes"`
	Private   bool   `json:"private"`
	Gated     any    `json:"gated"` // false | "auto" | "manual"
}

// modelInfo is the subset of a /api/models/{repo} response we use. `gated` is a
// polymorphic field: the JSON `false` or one of the strings "auto"/"manual".
type modelInfo struct {
	ID        string          `json:"id"`
	SHA       string          `json:"sha"`
	Private   bool            `json:"private"`
	Gated     json.RawMessage `json:"gated"`
	Downloads int             `json:"downloads"`
	Likes     int             `json:"likes"`
	Tags      []string        `json:"tags"`
	CardData  struct {
		License string `json:"license"`
	} `json:"cardData"`
}

// gatedOrPrivate reports whether the repo is gated (gated != false) or private —
// either blocks anonymous auto-download.
func (mi *modelInfo) gatedOrPrivate() bool {
	if mi.Private {
		return true
	}
	return gatedIsTrue(mi.Gated)
}

// gatedIsTrue decodes HF's polymorphic `gated` value: JSON false → not gated;
// "auto"/"manual" (any non-false, non-empty) → gated. An absent/unknown value is
// treated as NOT gated (the model-info of a public repo omits nothing here, and
// failing open on the flag would over-block; the resolve HEAD 401 is the real gate).
func gatedIsTrue(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "false", "null":
		return false
	case "true":
		return true
	}
	// A quoted string like "auto" / "manual".
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return strings.TrimSpace(str) != "" && !strings.EqualFold(str, "false")
	}
	return false
}

// license extracts a best-effort license label from cardData or the license:* tag.
func (mi *modelInfo) license() string {
	if l := strings.TrimSpace(mi.CardData.License); l != "" {
		return l
	}
	for _, t := range mi.Tags {
		if strings.HasPrefix(t, "license:") {
			return strings.TrimPrefix(t, "license:")
		}
	}
	return ""
}

// treeEntry is one file/dir in a /tree response.
type treeEntry struct {
	Type string `json:"type"` // "file" | "directory"
	Path string `json:"path"`
	Size int64  `json:"size"`
	OID  string `json:"oid"` // git blob sha1 (non-LFS) — NOT a content sha256
	LFS  struct {
		OID  string `json:"oid"` // git-LFS oid == the file content sha256
		Size int64  `json:"size"`
	} `json:"lfs"`
}

// searchModels queries /api/models?search=... ranked by downloads and returns the
// candidate repos (sorted descending by downloads).
func (c *Client) searchModels(ctx context.Context, query string, limit int) ([]repoSummary, error) {
	q := url.Values{}
	q.Set("search", query)
	q.Set("sort", "downloads")
	q.Set("direction", "-1")
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	u := c.base + "/api/models?" + q.Encode()
	var repos []repoSummary
	if err := c.getJSON(ctx, u, &repos); err != nil {
		return nil, err
	}
	sortReposByDownloads(repos)
	return repos, nil
}

// getModelInfo fetches /api/models/{repo}.
func (c *Client) getModelInfo(ctx context.Context, repo string) (*modelInfo, error) {
	u := c.base + "/api/models/" + repo
	var mi modelInfo
	if err := c.getJSON(ctx, u, &mi); err != nil {
		return nil, err
	}
	return &mi, nil
}

// getTree fetches the recursive file tree WITH hashes for a repo revision.
func (c *Client) getTree(ctx context.Context, repo, revision string) ([]treeEntry, error) {
	u := c.base + "/api/models/" + repo + "/tree/" + url.PathEscape(revision) + "?recursive=true"
	var tree []treeEntry
	if err := c.getJSON(ctx, u, &tree); err != nil {
		return nil, err
	}
	return tree, nil
}
