package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// NSFW display modes (persisted under nsfwSettingKey). blur is the default.
//
// NSFWHide is RETAINED as a constant (and the two server-side omit branches that
// key off it are kept) so the "omit NSFW server-side" capability still exists per
// the CLAUDE.md invariant — but the navbar toggle NO LONGER OFFERS hide (it is a
// 2-state Blur ⇄ Show control). normalizeNSFWMode migrates any stored "hide" to
// blur, so no user is stuck on hide and mode == NSFWHide is unreachable in
// practice (the omit branches are inert but preserved).
const (
	NSFWHide       = "hide"
	NSFWBlur       = "blur"
	NSFWShow       = "show"
	nsfwSettingKey = "nsfw_display"
)

// normalizeNSFWMode coerces a stored/submitted value to a known mode, defaulting
// to blur (the safe default: NSFW images are obscured until the user reveals one).
// A stored "hide" is MIGRATED to blur: the toggle dropped the hide state, so a
// previously-persisted hide now reads as blur everywhere (the server-side omit
// branches keyed on NSFWHide are thus never reached, but stay in place as an
// inert, preserved capability — see the const block above).
func normalizeNSFWMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case NSFWShow:
		return NSFWShow
	default: // blur (the safe default) — and "hide" migrates here too
		return NSFWBlur
	}
}

// CivitAI encodes an image's nsfwLevel as a NUMBER (a bitmask-ish severity) on
// the inline modelVersions[].images[] payload: 1=None/PG, 2=Soft/PG-13,
// 4=Mature/R, 8=X, 16=XX, 32=XXX. nsfwSafeLevel is the highest level rendered in
// the clear — only None/PG (1, and the never-observed 0) is safe; everything at
// Soft (2) and above is treated NSFW.
const nsfwSafeLevel = 1

// nsfwLevelUnknown is the sentinel the image parser assigns when an image's
// nsfwLevel is ABSENT or not an integer. It is above every real level so the
// blur/hide gate FAILS CLOSED: an image with no/garbage level is blurred (blur
// mode) and omitted (hide mode) rather than rendered un-obscured.
const nsfwLevelUnknown = 99

// isNSFWLevel reports whether a numeric CivitAI nsfwLevel should be treated as
// NSFW. Fail-closed: only an explicitly-safe level (<= nsfwSafeLevel) is safe;
// Soft (2) and above — and the nsfwLevelUnknown sentinel for an absent/garbage
// level — are NSFW.
func isNSFWLevel(level int) bool { return level > nsfwSafeLevel }

// galleryImage is one showcase image sourced from a model version's INLINE
// images[] (already present in the GetModel / GetModelVersion raw JSON) — not
// from a separate /api/v1/images call. NSFWLevel is the numeric CivitAI level
// (nsfwLevelUnknown when absent/unparseable). Meta is the flat generation
// metadata object, decoded best-effort at render time.
type galleryImage struct {
	URL       string
	NSFWLevel int
	Width     int
	Height    int
	Meta      json.RawMessage
	// Type is the media kind from the civitai payload ("image" or "video"; ""
	// when absent). A "video" tile still renders a still poster thumbnail (via
	// civitaiThumbURL, which forces anim=false), but the lightbox plays the
	// original as a <video> and the tile carries a ▶ badge + data-video marker.
	Type string
}

// isVideoType reports whether a media Type string denotes a video (case- and
// whitespace-insensitive). Centralized so the tile, lightbox marker, and parsers
// agree on what counts as a video.
func isVideoType(t string) bool {
	return strings.EqualFold(strings.TrimSpace(t), "video")
}

// rawInlineImage mirrors one object of a version's inline images[] array. The
// numeric-ish nsfwLevel is captured as raw JSON (not int) so an absent or
// non-integer value can be detected and mapped to nsfwLevelUnknown (fail closed)
// rather than silently decoding to 0 (which would read as safe).
type rawInlineImage struct {
	URL       string          `json:"url"`
	NSFWLevel json.RawMessage `json:"nsfwLevel"`
	Type      string          `json:"type"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Meta      json.RawMessage `json:"meta"`
}

// toGalleryImages converts parsed inline-image objects to galleryImage values,
// mapping each nsfwLevel to its numeric level (fail-closed to nsfwLevelUnknown
// when absent/unparseable) and dropping entries with no URL.
func toGalleryImages(raws []rawInlineImage) []galleryImage {
	var out []galleryImage
	for _, ri := range raws {
		if strings.TrimSpace(ri.URL) == "" {
			continue
		}
		out = append(out, galleryImage{
			URL:       ri.URL,
			NSFWLevel: parseNSFWLevel(ri.NSFWLevel),
			Width:     ri.Width,
			Height:    ri.Height,
			Meta:      ri.Meta,
			Type:      ri.Type,
		})
	}
	return out
}

// parseNSFWLevel decodes a raw nsfwLevel value to its integer level. An absent
// (empty/null) or non-integer value → nsfwLevelUnknown (fail closed).
func parseNSFWLevel(raw json.RawMessage) int {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nsfwLevelUnknown
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return nsfwLevelUnknown
	}
	return n
}

// searchImageCap bounds how many showcase images a single search card renders.
const searchImageCap = 8

// rawSearchImage mirrors one modelVersions[].images[] object in a SearchModels
// response. It captures the media Type ("image"/"video"); videos are INCLUDED —
// the tile renders a still poster (civitaiThumbURL forces anim=false) and the
// lightbox plays the original — so a video-only model still shows a card.
type rawSearchImage struct {
	URL       string          `json:"url"`
	NSFWLevel json.RawMessage `json:"nsfwLevel"`
	Type      string          `json:"type"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Meta      json.RawMessage `json:"meta"`
}

// parseSearchImages extracts, per model id, a showcase-image list from a
// SearchModels raw response body (the SDK's typed ModelListItem carries no
// images, but they are present in res.Raw). It scans the model's versions in
// order and uses the FIRST version that yields at least one usable image — so a
// model whose first version has no images still shows later-version images.
// Videos are INCLUDED (the tile renders a still poster; the lightbox plays the
// original), so a video-only model still gets a card. Each model's list is capped
// at searchImageCap. This is a MANAGER-SIDE parse of data already fetched — it
// makes no extra API call. Returns a non-nil (possibly empty) map; a model with
// no usable images across any version is simply absent from it.
func parseSearchImages(raw []byte) map[int][]galleryImage {
	out := map[int][]galleryImage{}
	if len(raw) == 0 {
		return out
	}
	var body struct {
		Items []struct {
			ID            int `json:"id"`
			ModelVersions []struct {
				Images []rawSearchImage `json:"images"`
			} `json:"modelVersions"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return out
	}
	for _, it := range body.Items {
		// Show the creator's PRIMARY version's showcase — modelVersions[0], the
		// lowest-`index` version, which is exactly what the model detail page
		// defaults to (so a card and its detail page agree). Scan to the next
		// version ONLY when the primary carries no images, so the card is never
		// empty. Images keep the creator's order; videos are included (poster tile).
		var imgs []galleryImage
		for _, ver := range it.ModelVersions {
			for _, ri := range ver.Images {
				if strings.TrimSpace(ri.URL) == "" {
					continue
				}
				imgs = append(imgs, galleryImage{
					URL:       ri.URL,
					NSFWLevel: parseNSFWLevel(ri.NSFWLevel),
					Width:     ri.Width,
					Height:    ri.Height,
					Meta:      ri.Meta,
					Type:      ri.Type,
				})
				if len(imgs) >= searchImageCap {
					break
				}
			}
			if len(imgs) > 0 {
				break // primary (or first non-empty) version wins
			}
		}
		if len(imgs) > 0 {
			out[it.ID] = imgs
		}
	}
	return out
}

// parseVersionImages sources the showcase gallery from inline image data already
// fetched with the model — NEVER a separate /api/v1/images call. It prefers the
// selected version's own raw JSON (GetModelVersion) top-level images[]; when that
// carries none, it falls back to the matching version object inside the model's
// raw JSON (GetModel) modelVersions[]. Returns nil (not an error) when neither
// has any inline images.
func parseVersionImages(versionRaw, modelRaw []byte, versionID int) []galleryImage {
	if imgs := parseInlineImages(versionRaw); len(imgs) > 0 {
		return imgs
	}
	return parseModelVersionImages(modelRaw, versionID)
}

// cardCarouselImages extracts a suggestion card's showcase images from a cached
// GetModel raw body, using the SAME inline-image path the model DETAIL page uses
// (parseVersionImages against the model raw with versionID 0 → the primary
// version's images). The result is capped at searchImageCap so a card renders the
// same number of tiles a search card does. Returns nil (never an error) when the
// model carries no inline images.
func cardCarouselImages(modelRaw []byte) []galleryImage {
	imgs := parseVersionImages(nil, modelRaw, 0)
	if len(imgs) > searchImageCap {
		imgs = imgs[:searchImageCap]
	}
	return imgs
}

// parseInlineImages extracts a top-level images[] array from a raw JSON body
// (a version detail body). Returns nil when absent/unparseable.
func parseInlineImages(raw []byte) []galleryImage {
	if len(raw) == 0 {
		return nil
	}
	var body struct {
		Images []rawInlineImage `json:"images"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	return toGalleryImages(body.Images)
}

// parseModelVersionImages finds the version whose id == versionID inside a model
// detail raw body's modelVersions[] and returns its inline images[]. When
// versionID is 0 (no selection) it uses the first listed version. Returns nil
// when the version or its images are absent.
func parseModelVersionImages(modelRaw []byte, versionID int) []galleryImage {
	if len(modelRaw) == 0 {
		return nil
	}
	var body struct {
		ModelVersions []struct {
			ID     int              `json:"id"`
			Images []rawInlineImage `json:"images"`
		} `json:"modelVersions"`
	}
	if err := json.Unmarshal(modelRaw, &body); err != nil {
		return nil
	}
	for _, ver := range body.ModelVersions {
		if versionID == 0 || ver.ID == versionID {
			return toGalleryImages(ver.Images)
		}
	}
	return nil
}

// modelDetailView bundles everything the rich model page renders. Any of the
// optional pieces (Version, Images) may be zero if the corresponding API call
// failed or the data genuinely carries none — the page degrades gracefully
// rather than erroring.
type modelDetailView struct {
	Model             *civitai.ModelDetail
	Description       string // raw author HTML; sanitized at render time
	SelectedVersionID int
	Version           *civitai.ModelVersionDetail
	PublishedAt       string
	// LastUpdated is the newest modelVersions[].publishedAt across ALL versions
	// (parsed from the GetModel raw), used for the header's "Updated X ago". Zero
	// when no version carries a parseable date — the stat is then omitted.
	LastUpdated time.Time
	Images      []galleryImage
	NSFWMode    string
	// LocalVersionIDs is the set of this model's version ids the user has locally
	// (derived from local files), used to badge owned versions in the version list.
	LocalVersionIDs map[int]bool
	// loadErr carries the model-load failure (used only to classify the HTTP
	// status: a not-found model → 404, anything else → 502).
	loadErr error
}

// parseModelDescription extracts the `description` field from a raw model-detail
// JSON body. ModelDetail does not carry it as a typed field, so it is read from
// the raw bytes GetModel returns. Returns "" when absent.
func parseModelDescription(raw []byte) string {
	var body struct {
		Description string `json:"description"`
	}
	_ = json.Unmarshal(raw, &body)
	return body.Description
}

// parsePublishedAt best-effort reads a version's `publishedAt` timestamp from its
// raw JSON body (ModelVersionDetail does not type it). Returns "" when absent.
func parsePublishedAt(raw []byte) string {
	var body struct {
		PublishedAt string `json:"publishedAt"`
	}
	_ = json.Unmarshal(raw, &body)
	return strings.TrimSpace(body.PublishedAt)
}

// versionPublishedDate extracts the publish DATE (YYYY-MM-DD) of the given version
// id from a raw GetModel body's modelVersions[]. It is defensive: an absent field,
// undecodable bytes, or a missing version all yield "" (the popover then omits the
// date). The ISO timestamp (e.g. "2023-05-01T12:00:00.000Z") is truncated to its
// date prefix without pulling in a time parse, so a malformed value never panics.
func versionPublishedDate(raw []byte, versionID int) string {
	if len(raw) == 0 || versionID == 0 {
		return ""
	}
	var body struct {
		ModelVersions []struct {
			ID          int    `json:"id"`
			PublishedAt string `json:"publishedAt"`
		} `json:"modelVersions"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	for _, v := range body.ModelVersions {
		if v.ID != versionID {
			continue
		}
		p := strings.TrimSpace(v.PublishedAt)
		if len(p) >= 10 && p[4] == '-' && p[7] == '-' {
			return p[:10]
		}
		return p
	}
	return ""
}

// maxPublishedTime parses each publishedAt string as RFC3339 and returns the
// newest as a time.Time (zero when none parse). Empty/unparseable entries are
// skipped — defensive, never panics on malformed data. civitai timestamps carry
// fractional seconds (e.g. "2023-05-01T12:00:00.000Z"), which RFC3339 parses.
func maxPublishedTime(publishedAts []string) time.Time {
	var newest time.Time
	for _, s := range publishedAts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			continue
		}
		if t.After(newest) {
			newest = t
		}
	}
	return newest
}

// newestVersionPublishedAt scans a GetModel raw body's modelVersions[].publishedAt
// and returns the newest as a real time.Time (zero when the raw is empty,
// undecodable, or carries no parseable date). Unlike versionPublishedDate (which
// truncates to YYYY-MM-DD without parsing), this yields a time.Time for a
// relative "Updated X ago" render.
func newestVersionPublishedAt(raw []byte) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var body struct {
		ModelVersions []struct {
			PublishedAt string `json:"publishedAt"`
		} `json:"modelVersions"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return time.Time{}
	}
	ats := make([]string, 0, len(body.ModelVersions))
	for _, v := range body.ModelVersions {
		ats = append(ats, v.PublishedAt)
	}
	return maxPublishedTime(ats)
}

// modelUpdateInfo is the per-model "last updated" summary derived from a
// SearchModels raw body: the newest version's publish time plus that version's
// NAME (both feed the search card's "Updated X ago" hover popover). Name is an
// untrusted civitai string — escape it at render time.
type modelUpdateInfo struct {
	At   time.Time
	Name string
	// VersionID is the newest version's id, used to deeplink the popover's
	// "Latest version: {name}" line to /models/{id}?version={VersionID}. Zero when
	// unknown (the line then renders as plain text, not a link).
	VersionID int
}

// newestVersionInfoByModel scans a SearchModels raw body and returns, per model
// id, the newest modelVersions[].publishedAt (as a time.Time) together with that
// version's name. A model with no parseable date is absent from the (non-nil) map.
// Defensive: an empty/undecodable raw yields an empty map, never a panic.
func newestVersionInfoByModel(raw []byte) map[int]modelUpdateInfo {
	out := map[int]modelUpdateInfo{}
	if len(raw) == 0 {
		return out
	}
	var body struct {
		Items []struct {
			ID            int `json:"id"`
			ModelVersions []struct {
				ID          int    `json:"id"`
				Name        string `json:"name"`
				PublishedAt string `json:"publishedAt"`
			} `json:"modelVersions"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return out
	}
	for _, it := range body.Items {
		var newest time.Time
		var name string
		var versionID int
		for _, v := range it.ModelVersions {
			s := strings.TrimSpace(v.PublishedAt)
			if s == "" {
				continue
			}
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				continue
			}
			if t.After(newest) {
				newest = t
				name = v.Name
				versionID = v.ID
			}
		}
		if !newest.IsZero() {
			out[it.ID] = modelUpdateInfo{At: newest, Name: name, VersionID: versionID}
		}
	}
	return out
}

// isoDatePrefix truncates an ISO-8601 timestamp to its YYYY-MM-DD date prefix,
// defensively — an input that is not shaped like a date is returned unchanged and
// nothing ever panics.
func isoDatePrefix(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[:10]
	}
	return s
}

// updatedPopBody builds the "Updated X ago" hover-popover child (.cm-updated-pop),
// mirroring .cm-vstatus-pop: an absolute "Updated {date}" title line, then
// "Latest version: {name}" and "Published {date}" lines shown only when available
// (each omitted gracefully when empty). versionName/versionDate are untrusted
// civitai strings — g.Text escapes every one; absDate is server-formatted (trusted).
// When modelID and versionID are both > 0 the "Latest version: {name}" line is a
// deeplink to that version's detail page (/models/{id}?version={vid}); otherwise it
// is plain text. The link stops click propagation so it navigates even though it
// lives inside the JS-hover-controlled popover wrapper.
func updatedPopBody(modelID, versionID int, absDate, versionName, versionDate string) g.Node {
	rows := []g.Node{
		h.Div(h.Class("cm-updated-title"), g.Text("Updated "+absDate)),
	}
	if strings.TrimSpace(versionName) != "" {
		if modelID > 0 && versionID > 0 {
			rows = append(rows, h.Div(h.A(
				h.Href(fmt.Sprintf("/models/%d?version=%d", modelID, versionID)),
				h.Class("text-indigo-300 hover:text-indigo-200"),
				g.Attr("onclick", "event.stopPropagation()"),
				g.Text("Latest version: "+versionName),
			)))
		} else {
			rows = append(rows, h.Div(g.Text("Latest version: "+versionName)))
		}
	}
	if strings.TrimSpace(versionDate) != "" {
		rows = append(rows, h.Div(h.Class("cm-updated-date"), g.Text("Published "+versionDate)))
	}
	return h.Span(h.Class("cm-updated-pop"), g.Attr("role", "tooltip"), g.Group(rows))
}

// updatedHeaderStat renders the model header's "Updated: X ago" stat as a
// hover/focus popover trigger (mirroring statInline's look) carrying updatedPopBody.
// A plain title= tooltip is kept as a harmless fallback for AT / non-hover users.
func updatedHeaderStat(modelID, versionID int, rel, absDate, versionName, versionDate string) g.Node {
	return h.Div(
		h.Class("cm-updated"),
		g.Attr("tabindex", "0"),
		h.Title("Updated "+absDate),
		h.Span(h.Class("text-slate-500"), g.Text("Updated: ")),
		h.Span(h.Class("font-medium text-slate-200"), g.Text(rel)),
		updatedPopBody(modelID, versionID, absDate, versionName, versionDate),
	)
}

// updatedCardLine renders a search card's "Updated X ago" line as a hover/focus
// popover trigger carrying updatedPopBody, with a title= fallback.
func updatedCardLine(modelID, versionID int, rel, absDate, versionName, versionDate string) g.Node {
	return h.Div(
		h.Class("cm-updated text-xs text-slate-500"),
		g.Attr("tabindex", "0"),
		h.Title("Updated "+absDate),
		h.Span(g.Text("Updated "+rel)),
		updatedPopBody(modelID, versionID, absDate, versionName, versionDate),
	)
}

// modelDetailPage renders the rich model detail page: header + stats, sanitized
// description, tags, a version selector with per-version detail, and a showcase
// image gallery with NSFW handling + a lightbox.
func modelDetailPage(v modelDetailView, sub *store.Subscription, csrf, theme, baseURL string) g.Node {
	m := v.Model
	creator := ""
	if m.Creator != nil {
		creator = m.Creator.Username
	}
	mode := normalizeNSFWMode(v.NSFWMode)
	modelURL := fmt.Sprintf("%s/models/%d", strings.TrimRight(baseURL, "/"), m.ID)

	// The header's "Updated X ago" popover names the (default-selected, i.e. latest)
	// version and its publish date when available — degrades gracefully when absent.
	verName, verDate := "", ""
	if v.Version != nil {
		verName = v.Version.Name
		verDate = isoDatePrefix(v.PublishedAt)
	}

	return page(m.Name, theme, csrf, mode,
		modelHeaderCard(m, creator, csrf, modelURL, sub, v.LastUpdated, v.SelectedVersionID, verName, verDate),
		g.If(strings.TrimSpace(v.Description) != "", modelDescriptionCard(v.Description)),
		// Tags are a compact, de-emphasized inline chip row under the description
		// (not a standalone "Tags" card).
		g.If(len(m.Tags) > 0, modelTagChips(m.Tags)),
		// The version-DEPENDENT region: showcase carousel + version list/detail +
		// the community feed. Selecting a version htmx-swaps this container's
		// innerHTML (see versionLinkAttrs / handleModel's HX path) so the URL
		// updates (hx-push-url) and scroll is preserved, without a full reload.
		h.Div(h.ID(versionRegionID), versionRegionInner(v, csrf)),
		lightboxOverlay(),
		modelPageScript(),
		// The showcase/community carousels' prev/next buttons call cmCarouselScroll.
		libraryCarouselScript(),
	)
}

// versionRegionID is the stable container the version-dependent content lives in;
// version links htmx-swap its innerHTML (never the node itself), so the poll/swap
// target survives every version change.
const versionRegionID = "version-region"

// versionRegionInner renders the version-DEPENDENT content of the model page: the
// showcase carousel for the selected version, the version list (with the active
// version highlighted) + its detail, and the lazy community-feed container keyed
// to the selected version. It is rendered both inside #version-region on the full
// page AND standalone as the HX-swap response (handleModel's HX path), so a
// version change re-renders exactly this content — including re-arming the
// community feed's lazy `revealed` trigger for the new version id.
func versionRegionInner(v modelDetailView, csrf string) g.Node {
	m := v.Model
	mode := normalizeNSFWMode(v.NSFWMode)
	return g.Group([]g.Node{
		showcaseCard(m.ID, v.Images, mode),
		modelVersionsCard(v, csrf),
		communityFeedContainer(m.ID, v.SelectedVersionID),
	})
}

// showcaseCard renders the selected version's showcase carousel (moved out of the
// header into the version region so it re-renders on a version change). The
// carousel tiles route through galleryTile → the thumbnail helper + NSFW handling
// and share the page lightbox.
func showcaseCard(modelID int, images []galleryImage, mode string) g.Node {
	return card(
		h.Div(
			h.Class("mb-2 flex flex-wrap items-center justify-between gap-2"),
			h.H2(h.Class("text-sm font-semibold text-slate-300"), g.Text("Showcase images")),
		),
		modelCardCarousel(modelID, images, mode),
	)
}

// communityFeedContainer is the lazy community-feed container keyed to the
// selected version. It is fetched on `revealed` (when scrolled into view) — not
// inline — because the SearchImages call is slow; see handleModelCommunity. When
// the version region is htmx-swapped, htmx processes this fresh node and re-arms
// the `revealed` trigger for the new version id.
func communityFeedContainer(modelID, versionID int) g.Node {
	return h.Div(
		h.ID("community-feed"),
		hx("get", fmt.Sprintf("/models/%d/community?versionId=%d", modelID, versionID)),
		hx("trigger", "revealed"),
		hx("swap", "innerHTML"),
		g.Text("Loading community images…"),
	)
}

// modelHeaderCard renders the model header: name/type/creator, key stats, the
// Subscribe affordance, and the "View on CivitAI" link. The showcase carousel is
// NOT here — it lives in the version region so it re-renders on a version change.
func modelHeaderCard(m *civitai.ModelDetail, creator, csrf, modelURL string, sub *store.Subscription, lastUpdated time.Time, latestVersionID int, versionName, versionDate string) g.Node {
	return card(
		h.Div(
			h.Class("flex flex-wrap items-start justify-between gap-4"),
			h.Div(
				h.H1(h.Class("text-xl font-semibold"), g.Text(m.Name)),
				h.Div(
					h.Class("mt-1 flex flex-wrap items-center gap-2 text-sm text-slate-400"),
					badge(m.Type, "indigo"),
					g.If(m.NSFW, badge("NSFW", "red")),
					g.If(creator != "", h.A(h.Href("/creators/"+creator),
						h.Class("hover:underline"), g.Text("@"+creator))),
				),
				h.Div(
					h.Class("mt-2 flex flex-wrap gap-4 text-xs text-slate-400"),
					statInline("Downloads", compactCount(m.Stats.DownloadCount)),
					statInline("Likes", compactCount(m.Stats.ThumbsUpCount)),
					statInline("Comments", compactCount(m.Stats.CommentCount)),
					// "Updated X ago" from the newest version's publish date, with a
					// hover/focus popover (absolute date + latest version name/date).
					// Omitted when no parseable date.
					g.If(!lastUpdated.IsZero(), updatedHeaderStat(
						m.ID, latestVersionID,
						humanSince(lastUpdated),
						lastUpdated.Local().Format("2006-01-02 15:04"),
						versionName, versionDate)),
				),
			),
			// Reflect the real subscription state (subscribed → "Subscribed ✓ /
			// Unsubscribe", not-subscribed → collapsed "Subscribe") and a secondary
			// "View on CivitAI" link out to the model's civitai.com page.
			h.Div(
				h.Class("flex flex-col items-end gap-2"),
				subscribeControl(m.ID, sub, csrf),
				viewOnCivitaiLink(modelURL),
			),
		),
	)
}

// viewOnCivitaiLink renders the header's secondary "View on CivitAI" affordance:
// an anchor styled as a civitai outline button (the component CSS is
// attribute-driven, so it styles an <a> too) that opens the model's civitai.com
// page in a new tab, hardened with rel=noopener noreferrer.
func viewOnCivitaiLink(modelURL string) g.Node {
	return h.A(
		h.Href(modelURL),
		h.Target("_blank"),
		g.Attr("rel", "noopener noreferrer"),
		dataAttr("civitai-ui", "button"),
		dataAttr("variant", "outline"),
		dataAttr("size", "sm"),
		g.Text("View on CivitAI ↗"),
	)
}

func statInline(label, value string) g.Node {
	return h.Div(
		h.Span(h.Class("text-slate-500"), g.Text(label+": ")),
		h.Span(h.Class("font-medium text-slate-200"), g.Text(value)),
	)
}

// modelDescriptionCard renders the SANITIZED description HTML. The raw author
// HTML is routed through bluemonday's UGCPolicy (see sanitize.go) before g.Raw,
// so a <script>/onerror=/javascript: in a description cannot execute.
func modelDescriptionCard(rawHTML string) g.Node {
	return card(
		sectionTitle("Description"),
		// Collapsible wrapper: default-collapsed to a max-height with a bottom fade
		// and a Read more / Show less toggle (cmToggleDesc flips data-collapsed). The
		// max-height/fade live in .cm-desc-collapsible (app.css); the sanitization is
		// unchanged — the toggle only bounds the rendered height.
		h.Div(
			h.Class("cm-desc-collapsible"),
			dataAttr("collapsed", "true"),
			h.Div(
				// cm-model-desc deterministically constrains the sanitized author HTML
				// so wide images / <pre> / <table> / long unbroken tokens cannot overflow
				// the card (see .cm-model-desc in app.css).
				h.Class("cm-model-desc cm-desc-content prose-invert max-w-none text-sm text-slate-300 space-y-2 [&_a]:text-indigo-400 [&_a]:underline"),
				g.Raw(sanitizeDescription(rawHTML)),
			),
			// Bottom fade, shown only while collapsed (CSS).
			h.Div(h.Class("cm-desc-fade"), g.Attr("aria-hidden", "true")),
			h.Button(
				h.Type("button"),
				h.Class("cm-desc-toggle"),
				g.Attr("aria-expanded", "false"),
				g.Attr("onclick", "cmToggleDesc(this)"),
				g.Text("Read more"),
			),
		),
	)
}

// modelTagChips renders the model's tags as a compact, muted inline chip row
// (see .cm-tag-chip in app.css) — small and de-emphasized rather than a full
// "Tags" card. Tag text is untrusted civitai data → g.Text escapes each one.
func modelTagChips(tags []string) g.Node {
	return h.Div(
		h.Class("flex flex-wrap items-center gap-1.5 px-1"),
		g.Map(tags, func(t string) g.Node {
			return h.Span(h.Class("cm-tag-chip"), g.Text(t))
		}),
	)
}

// modelVersionsCard renders the version list (each a link that reloads the page
// with that version selected) and the selected version's detail block.
func modelVersionsCard(v modelDetailView, csrf string) g.Node {
	m := v.Model
	var items []g.Node
	for _, ver := range m.ModelVersions {
		selected := ver.ID == v.SelectedVersionID
		cls := "block rounded-md border border-slate-800 px-3 py-1.5 text-sm text-slate-300 hover:bg-slate-800"
		if selected {
			cls = "block rounded-md border border-indigo-600 bg-indigo-950/40 px-3 py-1.5 text-sm text-indigo-200"
		}
		// Version links do an htmx partial swap of #version-region (with hx-push-url
		// so the URL still becomes /models/{id}?version={vid} and direct links /
		// refresh work). The plain href is the no-JS fallback: htmx's hx-get on an
		// <a> with an href degrades to a normal navigation. innerHTML swap preserves
		// scroll (no scroll modifier added).
		versionHref := fmt.Sprintf("/models/%d?version=%d", m.ID, ver.ID)
		items = append(items, h.A(
			h.Href(versionHref),
			hx("get", versionHref),
			hx("target", "#"+versionRegionID),
			hx("swap", "innerHTML"),
			hx("push-url", "true"),
			h.Class(cls),
			h.Div(h.Class("flex items-center justify-between gap-2"),
				h.Span(g.Text(ver.Name)),
				h.Span(h.Class("flex shrink-0 items-center gap-1.5"),
					// In-library indicator: a compact green ✓ (not a text badge, not a
					// button), labeled for AT. Only owned versions carry it.
					g.If(v.LocalVersionIDs[ver.ID], h.Span(
						// cm-ok resolves the green from the civitai success token so the
						// indicator is genuinely green in both themes, independent of the
						// purged Tailwind build (which omits text-green-*).
						h.Class("cm-ok font-semibold"),
						h.Title("In your library"),
						g.Attr("aria-label", "In your library"),
						g.Text("✓"),
					)),
					g.If(ver.BaseModel != "", badge(ver.BaseModel, "blue")),
				),
			),
		))
	}

	return card(
		sectionTitle("Versions"),
		h.Div(
			h.Class("grid gap-4 md:grid-cols-3"),
			h.Div(h.Class("space-y-1.5 md:col-span-1"), g.Group(items)),
			h.Div(h.Class("md:col-span-2"), versionDetail(v, csrf)),
		),
	)
}

// versionDetail renders the selected version's key facts: base model, trigger
// words as copy-able chips, published date, and the file list (each file with a
// Download action that enqueues it — csrf is threaded through for that POST).
func versionDetail(v modelDetailView, csrf string) g.Node {
	ver := v.Version
	if ver == nil {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Select a version to see its details."))
	}
	var rows []g.Node
	if ver.BaseModel != "" {
		rows = append(rows, detailRow("Base model", badge(ver.BaseModel, "blue")))
	}
	if v.PublishedAt != "" {
		rows = append(rows, detailRow("Published", h.Span(h.Class("text-sm text-slate-300"), g.Text(v.PublishedAt))))
	}
	if len(ver.TrainedWords) > 0 {
		rows = append(rows, detailRow("Trigger words", triggerWordChips(ver.TrainedWords)))
	}
	rows = append(rows, detailRow("Files", fileList(v.Model.ID, ver.ID, ver.Files, ver.DownloadURL, csrf)))

	return h.Div(h.Class("space-y-3"), g.Group(rows))
}

func detailRow(label string, value g.Node) g.Node {
	return h.Div(
		h.Div(h.Class("text-xs uppercase tracking-wide text-slate-500"), g.Text(label)),
		h.Div(h.Class("mt-1"), value),
	)
}

// triggerWordChips renders each trained/trigger word as a click-to-copy chip.
func triggerWordChips(words []string) g.Node {
	return h.Div(
		h.Class("flex flex-wrap gap-1.5"),
		g.Map(words, func(word string) g.Node {
			return h.Button(
				h.Type("button"),
				g.Attr("data-copy", word),
				g.Attr("onclick", "cmCopy(this)"),
				h.Class("cm-chip inline-flex items-center gap-1 rounded-md border border-slate-700 bg-slate-800 px-2 py-0.5 text-xs text-slate-200 hover:bg-slate-700"),
				h.Title("Click to copy"),
				g.Text(word),
				h.Span(h.Class("text-slate-500"), g.Text("⧉")),
			)
		}),
	)
}

// fileList renders the selected version's files, each with a Download action that
// enqueues the file into the app's download queue (POST /models/{id}/download,
// CSRF-protected). versionDownloadURL is the version-level fallback used when a
// file carries no own downloadUrl.
func fileList(modelID, versionID int, files []civitai.ModelVersionFile, versionDownloadURL, csrf string) g.Node {
	if len(files) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No files."))
	}
	var rows []g.Node
	for _, f := range files {
		hasURL := strings.TrimSpace(f.DownloadURL) != "" || strings.TrimSpace(versionDownloadURL) != ""
		rows = append(rows, h.Li(
			h.Class("flex items-center justify-between gap-2 rounded border border-slate-800 px-2 py-1 text-xs"),
			h.Span(h.Class("truncate text-slate-300"), g.Text(f.Name)),
			h.Span(h.Class("flex shrink-0 items-center gap-2 text-slate-500"),
				g.If(f.Type != "", badge(f.Type, "slate")),
				g.Text(humanBytes(int64(f.SizeKB*1024))),
				downloadFileButton(modelID, versionID, f.ID, csrf, hasURL),
			),
		))
	}
	return h.Ul(h.Class("space-y-1"), g.Group(rows))
}

// downloadFileID is the stable element id for a file's download control, so the
// POST can outerHTML-swap just that control with its "Queued ✓" / error feedback.
func downloadFileID(modelID, versionID, fileID int) string {
	return fmt.Sprintf("dl-%d-%d-%d", modelID, versionID, fileID)
}

// downloadFileButton renders the per-file Download control. When no download URL
// is resolvable it renders a disabled note instead of a button. The POST carries
// the CSRF token (via hx-vals) and the version/file ids; the server resolves the
// destination path from the model/version/file metadata (no client path).
func downloadFileButton(modelID, versionID, fileID int, csrf string, hasURL bool) g.Node {
	if !hasURL {
		return h.Span(h.Class("text-slate-600"), h.Title("No download URL available"), g.Text("no URL"))
	}
	id := downloadFileID(modelID, versionID, fileID)
	return civButton("outline", "sm", []g.Node{
		h.Type("button"),
		h.ID(id),
		hx("post", fmt.Sprintf("/models/%d/download", modelID)),
		hx("vals", fmt.Sprintf(`{"versionId":"%d","fileId":"%d","csrf_token":%q}`, versionID, fileID, csrf)),
		hx("target", "#"+id),
		hx("swap", "outerHTML"),
	}, g.Text("Download"))
}

// downloadFeedback renders the small fragment that replaces a file's Download
// control after a POST: a green "Queued ✓" on success, or a muted note (already
// queued / error). ok tints it green.
func downloadFeedback(modelID, versionID, fileID int, msg string, ok bool) g.Node {
	cls := "text-xs text-amber-400"
	if ok {
		cls = "text-xs font-medium cm-ok" // green from the civitai success token
	}
	return h.Span(h.ID(downloadFileID(modelID, versionID, fileID)), h.Class(cls), g.Text(msg))
}

// galleryTile renders one showcase image. When blur is true the image is shown
// blurred behind a click-to-reveal overlay; otherwise clicking opens the
// lightbox. Generation metadata is stashed in a hidden node (keyed by the
// caller-supplied metaID, which must be unique across the page so multiple
// galleries/carousels don't collide) the lightbox shows.
// thumbnailWidth is the pixel width requested for grid/carousel showcase tiles.
// The full-resolution original is kept for the click-to-zoom lightbox, so only
// the (many, small) grid tiles pay the reduced-bandwidth path.
const thumbnailWidth = 450

// tileThumbWidth is the width to request for one tile: the thumbnail target,
// capped to the image's own width so we never upscale a small original.
func tileThumbWidth(im galleryImage) int {
	if im.Width > 0 && im.Width < thumbnailWidth {
		return im.Width
	}
	return thumbnailWidth
}

// civitaiThumbURL rewrites an image.civitai.com URL to request an optimized,
// downscaled rendition. The civitai image CDN encodes transform params as a path
// segment between the image UUID and the filename
// (…/<bucket>/<uuid>/anim=false,width=450,optimized=true/<file>); this inserts
// that segment, or replaces an existing transform segment. Non-civitai hosts and
// unparseable/oddly-shaped URLs are returned unchanged — so this doubles as a
// host allowlist for the transform. width<=0 returns the URL unchanged.
func civitaiThumbURL(rawURL string, width int) string {
	if width <= 0 {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host != "image.civitai.com" {
		return rawURL
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 3 {
		return rawURL // not the expected /<bucket>/<uuid>/<file> shape — don't touch
	}
	params := fmt.Sprintf("anim=false,width=%d,optimized=true", width)
	if len(segs) >= 4 && strings.Contains(segs[2], "=") {
		// An existing transform segment sits between the uuid and the filename.
		segs[2] = params
	} else {
		// No transform segment yet — insert one before the filename.
		newSegs := make([]string, 0, len(segs)+1)
		newSegs = append(newSegs, segs[0], segs[1], params)
		segs = append(newSegs, segs[2:]...)
	}
	// Rebuild without url.String() re-encoding (which would percent-escape the
	// commas/equals in the params segment); civitai image URLs carry no query.
	return u.Scheme + "://" + u.Host + "/" + strings.Join(segs, "/")
}

func galleryTile(im galleryImage, metaID string, blur bool) g.Node {
	imgClass := "h-full w-full cursor-zoom-in object-cover transition"
	if blur {
		imgClass += " cm-blur"
	}

	isVideo := isVideoType(im.Type)
	altText := "showcase image"
	if isVideo {
		altText = "showcase video"
	}

	img := h.Img(
		// Video tiles still show a STILL poster thumbnail: civitaiThumbURL forces
		// anim=false, so the CDN returns a poster frame even for a video source.
		h.Src(civitaiThumbURL(im.URL, tileThumbWidth(im))),
		h.Alt(altText),
		h.Loading("lazy"),
		// data-full is the ORIGINAL url (played as <video> for a video, shown at
		// full-res in the lightbox for an image); data-video marks video tiles so
		// cmTileClick opens the correct lightbox media node.
		g.Attr("data-full", im.URL),
		g.Attr("data-meta", metaID),
		g.If(isVideo, g.Attr("data-video", "1")),
		g.If(blur, g.Attr("data-blurred", "1")),
		g.Attr("onclick", "cmTileClick(this)"),
		h.Class(imgClass),
	)

	// True aspect ratio: shape the tile box to the image's own W/H so object-cover
	// fills it without cropping. The carousel is a fixed-HEIGHT strip (.cm-carousel-item
	// sets the height), so the box's width is derived from this ratio. When the
	// dimensions are missing/zero, fall back to a square box (aspect-square).
	wrapClass := "group relative overflow-hidden rounded-md border border-slate-800 bg-slate-900"
	children := []g.Node{}
	if im.Width > 0 && im.Height > 0 {
		children = append(children,
			h.Class(wrapClass),
			h.StyleAttr(fmt.Sprintf("aspect-ratio: %d/%d", im.Width, im.Height)),
		)
	} else {
		children = append(children, h.Class(wrapClass+" aspect-square"))
	}
	children = append(children, img)
	if isVideo {
		// A subtle ▶ badge over the poster so the tile clearly reads as a video.
		// Styled by .cm-video-badge in app.css (theme-aware; not a Tailwind util).
		children = append(children, h.Span(
			h.Class("cm-video-badge"),
			g.Attr("aria-hidden", "true"),
			g.Text("▶"),
		))
	}
	if blur {
		children = append(children, h.Button(
			h.Type("button"),
			g.Attr("onclick", "cmReveal(this)"),
			h.Class("cm-reveal absolute inset-0 z-10 flex items-center justify-center bg-slate-950/40 text-xs font-medium text-slate-100"),
			g.Text("reveal"),
		))
	}
	children = append(children, imageMetaHidden(metaID, im))
	return h.Div(children...)
}

// imageMetaHidden renders the (hidden) generation-metadata block the lightbox
// reveals when the image is expanded.
func imageMetaHidden(metaID string, im galleryImage) g.Node {
	// Reuse the SDK's robust meta decoder (numeric-ish steps/cfg/seed handled)
	// by wrapping the inline meta bytes in an ImageItem.
	meta, state := civitai.ImageItem{Meta: im.Meta}.ParseMeta()
	var rows []g.Node
	if state == civitai.MetaOK {
		add := func(label, val string) {
			if strings.TrimSpace(val) == "" {
				return
			}
			rows = append(rows, h.Div(
				h.Class("text-xs"),
				h.Span(h.Class("text-slate-500"), g.Text(label+": ")),
				h.Span(h.Class("text-slate-200 break-words"), g.Text(val)),
			))
		}
		add("Prompt", meta.Prompt)
		add("Negative", meta.NegativePrompt)
		add("Sampler", meta.Sampler)
		add("Steps", meta.StepsString())
		add("CFG", meta.CfgScaleString())
		add("Seed", meta.SeedString())
		add("Model", meta.Model)
	}
	if len(rows) == 0 {
		rows = append(rows, h.Div(h.Class("text-xs text-slate-500"),
			g.Text("No generation metadata for this image.")))
	}
	return h.Template(
		h.ID(metaID),
		h.Div(h.Class("space-y-1"), g.Group(rows)),
	)
}

// lightboxOverlay is the single shared full-size viewer (hidden until opened by
// cmTileClick). It shows the full image and the selected image's metadata.
func lightboxOverlay() g.Node {
	return h.Div(
		h.ID("cm-lightbox"),
		g.Attr("onclick", "cmCloseLightbox(event)"),
		h.Class("fixed inset-0 z-50 hidden items-center justify-center bg-black/80 p-4"),
		h.Div(
			h.Class("flex max-h-full w-full max-w-5xl flex-col gap-3 overflow-hidden md:flex-row"),
			h.Img(h.ID("cm-lightbox-img"), h.Alt("full image"),
				h.Class("max-h-[85vh] max-w-full rounded-md object-contain")),
			// Video counterpart, hidden until a video tile is opened. muted +
			// playsinline lets it autoplay under browser policy; loop keeps it going.
			// cmOpenLightbox/cmCloseLightbox toggle which of img/video is visible and
			// pause+clear the video on close so it stops playing.
			h.Video(
				h.ID("cm-lightbox-video"),
				g.Attr("controls", ""),
				g.Attr("loop", ""),
				g.Attr("muted", ""),
				g.Attr("playsinline", ""),
				h.Class("hidden max-h-[85vh] max-w-full rounded-md object-contain"),
			),
			h.Div(
				h.ID("cm-lightbox-meta"),
				h.Class("max-h-[85vh] w-full overflow-y-auto rounded-md bg-slate-900 p-3 md:w-80"),
			),
		),
		h.Button(
			h.Type("button"),
			g.Attr("onclick", "cmCloseLightbox()"),
			h.Class("absolute right-4 top-4 rounded-md bg-slate-800 px-3 py-1 text-sm text-slate-200 hover:bg-slate-700"),
			g.Text("Close ✕"),
		),
	)
}

// modelPageScript is the small, self-contained interaction script for the model
// page: click-to-copy chips, NSFW reveal, and the lightbox. No external JS.
func modelPageScript() g.Node {
	const js = `
function cmCopy(btn){
  var t = btn.getAttribute('data-copy') || '';
  if (navigator.clipboard) { navigator.clipboard.writeText(t); }
  var prev = btn.innerHTML;
  btn.innerHTML = 'copied ✓';
  setTimeout(function(){ btn.innerHTML = prev; }, 1200);
}
function cmReveal(btn){
  var img = btn.parentElement.querySelector('img');
  if (img){ img.classList.remove('cm-blur'); img.removeAttribute('data-blurred'); }
  btn.remove();
}
function cmTileClick(img){
  if (img.getAttribute('data-blurred')){ return; }
  cmOpenLightbox(img.getAttribute('data-full'), img.getAttribute('data-meta'), img.getAttribute('data-video'));
}
function cmOpenLightbox(url, metaId, isVideo){
  var box = document.getElementById('cm-lightbox');
  var im = document.getElementById('cm-lightbox-img');
  var vid = document.getElementById('cm-lightbox-video');
  if (isVideo && vid){
    // Show the <video>, play it (muted → autoplay allowed), hide the <img>.
    if (im){ im.classList.add('hidden'); im.src = ''; }
    vid.src = url;
    vid.classList.remove('hidden');
    vid.muted = true;
    var p = vid.play();
    if (p && p.catch){ p.catch(function(){}); }
  } else {
    // Show the <img>, pause+clear the <video>.
    if (vid){ vid.pause(); vid.removeAttribute('src'); vid.load(); vid.classList.add('hidden'); }
    if (im){ im.src = url; im.classList.remove('hidden'); }
  }
  var meta = document.getElementById('cm-lightbox-meta');
  var tpl = document.getElementById(metaId);
  meta.innerHTML = tpl ? tpl.innerHTML : '';
  box.classList.remove('hidden');
  box.classList.add('flex');
}
function cmCloseLightbox(ev){
  if (ev && ev.target && ev.target.id !== 'cm-lightbox' && ev.type === 'click') { return; }
  var box = document.getElementById('cm-lightbox');
  box.classList.add('hidden');
  box.classList.remove('flex');
  var im = document.getElementById('cm-lightbox-img');
  if (im){ im.src = ''; }
  var vid = document.getElementById('cm-lightbox-video');
  // Pause + clear the video src so closing stops playback (not just hides it).
  if (vid){ vid.pause(); vid.removeAttribute('src'); vid.load(); vid.classList.add('hidden'); }
}
function cmToggleDesc(btn){
  var box = btn.closest('.cm-desc-collapsible');
  if(!box){ return; }
  var collapsed = box.getAttribute('data-collapsed') === 'true';
  box.setAttribute('data-collapsed', collapsed ? 'false' : 'true');
  btn.setAttribute('aria-expanded', collapsed ? 'true' : 'false');
  btn.textContent = collapsed ? 'Show less' : 'Read more';
}
document.addEventListener('keydown', function(e){ if (e.key === 'Escape'){ cmCloseLightbox(); } });
// cm popover hover controller: keeps the .cm-vstatus / .cm-updated popovers open
// while the mouse is over EITHER the trigger or the popover (the popover is a child
// of the trigger, so a single wrapper covers both), with a ~200ms grace delay after
// the mouse leaves both — so the now-deeplinked popover contents are reliably
// hoverable/clickable. Event delegation on document is required because these
// popovers are inserted LAZILY via htmx after page load (direct binding would miss
// them). The CSS :hover/:focus-within rules remain as fallback (keyboard a11y).
(function(){
  var timers = new WeakMap();
  function trigOf(el){ return (el && el.closest) ? el.closest('.cm-vstatus, .cm-updated') : null; }
  function openPop(trig){
    var t = timers.get(trig);
    if (t){ clearTimeout(t); timers.delete(trig); }
    trig.classList.add('cm-pop-open');
  }
  function closeSoon(trig){
    var t = timers.get(trig);
    if (t){ clearTimeout(t); }
    timers.set(trig, setTimeout(function(){ trig.classList.remove('cm-pop-open'); timers.delete(trig); }, 200));
  }
  document.addEventListener('mouseover', function(e){
    var trig = trigOf(e.target);
    if (trig){ openPop(trig); }
  });
  document.addEventListener('mouseout', function(e){
    var trig = trigOf(e.target);
    if (!trig){ return; }
    // Moving to another node still inside the trigger (incl. the popover) → stay open.
    var to = e.relatedTarget;
    if (to && trig.contains(to)){ return; }
    closeSoon(trig);
  });
})();
`
	return h.Script(g.Raw(js))
}
