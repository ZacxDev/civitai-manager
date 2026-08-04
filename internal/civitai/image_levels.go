package civitai

import (
	"encoding/json"
	"errors"
)

// LeveledImage is an /api/v1/images item plus the NUMERIC maturity level the
// SDK's ImageItem does not carry.
//
// 🔴 WHY THIS TYPE EXISTS. sdk.ImageItem decodes `nsfwLevel`, which on this
// endpoint is a STRING label — and that label COLLAPSES the top two steps of
// CivitAI's scale. Live-probed 2026-07-31 on
// /api/v1/images?modelVersionId=3112728&limit=100&nsfw=X: 41 items came back at
// browsingLevel 8 and 40 at browsingLevel 16, and all 81 were labelled `"X"`.
// The numeric `browsingLevel` is the only field that distinguishes X from XXX,
// so anything that filters by maturity MUST read it.
//
// (The items also carry a bare `nsfw` boolean. It is coarser still — it cannot
// even separate PG-13 from XXX — so it is deliberately not decoded here.)
type LeveledImage struct {
	ImageItem
	// BrowsingLevel is CivitAI's numeric level: 1=PG, 2=PG-13, 4=R, 8=X, 16=XXX
	// (32=Blocked). ABSENT on some payloads — notably the INLINE
	// modelVersions[].images[] on /api/v1/models, whose `nsfwLevel` is already the
	// number — in which case it decodes to 0, which every maturity range treats as
	// unknown and omits.
	BrowsingLevel int `json:"browsingLevel"`
}

// LeveledImageSearchResult is DecodeLeveledImageSearch's result: the /api/v1/images
// items, each carrying its numeric browsingLevel.
type LeveledImageSearchResult struct {
	Items []LeveledImage `json:"items"`
	// Raw is the body it was decoded from, preserved so a caller that fetched it
	// can hand the exact same bytes to a cache.
	Raw []byte `json:"-"`
}

// DecodeLeveledImageSearch decodes a raw /api/v1/images response body into items
// that carry their numeric browsingLevel. It is the ONLY decode the community
// feed uses — both on the fetch path (SearchImages' .Raw) and on the cache path
// (the stored body) — so the two can never disagree about an item's level.
//
// A nil/empty or malformed body is an error; callers treat that as "no usable
// result" and fall through to their cache / empty state rather than rendering
// items whose maturity they cannot establish.
func DecodeLeveledImageSearch(raw []byte) (*LeveledImageSearchResult, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty image-search body")
	}
	var res LeveledImageSearchResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	res.Raw = raw
	return &res, nil
}
